package server

import "testing"

// Regression guard on the precondition errors of task resume (admitPausedTask) in
// goals.go: they are their exact English source text (assertEnglishMessage). The admitTaskWhen(requirePaused=true) path that returns both messages
// goes through the s.concMu lock and then the s.m.Task(), s.engine.IsDeleting() and
// beginTaskOperation() engine gates, so it cannot run to completion on a host with no
// DB or engine; the constants are therefore pinned directly (the same approach as the
// gated paths in finding_retests and finding_traffic). If someone reverts one of these
// literals to another language, this test fails.
//
// Call-graph check (see the constant block in goals.go): both messages are user-only,
// surfaced exclusively through task-control responses — single (server.go:1194 →
// writeErr 409) and batch (task_control.go:298 → items[].error). The orchestrator path
// (orchestration.go:343) is fixed to action="pause" and never reaches resume
// validation, so this is not brain input.
func TestGoalResumeErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg, wantEnglish string
	}{
		{"resume_terminal", errGoalResumeTerminal, "A terminated task cannot be resumed"},
		{"resume_not_paused", errGoalResumeNotPaused, "Only paused tasks can be resumed"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}
