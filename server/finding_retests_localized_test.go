package server

import "testing"

// Regression defence test that keeps the user-facing HTTP error response messages of
// finding_retests.go as their exact English source text (assertEnglishMessage). The startFindingRetest handler that returns the four messages
// passes the s.pg(w) (DB) gate at the very start, so it cannot run to the end on this host without a DB,
// hence the constants themselves are pinned (same approach as the DB-gated paths in finding_traffic,
// goals_api and notify_api). If someone reverts these literals to another language, this test fails.
//
// Tool descriptions (get_finding_retest_context, record_finding_retest_result), parameter descriptions,
// actool.Errorf (no run linked notice) and seedFindingRetester's DB seed agent name,
// profile and note are brain input read by the agent or seed data, so their original wording is
// deliberately preserved and is not asserted by this test (see the comment in the finding_retests.go constant block).
func TestFindingRetestErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg, wantEnglish string
	}{
		{"notes_too_long", errFindingRetestNotesTooLong, "The re-validation note can be up to 4000 characters"},
		{"agent_missing", errFindingRetestAgentMissing, "The vulnerability revalidation agent is missing or disabled. Configure retester in Agent Management"},
		{"tool_required", errFindingRetestToolRequired, "Enable and connect tools to the revalidation agent: "},
		{"service_stopping", errFindingRetestServiceStopping, "The service is shutting down"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}
