package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard for the constraint management API error responses in constraints_api.go: they are
// the exact English source messages (assertEnglishMessage). The body extraction reuses
// decodeErrorField from the task_categories test (same package server).

// TestConstraintErrorConstantsLocalized asserts that all five response constants are their exact
// English source text.
func TestConstraintErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"task_deleting_add", errConstraintTaskDeletingAdd, "A constraint cannot be added because the task is being deleted"},
		{"task_deleting_edit", errConstraintTaskDeletingEdit, "The task is being deleted, so constraints cannot be modified"},
		{"task_deleting_delete", errConstraintTaskDeletingDelete, "Cannot delete the constraint while the task is being deleted"},
		{"text_empty", errConstraintTextEmpty, "The constraint content cannot be empty"},
		{"kind_invalid", errConstraintKindInvalid, "The kind value must be either allow or deny"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestConstraintHandlersResponsesLocalized checks the handler paths that finish without touching the DB,
// down to the real HTTP response body, confirming the wiring that actually puts the constants into responses. All three handlers
// run s.m.Task(map lookup) → s.engine.beginTaskOperation(sync.Map-based deletion barrier) → request
// body/field validation in that order, so one task in the tasks map plus an empty Engine is enough to run with no Store and no DB.
func TestConstraintHandlersResponsesLocalized(t *testing.T) {
	// Server with the deleting barrier raised: beginTaskOperation returns false and yields 409.
	deletingServer := func() *Server {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		return s
	}
	// Server without the barrier: beginTaskOperation returns true, so input validation runs too.
	liveServer := func() *Server {
		return &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
	}

	newReq := func(body, cid string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/constraints", strings.NewReader(body))
		req.SetPathValue("id", "t1")
		if cid != "" {
			req.SetPathValue("cid", cid)
		}
		return req
	}

	cases := []struct {
		name        string
		handler     func(*Server) http.HandlerFunc
		server      func() *Server
		body        string
		cid         string
		code        int
		wantEnglish string
	}{
		{
			name:        "add/task-deleting",
			handler:     func(s *Server) http.HandlerFunc { return s.addConstraint },
			server:      deletingServer,
			body:        `{"text":"test the current port only","kind":"allow"}`,
			code:        http.StatusConflict,
			wantEnglish: "A constraint cannot be added because the task is being deleted",
		},
		{
			name:        "add/text-empty",
			handler:     func(s *Server) http.HandlerFunc { return s.addConstraint },
			server:      liveServer,
			body:        `{"text":"   ","kind":"allow"}`,
			code:        http.StatusBadRequest,
			wantEnglish: "The constraint content cannot be empty",
		},
		{
			name:        "add/kind-invalid",
			handler:     func(s *Server) http.HandlerFunc { return s.addConstraint },
			server:      liveServer,
			body:        `{"text":"do not scan other ports","kind":"maybe"}`,
			code:        http.StatusBadRequest,
			wantEnglish: "The kind value must be either allow or deny",
		},
		{
			name:        "edit/task-deleting",
			handler:     func(s *Server) http.HandlerFunc { return s.editConstraint },
			server:      deletingServer,
			body:        `{"text":"edited constraint","kind":"deny"}`,
			cid:         "5",
			code:        http.StatusConflict,
			wantEnglish: "The task is being deleted, so constraints cannot be modified",
		},
		{
			name:        "edit/text-empty",
			handler:     func(s *Server) http.HandlerFunc { return s.editConstraint },
			server:      liveServer,
			body:        `{"text":"  ","kind":"deny"}`,
			cid:         "5",
			code:        http.StatusBadRequest,
			wantEnglish: "The constraint content cannot be empty",
		},
		{
			name:        "edit/kind-invalid",
			handler:     func(s *Server) http.HandlerFunc { return s.editConstraint },
			server:      liveServer,
			body:        `{"text":"edited constraint","kind":"nope"}`,
			cid:         "5",
			code:        http.StatusBadRequest,
			wantEnglish: "The kind value must be either allow or deny",
		},
		{
			name:        "delete/task-deleting",
			handler:     func(s *Server) http.HandlerFunc { return s.deleteConstraint },
			server:      deletingServer,
			body:        ``,
			cid:         "5",
			code:        http.StatusConflict,
			wantEnglish: "Cannot delete the constraint while the task is being deleted",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.server()
			rec := httptest.NewRecorder()
			c.handler(s)(rec, newReq(c.body, c.cid))
			if rec.Code != c.code {
				t.Fatalf("status code = %d, want = %d (body %q)", rec.Code, c.code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.wantEnglish {
				t.Fatalf("response message = %q, want = %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, c.name, got, c.wantEnglish)
		})
	}
}
