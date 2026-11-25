package server

import "testing"

// Regression guard test that keeps the user-facing HTTP error response text of
// finding_traffic.go in Korean. The Korean check reuses assertKoreanError (contains Hangul ·
// zero Chinese Han), built in F3a. The paths that return the four messages
// (findingTrafficAccess · bindFindingTraffic · editFindingTraffic) all sit behind the
// s.m.pg.GetFinding (DB) gate and cannot run end to end on this DB-less host, so the
// constants themselves are pinned (same approach as the DB-gated paths in goals_api ·
// notify_api). This test fails if someone reverts these literals to Chinese.
//
// readEvidencePreview's errors.New (offset/length validation) · binary body placeholder and
// the roTool("get_finding_traffic") tool description are brain input fed back as agent tool
// results, so their originals (Chinese) are intentionally preserved and are not asserted by
// this test.
func TestFindingTrafficErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg string
	}{
		{"inherited_readonly", errFindingTrafficInheritedReadonly},
		{"select_required", errFindingTrafficSelectRequired},
		{"version_required", errFindingTrafficVersionRequired},
		{"binding_ids_required", errFindingTrafficBindingIDsRequired},
	} {
		assertKoreanError(t, c.label, c.msg)
	}
}
