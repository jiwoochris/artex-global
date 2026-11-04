package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/yukiteruamano/artex/db"
)

// Regression defence test that keeps the "user-facing" messages among engine.go's work-control
// and intent-intervention errors as their exact English source text (assertEnglishMessage).
//
// Call-graph judgement (see the comment in engine.go's constant block):
//   - ControlWork's four errors (no running work, already controlling, settlement-wait cancelled, settlement-wait
//     timeout) → applyIntentControl(task_control.go) → controlIntent(server.go:1236) →
//     writeErr 409. User-only, and they never reach the agent tool (actool) path.
//   - runDetachedIntent's two errors (Worker not ready, resume CAS conflict) →
//     sendWorkerMessage(intent_intervention.go:147) → writeErr. User-only.
//   - Conversely the Chinese in SteerWork, KillWork (the steer_work, kill_work tools actool.Errorf) and
//     transitionIntentState (internal state transition log) is brain input and logs, so it is preserved and is
//     not covered by this test.

// TestControlWorkNoRunningWorkErrorLocalized drives ControlWork directly, without DB or engine, to check
// that it really returns the English error when no work is running. The run==nil branch only reads the e.work map,
// so NewEngine(nil) reaches the end. Because it wraps the errWorkControlConflict sentinel with %w, it also
// checks that the errors.Is relation is preserved. The English prefix is pinned because the wrapped
// sentinel supplies the tail.
func TestControlWorkNoRunningWorkErrorLocalized(t *testing.T) {
	e := NewEngine(nil)
	err := e.ControlWork(context.Background(), 42, "pause")
	if err == nil {
		t.Fatal("ControlWork returned nil although no work is running")
	}
	if !errors.Is(err, errWorkControlConflict) {
		t.Fatalf("errors.Is(err, errWorkControlConflict) = false, err=%v", err)
	}
	assertEnglishMessage(t, "control_work_no_running", err.Error(),
		"work control conflict: no running Worker for intent 42 (it may have already stopped or not yet been assigned)")
}

// TestEngineUserFacingErrorsLocalized pins the remaining user-facing format strings that are behind
// engine/DB gates or goroutine timing and hard to drive to the end (the same approach as the paths
// behind the gates in finding_retests and finding_traffic). Each format string is filled with representative args, then checked.
func TestEngineUserFacingErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg, wantEnglish string
	}{
		{"work_control_busy", fmt.Errorf(errWorkControlBusyFmt, errWorkControlConflict, int64(42), "pause").Error(),
			"work control conflict: intent 42 already has a pause control in progress"},
		{"work_control_wait", fmt.Errorf(errWorkControlWaitFmt, int64(42), "pause", context.DeadlineExceeded).Error(),
			"An error occurred while waiting for intent 42 to complete its pause wrap-up: context deadline exceeded"},
		{"detached_worker_not_ready", errDetachedWorkerNotReady, "Worker is not ready yet"},
		{"detached_state_conflict", fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict).Error(),
			"intent state changed concurrently: the intent is no longer in the paused state"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}
