package db

import "testing"

// wantEnglish asserts that a user-visible literal is exactly its English source text.
func wantEnglish(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: want %q, got %q", name, want, got)
	}
}

// TestFindingRetestReasonsLocalized asserts that the two completion reasons stored in the
// finding_retests.error column and exposed as item.error on the retest panel
// (finding-retest-panel) are their exact English source text. They share that column and panel
// with the sibling reasons (convRetest*) in server/conversations.go, so one stale entry would
// make the panel mix languages.
func TestFindingRetestReasonsLocalized(t *testing.T) {
	wantEnglish(t, "retestNoConclusionReason", retestNoConclusionReason,
		"The agent finished without saving a retest conclusion. Check the conversation and start the retest again")
	wantEnglish(t, "retestServiceRestartReason", retestServiceRestartReason,
		"The service restarted, so the retest was interrupted. Start it again")
}
