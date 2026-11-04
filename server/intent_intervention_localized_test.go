package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard that keeps the Worker intervention API error responses of
// intent_intervention.go as their exact English source text (assertEnglishMessage).

// TestIntentInterventionErrorConstantsLocalized asserts that all 11 response constants are their exact English source text.
// The lifecycle and intent-state paths need s.engine and Store configuration and cannot run to
// completion on this host without a DB, so the constants themselves are asserted (following
// conversations.go). The four input-validation paths are checked down to the response body by the HTTP test below.
func TestIntentInterventionErrorConstantsLocalized(t *testing.T) {
	cases := []struct {
		name        string
		msg         string
		wantEnglish string
	}{
		{"request_too_large", errIntentRequestTooLarge, "The request body is too large"},
		{"message_empty", errIntentMessageEmpty, "The message cannot be empty"},
		{"message_too_long", errIntentMessageTooLong, "A message cannot exceed 4000 characters"},
		{"bad_request_id", errIntentBadRequestID, "request_id may contain only 1–128 characters: letters, digits, -, _, ., and :"},
		{"task_deleting", errIntentTaskDeleting, "The task is being deleted, so no message can be sent to the Worker"},
		{"task_paused", errIntentTaskPaused, "The task is paused. Resume the task first, then send a message to the Worker"},
		{"task_queued", errIntentTaskQueued, "Cannot send a message to the Worker for a pending task"},
		{"task_terminal", errIntentTaskTerminal, "Messages cannot be sent to the Worker for a terminated task"},
		{"task_settling", errIntentTaskSettling, "Cannot send a message to the Worker while the task is wrapping up"},
		{"inherited_readonly", errIntentInheritedReadonly, "Inherited intents are read-only, so no message can be sent to the Worker"},
		{"not_paused", errIntentNotPaused, "Messages can only be sent to a paused Worker. Pause it first"},
	}
	for _, c := range cases {
		assertEnglishMessage(t, c.name, c.msg, c.wantEnglish)
	}
}

// TestSendWorkerMessageInputValidationLocalized checks the four input-validation paths down to
// the real HTTP response body. These four sendWorkerMessage paths only look at s.m.Task (map
// lookup) and the request body, never s.engine, Store or the DB, so a single task in the tasks map is enough to run without a DB.
// This also proves that the constant really lands in the response.
func TestSendWorkerMessageInputValidationLocalized(t *testing.T) {
	s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}}

	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/1/message", strings.NewReader(body))
		req.SetPathValue("id", "t1")
		req.SetPathValue("iid", "1")
		rec := httptest.NewRecorder()
		s.sendWorkerMessage(rec, req)
		return rec
	}
	errBody := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var resp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse the response JSON: %v (body %q)", err, rec.Body.String())
		}
		return resp.Error
	}

	cases := []struct {
		name        string
		body        string
		code        int
		wantEnglish string
	}{
		// Valid JSON body exceeding the 64KB limit. MaxBytesReader reports the overflow while reading.
		{"request_too_large", `{"message":"` + strings.Repeat("a", maxWorkerMessageBytes+1024) + `"}`, http.StatusRequestEntityTooLarge, "The request body is too large"},
		{"message_empty", `{"message":"  "}`, http.StatusBadRequest, "The message cannot be empty"},
		// 가 is a multi-byte rune, so 4001 of them prove the limit counts characters, not bytes.
		{"message_too_long", `{"message":"` + strings.Repeat("가", 4001) + `"}`, http.StatusBadRequest, "A message cannot exceed 4000 characters"},
		{"bad_request_id", `{"message":"hello","request_id":"contains space"}`, http.StatusBadRequest, "request_id may contain only 1–128 characters: letters, digits, -, _, ., and :"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := call(c.body)
			if rec.Code != c.code {
				t.Fatalf("status = %d, want = %d (body %q)", rec.Code, c.code, rec.Body.String())
			}
			if got := errBody(t, rec); got != c.wantEnglish {
				t.Fatalf("response message = %q, want = %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, c.name, errBody(t, rec), c.wantEnglish)
		})
	}
}
