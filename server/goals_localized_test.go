package server

import "testing"

// Regression guard test that keeps in Korean the precondition validation error messages of
// task resume (admitPausedTask) in goals.go. The Korean check reuses assertKoreanError
// (contains Hangul · zero Chinese Han), built in F3a. The admitTaskWhen(requirePaused=true)
// path that returns the two messages goes through the engine gates s.m.Task() ·
// s.engine.IsDeleting() · beginTaskOperation() after taking the s.concMu lock, so it cannot
// run end to end on this host without a DB · engine; the constants themselves are pinned
// instead (same approach as the gated paths in finding_retests · finding_traffic). This test
// fails if someone reverts these literals to Chinese.
//
// Call-graph ruling (see the comment on the constant block in goals.go): both messages are
// user-only, exposed only through task-control responses, single (server.go:1194 → writeErr
// 409) and batch (task_control.go:298 → items[].error). The orchestrator path
// (orchestration.go:343) is fixed to action="pause" and never reaches the resume validation,
// so they are not brain input.
func TestGoalResumeErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"resume_terminal", errGoalResumeTerminal},
		{"resume_not_paused", errGoalResumeNotPaused},
	} {
		assertKoreanError(t, c.label, c.msg)
	}
}
