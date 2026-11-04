// Package selfupdate implements ARTEX's one-click page update: pull the new binary
// from GitHub Release, verify it, stage it, and atomically swap it in on the next start.
//
// Division of work (see start.sh / start.bat):
//
//	start script = dumb supervisor loop, only responsible for "after the process exits, decide by exit code whether to start it again"
//	this package = all the error-prone logic (download / SHA256 check / smoke test / swap / rollback on failure)
//
// The swap lives in Go instead of in the scripts because the sha256 check and the smoke test would
// have to be written twice on sh and bat (sha256sum / shasum / certutil), and this is exactly the step that must not go wrong — swapping in a
// binary that cannot run makes the supervisor faithfully restart it over and over, and the user can only rescue the machine by hand.
//
// One full upgrade goes through three process starts:
//
//	① old server receives /api/update/apply → download and verify → stage artex.new → exit 75
//	② script starts the old version again → Bootstrap finds artex.new → verify + smoke test → swap → exit 75
//	③ script starts again, now the new version → Bootstrap records one attempt → clears the marker after a successful start
//
// Any failed step falls back to the old version: if ② fails verification, delete the staged file and keep running the old version; if ③ does not survive
// long enough to clear the marker (it crashes instead of coming up) 3 times in a row, artex.old is swapped back automatically.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart is the exit code for "supervisor, please start me again" (EX_TEMPFAIL). When the start script sees it
// it reruns immediately, without counting toward the crash backoff. 0 means the user stopped normally (the script leaves the loop); everything else counts as a crash.
const ExitRestart = 75

// maxAttempts is how many start attempts are allowed after the swap. The new version bumps the count by 1 on every start and
// clears the marker once it survives settleDelay; maxAttempts crashes in a row mean the new version cannot start at all, so it rolls back automatically.
const maxAttempts = 3

// Paths holds every file one upgrade involves, all rooted in the **directory the executable lives in**.
// CWD is deliberately not used: when running as a service the working directory may be / or any other path, and using
// CWD would drop the staged files somewhere else and break the swap logic outright.
type Paths struct {
	Dir     string // directory the executable lives in
	Current string // currently running binary            artex      / artex.exe
	New     string // staged new version                  artex.new  / artex.new.exe
	Sum     string // sha256 of the new version (hex)     artex.new.sha256 / artex.new.exe.sha256
	Old     string // old version backed up before swap    artex.old  / artex.old.exe
	Marker  string // upgrade state marker                artex.upgrade.json
}

// ResolvePaths derives every upgrade path from the current executable.
//
// On Windows the .new/.old names must keep the .exe suffix too, otherwise both the smoke test and the execution after the swap fail,
// so the suffix is stripped first and appended again, which keeps the naming symmetrical on both platforms.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("failed to determine the executable location: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // ".exe" on Windows, usually empty on Unix
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker records the progress of one swap; it triggers the automatic rollback when the new version cannot start.
type marker struct {
	From     string `json:"from"`     // version before the upgrade
	To       string `json:"to"`       // target version
	Attempts int    `json:"attempts"` // start attempts since the swap
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged removes the staged files. A successful swap, a failed verification and a user cancel all go through it, so that a leftover
// artex.new is not retried on the next start.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions compares two version strings and returns -1/0/1 (a<b / a==b / a>b).
// ok=false means at least one side is not a comparable version (for example "dev" from a local development build, or
// the "0.3.7-2-gabc1234-dirty" produced by git describe), in which case the caller should disable one-click update,
// otherwise it would "upgrade" a development build to the release version and overwrite uncommitted changes.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion parses a version of the form "v0.3.7" / "0.3.7" into a [3]int.
//
// Only a clean three-part version is accepted: build.sh uses git describe on non-tag builds to produce
// suffixed versions such as "0.3.7-2-gabc1234", which must be judged incomparable instead of being treated as
// 0.3.7 — otherwise a development build is misjudged as "already up to date" or overwritten by the release version.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker reports whether the process runs in a container. Under Docker the swap writes the container writable layer,
// so rebuilding the container with `docker compose up -d` falls back to the version bundled in the image — that is the expected behaviour
// (the user is pulling a new image at that point anyway), but the frontend must be able to say so clearly.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
