// Package selfupdate implements ARTEX's one-click update from the web UI: it pulls the new binary from
// a GitHub Release, verifies it, stages it, and swaps it in atomically on the next startup.
//
// Overall division of labor (see start.sh / start.bat):
//
//	start script = a dumb supervisor loop, responsible only for "after the process exits, decide from the exit code whether to relaunch it"
//	this package = all the error-prone logic (download / SHA256 verification / smoke test / swap / rollback on failure)
//
// The swap lives in Go rather than in the scripts because the sha256 verification and smoke test would have
// to be written twice, for sh and bat (sha256sum / shasum / certutil), and this is exactly the link that must
// never fail — swap in a binary that cannot run, and the supervisor will faithfully relaunch it over and over,
// leaving the user no option but to log into the machine and rescue it by hand.
//
// A complete upgrade goes through three process starts:
//
//	① old server receives /api/update/apply → download and verify → stage artex.new → exit 75
//	② script relaunches the old version → Bootstrap finds artex.new → verify + smoke test → swap → exit 75
//	③ script relaunches, now running the new version → Bootstrap records one attempt → clears the marker after a successful start
//
// A failure at any step falls back to the old version: if verification fails at ②, the staged binary is deleted and the
// old version keeps running; at ③, if the new version fails 3 times in a row to live long enough to clear the marker
// (crashing because it cannot start), artex.old is automatically swapped back.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart is the "please, supervisor, relaunch me" exit code (EX_TEMPFAIL). When the start script sees it,
// it reruns immediately without counting it toward the crash backoff. 0 means the user stopped normally (the
// script exits its loop); everything else is treated as a crash.
const ExitRestart = 75

// maxAttempts is the number of startup attempts allowed after a swap. Each start of the new version increments
// the counter by 1, and surviving settleDelay clears the marker; crashing maxAttempts times in a row means the
// new version cannot start at all, so it rolls back automatically.
const maxAttempts = 3

// Paths are all the files involved in an upgrade, all placed under **the directory containing the executable**.
// CWD is deliberately not used: when running as a service the working directory may be / or any path, and using
// CWD would make the staged binary land somewhere else, breaking the swap logic outright.
type Paths struct {
	Dir     string // directory containing the executable
	Current string // currently running binary         artex      / artex.exe
	New     string // staged new version               artex.new  / artex.new.exe
	Sum     string // sha256 (hex) of the new version  artex.new.sha256 / artex.new.exe.sha256
	Old     string // old version backed up pre-swap   artex.old  / artex.old.exe
	Marker  string // upgrade state marker             artex.upgrade.json
}

// ResolvePaths derives all upgrade paths from the current executable.
//
// On Windows, .new/.old must also carry the .exe suffix, or the smoke test and execution after the swap both fail,
// so the suffix is stripped first and then appended, which keeps the naming symmetric on both platforms.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("실행 파일 위치 확인 실패: %w", err)
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

// marker records the progress of a swap and is used to trigger an automatic rollback when the new version cannot start.
type marker struct {
	From     string `json:"from"`     // version before the upgrade
	To       string `json:"to"`       // target version
	Attempts int    `json:"attempts"` // number of startup attempts since the swap
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

// cleanStaged removes the staged files. A successful swap, a failed verification and a user cancellation all go
// through it, so a leftover artex.new is not retried on the next startup.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions compares two version numbers and returns -1/0/1 (a<b / a==b / a>b).
// ok=false means at least one side is not a comparable version number (e.g. "dev" from a local development build,
// or "0.3.7-2-gabc1234-dirty" produced by git describe); in that case the caller should disable one-click update,
// or it would "upgrade" an in-development build to the stable release and overwrite uncommitted changes.
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

// parseVersion parses a version number of the form "v0.3.7" / "0.3.7" into a [3]int.
//
// Only clean three-part versions are accepted: for non-tag builds build.sh uses git describe to produce
// suffixed versions such as "0.3.7-2-gabc1234", and those must be judged incomparable rather than treated as
// 0.3.7 — otherwise a development build would be misjudged as "already up to date" or be overwritten by the stable release.
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

// InDocker reports whether the process is running in a container. Under Docker, the swap writes to the container's
// writable layer, and recreating the container with `docker compose up -d` reverts to the version bundled in the image
// — this is expected behavior (at that point the user is pulling a new image anyway), but the frontend must be able to explain that clearly.
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
