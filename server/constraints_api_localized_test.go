package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard test that keeps the error responses of the constraint (约束) management
// API in constraints_api.go in Korean. It reuses F3a's assertKoreanError (contains Hangul ·
// zero Chinese Han) for the Korean check, and decodeErrorField from the task_categories
// tests to extract the response body (same package server).

// TestConstraintErrorConstantsLocalized asserts that all 5 response constants are Korean.
func TestConstraintErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"task_deleting_add":    errConstraintTaskDeletingAdd,
		"task_deleting_edit":   errConstraintTaskDeletingEdit,
		"task_deleting_delete": errConstraintTaskDeletingDelete,
		"text_empty":           errConstraintTextEmpty,
		"kind_invalid":         errConstraintKindInvalid,
	}
	for label, msg := range cases {
		assertKoreanError(t, label, msg)
	}
}

// TestConstraintHandlersResponsesLocalized checks the handler paths that finish without
// touching the DB down to the actual HTTP response body, confirming that the constants are
// really wired into the responses. All three handlers go s.m.Task (map lookup) →
// s.engine.beginTaskOperation (sync.Map-based deletion barrier) → request body · field
// validation, so with one task in the tasks map and an empty Engine they run with no Store · DB.
func TestConstraintHandlersResponsesLocalized(t *testing.T) {
	// Server with the deleting barrier raised: beginTaskOperation returns false and yields a 409.
	deletingServer := func() *Server {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		return s
	}
	// Server without the barrier: beginTaskOperation returns true and proceeds to input validation.
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
		name    string
		handler func(*Server) http.HandlerFunc
		server  func() *Server
		body    string
		cid     string
		code    int
		want    string
	}{
		{
			name:    "add/task-deleting",
			handler: func(s *Server) http.HandlerFunc { return s.addConstraint },
			server:  deletingServer,
			body:    `{"text":"현재 포트만 테스트","kind":"allow"}`,
			code:    http.StatusConflict,
			want:    errConstraintTaskDeletingAdd,
		},
		{
			name:    "add/text-empty",
			handler: func(s *Server) http.HandlerFunc { return s.addConstraint },
			server:  liveServer,
			body:    `{"text":"   ","kind":"allow"}`,
			code:    http.StatusBadRequest,
			want:    errConstraintTextEmpty,
		},
		{
			name:    "add/kind-invalid",
			handler: func(s *Server) http.HandlerFunc { return s.addConstraint },
			server:  liveServer,
			body:    `{"text":"다른 포트 스캔 금지","kind":"maybe"}`,
			code:    http.StatusBadRequest,
			want:    errConstraintKindInvalid,
		},
		{
			name:    "edit/task-deleting",
			handler: func(s *Server) http.HandlerFunc { return s.editConstraint },
			server:  deletingServer,
			body:    `{"text":"수정된 제약","kind":"deny"}`,
			cid:     "5",
			code:    http.StatusConflict,
			want:    errConstraintTaskDeletingEdit,
		},
		{
			name:    "edit/text-empty",
			handler: func(s *Server) http.HandlerFunc { return s.editConstraint },
			server:  liveServer,
			body:    `{"text":"  ","kind":"deny"}`,
			cid:     "5",
			code:    http.StatusBadRequest,
			want:    errConstraintTextEmpty,
		},
		{
			name:    "edit/kind-invalid",
			handler: func(s *Server) http.HandlerFunc { return s.editConstraint },
			server:  liveServer,
			body:    `{"text":"수정된 제약","kind":"nope"}`,
			cid:     "5",
			code:    http.StatusBadRequest,
			want:    errConstraintKindInvalid,
		},
		{
			name:    "delete/task-deleting",
			handler: func(s *Server) http.HandlerFunc { return s.deleteConstraint },
			server:  deletingServer,
			body:    ``,
			cid:     "5",
			code:    http.StatusConflict,
			want:    errConstraintTaskDeletingDelete,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.server()
			rec := httptest.NewRecorder()
			c.handler(s)(rec, newReq(c.body, c.cid))
			if rec.Code != c.code {
				t.Fatalf("상태 코드 = %d, 기대 = %d (본문 %q)", rec.Code, c.code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.want {
				t.Fatalf("응답 문구 = %q, 기대 = %q", got, c.want)
			}
			assertKoreanError(t, c.name, got)
		})
	}
}
