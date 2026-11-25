package server

import "testing"

// Regression guard test that keeps the user-facing HTTP error response text of
// finding_retests.go in Korean. The Korean check reuses assertKoreanError (contains Hangul ·
// zero Chinese Han), built in F3a. The startFindingRetest handler that returns the four
// messages goes through the s.pg(w) (DB) gate right at the start, so it cannot run end to end
// on this DB-less host; the constants themselves are pinned instead (same approach as the
// DB-gated paths in finding_traffic · goals_api · notify_api). This test fails if someone
// reverts these literals to Chinese.
//
// The tool descriptions (get_finding_retest_context · record_finding_retest_result) ·
// parameter descriptions · actool.Errorf (the round-not-linked notice) and
// seedFindingRetester's DB-seeded agent name · profile · note are either brain input the
// agent reads or seed data, so their originals (Chinese) are intentionally preserved and are
// not asserted by this test (see the comment on the constant block in finding_retests.go).
func TestFindingRetestErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"notes_too_long", errFindingRetestNotesTooLong},
		{"agent_missing", errFindingRetestAgentMissing},
		{"tool_required", errFindingRetestToolRequired},
		{"service_stopping", errFindingRetestServiceStopping},
	} {
		assertKoreanError(t, c.label, c.msg)
	}
}
