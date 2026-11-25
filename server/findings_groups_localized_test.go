package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard test that keeps the user-facing text of findings_groups.go in Korean.
// The Korean check reuses assertKoreanError (contains Hangul · zero Chinese Han), built in F3a.

// TestDeepenFindingBodyTooLargeLocalized confirms over real HTTP that the oversized-body (413)
// response is Korean. This path returns at the MaxBytesReader decode step and finishes before
// reaching s.m.pg (DB), so it runs end to end even with an empty Server.
func TestDeepenFindingBodyTooLargeLocalized(t *testing.T) {
	s := &Server{}
	body := `{"description":"` + strings.Repeat("a", 33<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen", strings.NewReader(body))
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	s.deepenFinding(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("상태 코드 = %d, 기대 = %d (본문 %q)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("응답 JSON 파싱 실패: %v (본문 %q)", err, rec.Body.String())
	}
	const want = "요청 본문이 너무 큽니다"
	if resp.Error != want {
		t.Fatalf("응답 문구 = %q, 기대 = %q", resp.Error, want)
	}
	assertKoreanError(t, "body_too_large", resp.Error)
}

// TestFindingFollowUpAuditSummaryLocalized asserts that the follow-up intent activity summary
// constant is Korean. AddFindingFollowUpIntent, which stores this summary, needs a DB
// transaction and cannot run end to end on this DB-less host, so the constant itself is
// asserted. This text is a display-only summary for the activity timeline, separate from the
// intent payload the agent reads (the description the user entered).
func TestFindingFollowUpAuditSummaryLocalized(t *testing.T) {
	assertKoreanError(t, "follow_up_summary", auditFindingFollowUpSummary)
}
