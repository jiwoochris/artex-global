package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/dbtest"
)

// TestGetTaskReportsProgressAggregates verifies that the task detail endpoint
// carries the same progress aggregates the task list does.
//
// The task detail view renders goal counts, the running-intent count, the
// finding severity buckets and the token totals. Before this regression the
// detail endpoint only filled the static task fields, so every one of those
// progress fields read zero on the task page while the task list showed the
// real numbers — the same task appeared to have no progress and no findings
// depending on which screen it was opened from.
func TestGetTaskReportsProgressAggregates(t *testing.T) {
	dbtest.DSN(t) // skip unless ARTEX_TEST_DSN points at a throwaway DB
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("no database available: %v", err)
	}
	t.Cleanup(func() { _ = m.PG().Close() })

	task, err := m.CreateTaskWithOptions("detail-progress", "goal text", pgdb.TaskCreateOptions{})
	if err != nil {
		t.Fatalf("CreateTaskWithOptions: %v", err)
	}
	explorationID := task.ExpID

	store := m.PG().Exploration(explorationID)
	if _, err := store.AddGoal(map[string]any{"text": "goal one"}, "planner"); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	if _, err := store.AddGoal(map[string]any{"text": "goal two"}, "planner"); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	if _, err := m.PG().AddFinding(mustTaskID(t, task.ID), 0, "Unauthenticated Information Disclosure",
		"detail progress finding", "medium", "summary", "evidence", "worker", nil); err != nil {
		t.Fatalf("AddFinding: %v", err)
	}

	s := &Server{m: m, engine: NewEngine(m)}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil)
	request.SetPathValue("id", task.ID)
	s.getTask(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP %d body=%s", response.Code, response.Body)
	}
	var dto map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got := dto["goals_total"]; got != float64(2) {
		t.Errorf("goals_total = %v, want 2 (the detail endpoint dropped the goal aggregates)", got)
	}
	severities, _ := dto["findings"].(map[string]any)
	if got := severities["medium"]; got != float64(1) {
		t.Errorf("findings.medium = %v, want 1 (the detail endpoint dropped the finding buckets)", got)
	}
}
