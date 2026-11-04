package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard that keeps the goal-management API error responses of goals_api.go as their exact
// English source text (assertEnglishMessage), and the response body extraction reuses decodeErrorField
// from the task_categories tests (same package server).

// TestGoalErrorConstantsLocalized asserts that all 7 response constants are their English source text. Goal lookup and
// save (goal not found · read failure) are only reachable through Store and the DB, so they cannot
// be driven to completion on this host without a DB and the constants themselves are asserted
// (following intent_intervention). The input-validation and delete-barrier paths are checked down to the response body by the HTTP test below.
func TestGoalErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"task_deleting_add", errGoalTaskDeletingAdd, "The task is being deleted, so no goal can be added"},
		{"task_deleting_edit", errGoalTaskDeletingEdit, "Cannot modify the goal while the task is being deleted"},
		{"task_deleting_delete", errGoalTaskDeletingDelete, "The goal cannot be deleted because the task is being deleted"},
		{"text_empty", errGoalTextEmpty, "The goal content cannot be empty"},
		{"not_found", errGoalNotFound, "Goal not found"},
		{"read_after_add", errGoalReadAfterAdd, "Failed to read back the goal after saving it"},
		{"read_after_edit", errGoalReadAfterEdit, "Failed to read the goal after updating it"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestGoalHandlersResponsesLocalized exercises the handler paths that finish without touching
// the DB down to the real HTTP response body, confirming that the constants really land in the
// response. All three handlers run s.m.Task (map lookup) → s.engine.beginTaskOperation
// (sync.Map-based delete barrier) → request body validation, so one task in the tasks map and an empty Engine are enough to run without Store or the DB.
func TestGoalHandlersResponsesLocalized(t *testing.T) {
	// A server with the deleting barrier set: beginTaskOperation returns false, yielding 409.
	deletingServer := func() *Server {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		return s
	}
	// A server without the barrier: beginTaskOperation returns true, so the handler proceeds to input validation.
	liveServer := func() *Server {
		return &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
	}

	newReq := func(body, gid string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/goals", strings.NewReader(body))
		req.SetPathValue("id", "t1")
		if gid != "" {
			req.SetPathValue("gid", gid)
		}
		return req
	}

	cases := []struct {
		name        string
		handler     func(*Server) http.HandlerFunc
		server      func() *Server
		body        string
		gid         string
		code        int
		wantEnglish string
	}{
		{
			name:        "add/task-deleting",
			handler:     func(s *Server) http.HandlerFunc { return s.addGoal },
			server:      deletingServer,
			body:        `{"text":"prove SQL injection"}`,
			code:        http.StatusConflict,
			wantEnglish: "The task is being deleted, so no goal can be added",
		},
		{
			name:        "add/text-empty",
			handler:     func(s *Server) http.HandlerFunc { return s.addGoal },
			server:      liveServer,
			body:        `{"text":"   "}`,
			code:        http.StatusBadRequest,
			wantEnglish: "The goal content cannot be empty",
		},
		{
			name:        "edit/task-deleting",
			handler:     func(s *Server) http.HandlerFunc { return s.editGoal },
			server:      deletingServer,
			body:        `{"text":"edited goal"}`,
			gid:         "5",
			code:        http.StatusConflict,
			wantEnglish: "Cannot modify the goal while the task is being deleted",
		},
		{
			name:        "edit/text-empty",
			handler:     func(s *Server) http.HandlerFunc { return s.editGoal },
			server:      liveServer,
			body:        `{"text":"  "}`,
			gid:         "5",
			code:        http.StatusBadRequest,
			wantEnglish: "The goal content cannot be empty",
		},
		{
			name:        "delete/task-deleting",
			handler:     func(s *Server) http.HandlerFunc { return s.deleteGoal },
			server:      deletingServer,
			body:        ``,
			gid:         "5",
			code:        http.StatusConflict,
			wantEnglish: "The goal cannot be deleted because the task is being deleted",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.server()
			rec := httptest.NewRecorder()
			c.handler(s)(rec, newReq(c.body, c.gid))
			if rec.Code != c.code {
				t.Fatalf("status = %d, want = %d (body %q)", rec.Code, c.code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.wantEnglish {
				t.Fatalf("response message = %q, want = %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, c.name, got, c.wantEnglish)
		})
	}
}
