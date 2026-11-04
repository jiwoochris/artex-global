package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yukiteruamano/artex/db"
)

// Regression guard on the task and intent control API error responses in
// task_control.go: they are their exact English source text (assertEnglishMessage) and
// body extraction reuses decodeErrorField from the task_categories tests (same package
// server).

// TestTaskControlErrorConstantsLocalized asserts that all ten control response
// constants are the English source text. The intent control path (applyIntentControl) needs
// t.Store.GetNode(DB) on its first line, so it cannot be driven to the end on a host
// without a DB; those six constants are asserted directly (following
// intent_intervention and goals_api). The two tests below drive the task control and
// batch paths for real, down to the HTTP response body.
func TestTaskControlErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"task_deleting", errTaskCtrlDeleting, "Cannot control the task while it is being deleted"},
		{"task_terminal_pause", errTaskCtrlTerminalPause, "A terminated task cannot be paused"},
		{"task_already_paused", errTaskCtrlAlreadyPaused, "The task is already paused"},
		{"batch_size", errTaskCtrlBatchSizeFmt, "The number of task_ids must be between 1 and %d"},
		{"intent_inherited", errIntentCtrlInheritedReadonly, "An inherited intent is read-only and cannot be controlled"},
		{"intent_only_running", errIntentCtrlOnlyRunningPause, "Only a running intent can be paused"},
		{"intent_only_paused", errIntentCtrlOnlyPausedResume, "Only paused intents can be resumed"},
		{"intent_state_conflict", errIntentCtrlStateConflictFmt, "%w: the intent is no longer in the paused state"},
		{"intent_only_deletable", errIntentCtrlOnlyDeletable, "Only intents in the pending, running, or paused state can be deleted"},
		{"intent_reason_required", errIntentCtrlReasonRequired, "Enter a reason for deletion"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestTaskControlApplyResponsesLocalized actually runs applyTaskControl on the pause
// paths that finish without touching DB or engine state, proving the constants really
// ride on the returned error. All three branches go s.m.Task (map lookup) → delete
// barrier → t.lifecycleSnapshot() (reads the in-memory Status/Paused fields), so one
// task in the tasks map plus an empty Engine is enough, with no Store and no DB. The
// success path (an actual pause) goes through ApplyTaskPause(DB) and is not driven here
// — every error branch returns before that.
func TestTaskControlApplyResponsesLocalized(t *testing.T) {
	// Terminal task: the isTerminalStatus(done) branch.
	terminalServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "done"}
		return &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}, tk
	}
	// Already paused running task: the lifecycle.Paused branch.
	pausedServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "running", Paused: true}
		return &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}, tk
	}
	// Task with the delete barrier raised: the IsDeleting branch.
	deletingServer := func() (*Server, *Task) {
		tk := &Task{ID: "1", Status: "running"}
		s := &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}
		s.engine.deleting.Store("1", true)
		return s, tk
	}

	cases := []struct {
		name        string
		setup       func() (*Server, *Task)
		wantEnglish string
	}{
		{"terminal", terminalServer, "A terminated task cannot be paused"},
		{"already-paused", pausedServer, "The task is already paused"},
		{"deleting", deletingServer, "Cannot control the task while it is being deleted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, tk := c.setup()
			_, err := s.applyTaskControl(tk, "pause")
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if err.Error() != c.wantEnglish {
				t.Fatalf("returned error = %q, want = %q", err.Error(), c.wantEnglish)
			}
			assertEnglishMessage(t, c.name, err.Error(), c.wantEnglish)
		})
	}
}

// TestControlTasksBatchResponsesLocalized inspects the batch control endpoint's
// response body over real HTTP. (1) Empty task_ids returns 400 with the size error via
// writeErr before s.m is touched, so a bare &Server{} reaches it. (2) Batch-pausing one
// terminal task returns 200 with the task control error in items[].error, proving the
// applyTaskControl error propagates all the way into the batch JSON.
func TestControlTasksBatchResponsesLocalized(t *testing.T) {
	t.Run("size-error", func(t *testing.T) {
		s := &Server{}
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/control",
			strings.NewReader(`{"task_ids":[],"action":"pause"}`))
		rec := httptest.NewRecorder()
		s.controlTasksBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status code = %d, want = 400 (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeErrorField(t, rec.Body.Bytes())
		wantEnglish := "The number of task_ids must be between 1 and 100"
		if got != wantEnglish {
			t.Fatalf("response message = %q, want = %q", got, wantEnglish)
		}
		if !strings.Contains(got, "100") {
			t.Fatalf("the size limit 100 is not reflected in the message: %q", got)
		}
		assertEnglishMessage(t, "size-error", got, wantEnglish)
	})

	t.Run("item-error-surfaces", func(t *testing.T) {
		tk := &Task{ID: "1", Status: "done"}
		s := &Server{m: &Manager{tasks: map[string]*Task{"1": tk}}, engine: &Engine{}}
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/control",
			strings.NewReader(`{"task_ids":["1"],"action":"pause"}`))
		rec := httptest.NewRecorder()
		s.controlTasksBatch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want = 200 (body %q)", rec.Code, rec.Body.String())
		}
		var out struct {
			Items []struct {
				ID    string `json:"id"`
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("failed to parse the response JSON: %v (body %q)", err, rec.Body.String())
		}
		if len(out.Items) != 1 {
			t.Fatalf("items count = %d, want = 1", len(out.Items))
		}
		wantEnglish := "A terminated task cannot be paused"
		if out.Items[0].OK || out.Items[0].Error != wantEnglish {
			t.Fatalf("items[0] = %+v, want error = %q", out.Items[0], wantEnglish)
		}
		assertEnglishMessage(t, "item-error", out.Items[0].Error, wantEnglish)
	})
}

// TestIntentStateConflictWrapsSentinel checks that the intent state conflict message is
// the English source text while still wrapping db.ErrIntentStateConflict with %w, so
// errors.Is identification keeps working. The enum value paused must be preserved
// verbatim.
func TestIntentStateConflictWrapsSentinel(t *testing.T) {
	err := fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict)
	if !errors.Is(err, db.ErrIntentStateConflict) {
		t.Fatalf("errors.Is(.., ErrIntentStateConflict) = false, %%w wrapping is broken: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Fatalf("enum value paused was not preserved in the message: %q", err.Error())
	}
	assertEnglishMessage(t, "state-conflict", err.Error(),
		"intent state changed concurrently: the intent is no longer in the paused state")
}
