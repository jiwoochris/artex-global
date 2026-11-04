package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard that keeps the task metadata update API error responses in task_metadata.go
// as their exact English source text (assertEnglishMessage).

// TestTaskMetadataErrorConstantsLocalized asserts that all four response constants are their
// exact English source text. Reverting any one of them to another language fails this test.
func TestTaskMetadataErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"errTaskMetaRequestTooLarge", errTaskMetaRequestTooLarge, "The request body is too large"},
		{"errTaskMetaNoFields", errTaskMetaNoFields, "You must provide either name or pinned"},
		{"errTaskMetaNameEmpty", errTaskMetaNameEmpty, "The task name cannot be empty"},
		{"errTaskMetaNameTooLongFmt", fmt.Sprintf(errTaskMetaNameTooLongFmt, maxTaskNameRunes),
			fmt.Sprintf("A task name can be at most %d characters", maxTaskNameRunes)},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestUpdateTaskMetadataResponsesLocalized checks the four input-validation paths all the way to
// the real HTTP response body. updateTaskMetadata returns on these four paths (body too large,
// missing field, blank name, name too long) after reading only s.m.Task (map lookup) and the
// request body, before s.m.UpdateTaskMetadata (DB), so one task in the tasks map runs to completion without a DB. It also checks the wiring that actually places the constants in the response.
func TestUpdateTaskMetadataResponsesLocalized(t *testing.T) {
	s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}}

	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/tasks/t1/metadata", strings.NewReader(body))
		req.SetPathValue("id", "t1")
		rec := httptest.NewRecorder()
		s.updateTaskMetadata(rec, req)
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
		// Valid JSON body that exceeds the 16KB limit. MaxBytesReader reports the overrun mid-read.
		{"request_too_large", `{"name":"` + strings.Repeat("a", maxTaskMetadataRequestSize+1024) + `"}`, http.StatusRequestEntityTooLarge, "The request body is too large"},
		// Omitting both name and pinned leaves no field to update.
		{"no_fields", `{}`, http.StatusBadRequest, "You must provide either name or pinned"},
		{"name_empty", `{"name":"  "}`, http.StatusBadRequest, "The task name cannot be empty"},
		{"name_too_long", `{"name":"` + strings.Repeat("a", maxTaskNameRunes+1) + `"}`, http.StatusBadRequest,
			fmt.Sprintf("A task name can be at most %d characters", maxTaskNameRunes)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := call(c.body)
			if rec.Code != c.code {
				t.Fatalf("status code = %d, want = %d (body %q)", rec.Code, c.code, rec.Body.String())
			}
			if got := errBody(t, rec); got != c.wantEnglish {
				t.Fatalf("response message = %q, want = %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, c.name, errBody(t, rec), c.wantEnglish)
		})
	}
}
