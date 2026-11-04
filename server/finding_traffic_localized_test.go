package server

import "testing"

// Regression guard that keeps the user-facing HTTP error responses of finding_traffic.go
// as their exact English source text (assertEnglishMessage). The four paths that return these messages (findingTrafficAccess·bindFindingTraffic·
// editFindingTraffic) all sit behind the s.m.pg.GetFinding(DB) gate and cannot run to completion
// on this host without a DB, so the constants themselves are pinned (the same approach as the
// DB-gated paths in goals_api·notify_api). If anyone reverts these literals to another language, this test fails.
//
// readEvidencePreview's errors.New values (offset/length validation), the binary-body
// placeholder and the roTool("get_finding_traffic") tool description are model input fed back
// as agent tool results, so they deliberately keep their original wording and are not asserted by this test.
func TestFindingTrafficErrorsLocalized(t *testing.T) {
	for _, c := range []struct {
		label, msg, wantEnglish string
	}{
		{"inherited_readonly", errFindingTrafficInheritedReadonly, "Inherited vulnerabilities are read-only and must be edited in the source task"},
		{"select_required", errFindingTrafficSelectRequired, "Select traffic"},
		{"version_required", errFindingTrafficVersionRequired, "version and a valid request body are required"},
		{"binding_ids_required", errFindingTrafficBindingIDsRequired, "binding_ids is required"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}
