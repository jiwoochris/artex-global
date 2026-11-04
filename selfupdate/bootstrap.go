package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv makes a child process launched by the smoke test skip Bootstrap entirely.
//
// Strictly speaking it is not needed: the child's os.Executable() is artex.new, so every
// derived path is prefixed with .new and never touches the real update files. Relying on
// that coincidence is too fragile — an explicit short-circuit is obvious and also saves
// the child one pointless disk probe.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action is the instruction Bootstrap gives back to main.
type Action int

const (
	// Continue: start the server as usual.
	Continue Action = iota
	// Restart: exit immediately with ExitRestart so the supervisor script relaunches.
	Restart
)

// State describes the update state at this startup, so /api/update/check can tell the
// frontend truthfully whether the last update succeeded or was rolled back.
type State struct {
	Pending     bool   // swapped but not yet confirmed stable
	RolledBack  bool   // an automatic rollback ran during this startup
	FailedStage bool   // the staged file failed verification/smoke and was discarded
	Detail      string // one user-facing sentence
}

// Bootstrap runs at the very start of main and must be called before any port is
// listened on or any database is opened.
//
// Three situations:
//
//  1. a staged file artex.new exists → verify + smoke test; on success swap and ask for a restart, on failure discard and keep running the old version
//  2. only the marker file remains                 → a swap just happened; count an attempt and roll back after enough consecutive failures
//  3. neither                                       → normal startup
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] skipping bootstrap: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged handles the "staged file exists" situation: swap when verification passes,
// discard when it fails.
//
// This is the only place in the whole update chain that overwrites the executable, and it
// is the last gate — the smoke test keeps out corrupted downloads, wrong architectures and
// missing dynamic libraries. Once a binary that cannot run is let through, the supervisor
// script relaunches it tirelessly while the Go code never gets to run, so automatic
// rollback can never happen either.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] staged new version failed verification, discarded, continuing on the current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "The staged update failed verification and was discarded: " + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] swap failed, continuing on the current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "The version swap failed: " + err.Error()}
	}

	// Swap succeeded. Keep the marker so the next startup (running the new version) can
	// confirm whether it is stable.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] failed to write the update marker (automatic rollback is now unavailable): %v", err)
	}
	log.Printf("[update] swapped in %s, exiting to restart (exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback handles the "startup after a swap" situation: count attempts and swap
// the old version back once the limit is exceeded.
//
// The counter only advances once Go code runs, so it covers "executes but crashes during
// init" (incompatible config, port taken, failed DB migration), while "cannot exec at all"
// is caught by the pre-swap smoke test; only the two together are complete.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// Do not restart again after a failed rollback, or this loops forever. Clear the
			// marker and let the process start as it is — if it cannot, the user can at least
			// see why in the logs.
			log.Printf("[update] new version failed to start %d times and the rollback failed: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "The new version failed to start and the rollback also failed: " + err.Error()}
		}
		log.Printf("[update] new version failed to start %d times, rolled back to %s, exiting to restart (exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("The new version failed to start, so it was rolled back to the previous version (%s)", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] failed to update the update marker: %v", err)
	}
	log.Printf("[update] new version starting (attempt %d/%d); the update is confirmed once it runs stably",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle confirms that the new version is running stably and clears the update marker.
//
// main calls it with a delay after the HTTP listener is up: only surviving that long counts,
// otherwise the marker stays put and the next startup keeps counting attempts until a
// rollback triggers.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // not a startup after an update, nothing to do
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] failed to clear the update marker: %v", err)
		return
	}
	log.Printf("[update] new version is stable, update complete (previous version kept as %s)", p.Old)
}

// SettleDelay is how long the new version must stay up before it counts as "survived".
const SettleDelay = 30 * time.Second

// verifyStaged verifies the staged file: compare the SHA256 first, then actually launch it once.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("Failed to read the checksum: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("Failed to compute the checksum: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 does not match (the download was corrupted or tampered with)")
	}
	return smokeTest(p.New)
}

// smokeTest launches the new binary with -h to confirm it really executes on this system.
// This keeps out a whole class of problems: truncated downloads, wrong architecture (exec
// format error), missing dependencies.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("Failed to grant the execute permission: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("Smoke test timed out (the new binary did not respond)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("Smoke test failed: %v: %s", err, snippet)
	}
	return nil
}

// swap replaces the current binary with the staged new version.
//
// Unix allows renaming a running executable, so this needs neither platform branching nor
// stopping ourselves first.
func swap(p Paths) error {
	// Clear the backup left by the previous update before it is replaced.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Failed to clean up the previous backup %s: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("Failed to back up the current version: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// The swap failed but the current version has already been moved away; it must be put
		// back exactly as it was or the next startup has no executable.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("Installing the new version failed (%v) and restoring the current version failed too: %w", err, rerr)
		}
		return fmt.Errorf("Failed to install the new version: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback swaps back the old version that swap backed up.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("There is no backup to roll back to (%s): %w", p.Old, err)
	}
	// Move the version that will not start aside as .failed for investigation instead of
	// deleting it outright.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("Failed to move the failed version aside: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("Failed to restore the previous version: %w", err)
	}
	return nil
}

// Rollback implements /api/update/rollback: actively return to the previous version.
// It only swaps files; restarting is left to the supervisor script (the caller then exits
// with ExitRestart).
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("There is no previous version to roll back to (%s is missing)", p.Old)
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("Refusing the rollback because the previous version cannot be executed: %w", err)
	}
	// Swap current and backup, so a rollback can itself be rolled back.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("Failed to move the current version: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("Failed to install the previous version: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] failed to tidy the backup after the rollback (does not affect running): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup reports whether a previous version exists to roll back to, so the frontend can
// decide whether to show the rollback button.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown version"
	}
	return s
}
