package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// 이 테스트는 상류 ARTEX(0.3.15 계열)에 있던
// GET /api/intercept/judge/usage 를 이 포크로 되가져오면서 함께 추가한 것이다.
// 폴백 판정(judge) 이 소비한 토큰 누계와 일자별 계열이 실제 DB 에서 정확히
// 집계되는지, 그리고 DB 가 없을 때도 500 이 아니라 빈 계열로 응답하는지 본다.
//
// DB 설정이 없으면 다른 통합 테스트처럼 건너뛴다(server/testmain_test.go 참고).
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
	// llm_usage 는 schema.sql 이 아니라 Go 쪽 EnsureLLMUsageTable() 이 만든다.
	// db 패키지의 기존 테스트(finding_traffic_archive_test.go 등)와 같은 방식으로 맞춘다.
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

// TestInterceptJudgeUsageAggregates: 누계는 worker='judge' 행만 세고, 일자 계열에
// 오늘 버킷이 생기며, judge 가 아닌 행은 어떤 값에도 영향을 주지 않아야 한다.
func TestInterceptJudgeUsageAggregates(t *testing.T) {
	s, d := judgeUsageRig(t)

	base, err := d.JudgeUsageStats(30)
	if err != nil {
		t.Fatalf("baseline JudgeUsageStats 실패: %v", err)
	}

	insertUsage(t, d, "judge", 100, 20, 7, 3, 0)
	insertUsage(t, d, "judge", 50, 10, 0, 0, 0)
	insertUsage(t, d, "worker", 999, 999, 999, 999, 0) // 집계에서 제외되어야 함

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

// TestInterceptJudgeUsageDaysWindow: days 는 일자 계열만 좁히고 누계는 전체 기간이다.
func TestInterceptJudgeUsageDaysWindow(t *testing.T) {
	s, d := judgeUsageRig(t)

	insertUsage(t, d, "judge", 11, 1, 0, 0, 3) // 창 밖(3일 전)
	insertUsage(t, d, "judge", 22, 2, 0, 0, 0) // 창 안

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

	_, all := callJudgeUsage(t, s, "?days=0") // 0 = 기본 30일로 보정
	if len(all.Daily) > 31 {
		t.Errorf("days=0 보정 후 버킷 수가 비정상적으로 많음: %d", len(all.Daily))
	}
}

// TestInterceptJudgeUsageWithoutDatabase: DB 미연결이어도 설정 화면이 깨지지 않도록
// 200 + 빈 계열로 응답한다(판정 자체가 꺼져 있는 상태).
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
