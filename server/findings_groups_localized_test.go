package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard that keeps the user-facing messages of findings_groups.go as their exact
// English source text (assertEnglishMessage).

// TestDeepenFindingBodyTooLargeLocalized checks over real HTTP that the oversized-body (413)
// response is the English source message. This path returns from the MaxBytesReader decode step before it reaches
// s.m.pg (the DB), so it runs to completion even with an empty Server.
func TestDeepenFindingBodyTooLargeLocalized(t *testing.T) {
	s := &Server{}
	body := `{"description":"` + strings.Repeat("a", 33<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen", strings.NewReader(body))
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	s.deepenFinding(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want = %d (body %q)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse the response JSON: %v (body %q)", err, rec.Body.String())
	}
	const want = "The request body is too large"
	if resp.Error != want {
		t.Fatalf("response message = %q, want = %q", resp.Error, want)
	}
	assertEnglishMessage(t, "body_too_large", resp.Error, want)
}

// TestFindingFollowUpAuditSummaryLocalized asserts that the follow-up intent activity summary constant is English.
// AddFindingFollowUpIntent, which stores this summary, needs a DB transaction and cannot run to
// completion on this host without a DB, so the constant itself is asserted. This message is a
// summary shown only on the activity timeline, separate from the intent payload the agent reads (the description the user entered).
func TestFindingFollowUpAuditSummaryLocalized(t *testing.T) {
	assertEnglishMessage(t, "follow_up_summary", auditFindingFollowUpSummary,
		"User submitted an intent to exploit the vulnerability further")
}
