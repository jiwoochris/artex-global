package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// This test was added together with bringing GET /api/intercept/judge/usage, which existed
// in upstream ARTEX (the 0.3.15 line), back into this fork. It checks that the running token
// total and the per-day series consumed by the fallback judge are aggregated correctly
// against a real DB, and that without a DB it responds with an empty series rather than a 500.
//
// Skipped like other integration tests when no DB is configured (see server/testmain_test.go).
//
// These tests cover the endpoint restored from upstream ARTEX: judge token totals
// and the per-day series, plus the no-database graceful path.
const judgeFixtureModel = "judge-usage-test-fixture"

// judgeUsageRig opens the shared test database and registers fixture cleanup.
func judgeUsageRig(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	// llm_usage is created not by schema.sql but by EnsureLLMUsageTable() on the Go side.
	// This follows the same approach as the existing tests in the db package
	// (finding_traffic_archive_test.go, etc.).
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatalf("EnsureLLMUsageTable 실패: %v", err)
	}
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE model = $1`, judgeFixtureModel)
	}
	cleanup()
	t.Cleanup(cleanup)
	return &Server{m: &Manager{pg: d}}, d
}

// callJudgeUsage invokes the handler directly (auth is covered by auth tests).
func callJudgeUsage(t *testing.T, s *Server, query string) (int, db.JudgeUsage) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/intercept/judge/usage"+query, nil)
	s.interceptJudgeUsage(rec, req)
	var out db.JudgeUsage
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("응답 JSON 파싱 실패: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec.Code, out
}

// insertUsage writes one ledger row. tsShiftDays > 0 backdates it for the window test.
func insertUsage(t *testing.T, d *db.DB, worker string, in, out, cacheRead, cacheWrite int, tsShiftDays int) {
	t.Helper()
	_, err := d.Exec(`
INSERT INTO llm_usage(worker, model, profile_name, input_tokens, output_tokens, cache_read, cache_write, status, ts)
VALUES ($1,$2,'fixture',$3,$4,$5,$6,'ok', now() - ($7 * interval '1 day'))`,
		worker, judgeFixtureModel, in, out, cacheRead, cacheWrite, tsShiftDays)
	if err != nil {
		t.Fatalf("fixture insert 실패: %v", err)
	}
}

// TestInterceptJudgeUsageAggregates: the running total counts only worker='judge' rows, a
// bucket for today appears in the per-day series, and non-judge rows must not affect any value.
func TestInterceptJudgeUsageAggregates(t *testing.T) {
	s, d := judgeUsageRig(t)

	base, err := d.JudgeUsageStats(30)
	if err != nil {
		t.Fatalf("baseline JudgeUsageStats 실패: %v", err)
	}

	insertUsage(t, d, "judge", 100, 20, 7, 3, 0)
	insertUsage(t, d, "judge", 50, 10, 0, 0, 0)
	insertUsage(t, d, "worker", 999, 999, 999, 999, 0) // must be excluded from aggregation

	code, got := callJudgeUsage(t, s, "")
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", code, "")
	}
	if got.Calls-base.Calls != 2 {
		t.Errorf("calls delta=%d want 2", got.Calls-base.Calls)
	}
	if got.InputTokens-base.InputTokens != 150 {
		t.Errorf("input delta=%d want 150 (judge 아닌 행이 섞였을 가능성)", got.InputTokens-base.InputTokens)
	}
	if got.OutputTokens-base.OutputTokens != 30 {
		t.Errorf("output delta=%d want 30", got.OutputTokens-base.OutputTokens)
	}
	if got.CacheReadTokens-base.CacheReadTokens != 7 {
		t.Errorf("cache_read delta=%d want 7", got.CacheReadTokens-base.CacheReadTokens)
	}
	if got.CacheWriteTokens-base.CacheWriteTokens != 3 {
		t.Errorf("cache_write delta=%d want 3", got.CacheWriteTokens-base.CacheWriteTokens)
	}

	today := time.Now().UTC().Format("2006-01-02")
	var found bool
	for _, day := range got.Daily {
		if day.Date == today {
			found = true
			if day.Calls < 2 {
				t.Errorf("오늘 버킷 calls=%d want >=2", day.Calls)
			}
			if day.InputTokens < 150 {
				t.Errorf("오늘 버킷 input=%d want >=150", day.InputTokens)
			}
		}
	}
	if !found {
		t.Errorf("일자 계열에 오늘(%s) 버킷이 없음: %+v", today, got.Daily)
	}
}

// TestInterceptJudgeUsageDaysWindow: days only narrows the per-day series; the running total covers the whole period.
func TestInterceptJudgeUsageDaysWindow(t *testing.T) {
	s, d := judgeUsageRig(t)

	insertUsage(t, d, "judge", 11, 1, 0, 0, 3) // outside the window (3 days ago)
	insertUsage(t, d, "judge", 22, 2, 0, 0, 0) // inside the window

	_, recent := callJudgeUsage(t, s, "?days=1")
	cutoff := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	for _, day := range recent.Daily {
		if day.Date < cutoff {
			t.Errorf("days=1 인데 창 밖 버킷(%s)이 반환됨 (cutoff=%s)", day.Date, cutoff)
		}
	}
	if recent.Calls < 2 {
		t.Errorf("누계 calls=%d want >=2 (창과 무관하게 전체 기간을 세야 함)", recent.Calls)
	}

	_, all := callJudgeUsage(t, s, "?days=0") // 0 = corrected to the default of 30 days
	if len(all.Daily) > 31 {
		t.Errorf("days=0 보정 후 버킷 수가 비정상적으로 많음: %d", len(all.Daily))
	}
}

// TestInterceptJudgeUsageWithoutDatabase: even with no DB connected, it responds with 200 + an
// empty series so the settings screen doesn't break (the state where judging itself is off).
func TestInterceptJudgeUsageWithoutDatabase(t *testing.T) {
	s := &Server{m: &Manager{}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/intercept/judge/usage", nil)
	s.interceptJudgeUsage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var out db.JudgeUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("응답 JSON 파싱 실패: %v", err)
	}
	if out.Calls != 0 || len(out.Daily) != 0 {
		t.Errorf("DB 없을 때는 빈 계열이어야 함: %+v", out)
	}
}
