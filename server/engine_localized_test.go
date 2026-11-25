package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Regression guard test that keeps in Korean the "user-facing" messages among engine.go's
// task-control · intent-intervention errors. The Korean check reuses assertKoreanError
// (contains Hangul · zero Chinese Han), built in F3a.
//
// Call-graph ruling (see the comment on the constant block in engine.go):
//   - ControlWork's four errors (no running work · already being controlled · settle-wait
//     cancelled · settle-wait timed out) → applyIntentControl (task_control.go) →
//     controlIntent (server.go:1236) → writeErr 409. User-only; they never reach an agent
//     tool (actool) path.
//   - runDetachedIntent's two errors (Worker not ready · resume CAS conflict) →
//     sendWorkerMessage (intent_intervention.go:147) → writeErr. User-only.
//   - Conversely, the Chinese in SteerWork · KillWork (steer_work · kill_work tool
//     actool.Errorf) · transitionIntentState (internal state-transition logs) is brain input ·
//     logs, so it is preserved and is not what this test targets.

// TestControlWorkNoRunningWorkErrorLocalized drives ControlWork directly, with no DB · engine,
// to check that it really returns a Korean error when there is no running work. The run==nil
// branch only looks at the e.work map, so NewEngine(nil) reaches it end to end. Since it wraps
// the errWorkControlConflict sentinel with %w, it also checks that the errors.Is relationship
// is preserved. Fails if someone reverts this literal to Chinese.
func TestControlWorkNoRunningWorkErrorLocalized(t *testing.T) {
	e := NewEngine(nil)
	err := e.ControlWork(context.Background(), 42, "pause")
	if err == nil {
		t.Fatal("실행 중 work 가 없는데 ControlWork 가 nil 을 반환했습니다")
	}
	if !errors.Is(err, errWorkControlConflict) {
		t.Fatalf("errors.Is(err, errWorkControlConflict) = false, err=%v", err)
	}
	assertKoreanError(t, "control_work_no_running", err.Error())
}

// TestEngineUserFacingErrorsLocalized pins the remaining user-facing format strings that sit
// behind engine/DB gates or goroutine timing and are hard to drive end to end (same approach
// as the gated paths in finding_retests · finding_traffic). Format strings are filled with
// representative arguments before being checked.
func TestEngineUserFacingErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"work_control_busy", fmt.Errorf(errWorkControlBusyFmt, errWorkControlConflict, int64(42), "pause").Error()},
		{"work_control_wait", fmt.Errorf(errWorkControlWaitFmt, int64(42), "pause", context.DeadlineExceeded).Error()},
		{"detached_worker_not_ready", errDetachedWorkerNotReady},
		{"detached_state_conflict", fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict).Error()},
	} {
		assertKoreanError(t, c.label, c.msg)
	}
}
