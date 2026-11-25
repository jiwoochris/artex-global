package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Regression guard that keeps the task/intent control API error responses in task_control.go in Korean.
// The Korean check reuses F3a's assertKoreanError (Hangul present, zero Chinese Han characters), and
// response body extraction reuses decodeErrorField from the task_categories tests (same package server).

// TestTaskControlErrorConstantsLocalized asserts that all ten control response constants are Korean.
// The intent control path (applyIntentControl) has to go through t.Store.GetNode (DB) on its first line,
// so it cannot be driven end to end on this DB-less host; for those six, the constants themselves are
// asserted (following the intent_intervention and goals_api precedent). The task control and batch paths
// are verified by the two tests below, through actual function execution and the HTTP response body.
func TestTaskControlErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"task_deleting":          errTaskCtrlDeleting,
		"task_terminal_pause":    errTaskCtrlTerminalPause,
		"task_already_paused":    errTaskCtrlAlreadyPaused,
		"batch_size":             errTaskCtrlBatchSizeFmt,
		"intent_inherited":       errIntentCtrlInheritedReadonly,
		"intent_only_running":    errIntentCtrlOnlyRunningPause,
		"intent_only_paused":     errIntentCtrlOnlyPausedResume,
		"intent_state_conflict":  errIntentCtrlStateConflictFmt,
		"intent_only_deletable":  errIntentCtrlOnlyDeletable,
		"intent_reason_required": errIntentCtrlReasonRequired,
	}
	for label, msg := range cases {
		assertKoreanError(t, label, msg)
	}
}

// TestTaskControlApplyResponsesLocalized actually runs, via applyTaskControl, the pause control paths that
// finish without touching DB or engine state, verifying that the constants actually make it into the
// returned error. All three branches go s.m.Task (map lookup) → deletion barrier → t.lifecycleSnapshot()
// (reading the in-memory Status/Paused fields directly), so they run without a Store or DB as long as the
// tasks map holds one task and an empty Engine. The success path (actually pausing) goes through
// ApplyTaskPause (DB), so it is not driven here — the error branches all return before that.
func TestTaskControlApplyResponsesLocalized(t *testing.T) {
	// Task in a terminal state: the isTerminalStatus(done) branch.
	terminalServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "done"}
		return &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}, tk
	}
	// Running task that is already paused: the lifecycle.Paused branch.
	pausedServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "running", Paused: true}
		return &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}, tk
	}
	// Task with a deletion barrier raised: the IsDeleting branch.
	deletingServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "running"}
		s := &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}
		s.engine.deleting.Store("1", true)
		return s, tk
	}

	cases := []struct {
		name  string
		setup func() (*Server, *Task)
		want  string
	}{
		{"terminal", terminalServer, errTaskCtrlTerminalPause},
		{"already-paused", pausedServer, errTaskCtrlAlreadyPaused},
		{"deleting", deletingServer, errTaskCtrlDeleting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, tk := c.setup()
			_, err := s.applyTaskControl(tk, "pause")
			if err == nil {
				t.Fatalf("오류를 기대했으나 nil 이 반환되었습니다")
			}
			if err.Error() != c.want {
				t.Fatalf("반환 오류 = %q, 기대 = %q", err.Error(), c.want)
			}
			assertKoreanError(t, c.name, err.Error())
		})
	}
}

// TestControlTasksBatchResponsesLocalized checks the batch control endpoint's response body over real
// HTTP. (1) Empty task_ids produce a 400 + size error via writeErr before s.m is accessed, so even
// &Server{} reaches it. (2) Batch-pausing a single task in a terminal state carries the task control error
// verbatim in the 200 response's items[].error, verifying that the applyTaskControl error propagates all
// the way into the batch JSON.
func TestControlTasksBatchResponsesLocalized(t *testing.T) {
	t.Run("size-error", func(t *testing.T) {
		s := &Server{}
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/control",
			strings.NewReader(`{"task_ids":[],"action":"pause"}`))
		rec := httptest.NewRecorder()
		s.controlTasksBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("상태 코드 = %d, 기대 = 400 (본문 %q)", rec.Code, rec.Body.String())
		}
		got := decodeErrorField(t, rec.Body.Bytes())
		want := fmt.Sprintf(errTaskCtrlBatchSizeFmt, maxBatchControlIDs)
		if got != want {
			t.Fatalf("응답 문구 = %q, 기대 = %q", got, want)
		}
		if !strings.Contains(got, "100") {
			t.Fatalf("크기 상한 100 이 문구에 반영되지 않았습니다: %q", got)
		}
		assertKoreanError(t, "size-error", got)
	})

	t.Run("item-error-surfaces", func(t *testing.T) {
		tk := &Task{ID: "1", Status: "done"}
		s := &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/control",
			strings.NewReader(`{"task_ids":["1"],"action":"pause"}`))
		rec := httptest.NewRecorder()
		s.controlTasksBatch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("상태 코드 = %d, 기대 = 200 (본문 %q)", rec.Code, rec.Body.String())
		}
		var out struct {
			Items []struct {
				ID    string `json:"id"`
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("응답 JSON 파싱 실패: %v (본문 %q)", err, rec.Body.String())
		}
		if len(out.Items) != 1 {
			t.Fatalf("items 개수 = %d, 기대 = 1", len(out.Items))
		}
		if out.Items[0].OK || out.Items[0].Error != errTaskCtrlTerminalPause {
			t.Fatalf("items[0] = %+v, error 기대 = %q", out.Items[0], errTaskCtrlTerminalPause)
		}
		assertKoreanError(t, "item-error", out.Items[0].Error)
	})
}

// TestIntentStateConflictWrapsSentinel verifies that the intent state conflict message is Korean while still
// wrapping db.ErrIntentStateConflict verbatim with %w, so errors.Is identification does not break.
// The enum value paused must be preserved verbatim.
func TestIntentStateConflictWrapsSentinel(t *testing.T) {
	err := fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict)
	if !errors.Is(err, db.ErrIntentStateConflict) {
		t.Fatalf("errors.Is(.., ErrIntentStateConflict) = false, %%w 래핑이 깨졌습니다: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Fatalf("enum paused 가 문구에 보존되지 않았습니다: %q", err.Error())
	}
	assertKoreanError(t, "state-conflict", err.Error())
}
