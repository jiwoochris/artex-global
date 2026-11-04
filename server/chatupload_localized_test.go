package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression guard for the chat attachment upload API error responses in chatupload.go: they are
// the exact English source messages (assertEnglishMessage). The body extraction reuses
// decodeErrorField from the task_categories test (same package server).

// TestChatUploadErrorConstantsLocalized asserts that all seven response constants are their exact
// English source text. The three "failed to ...: " constants are prefixes that get err.Error()
// appended, so the expectation keeps the trailing ": ".
func TestChatUploadErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"scope_invalid", errChatUploadScopeInvalid, "The scope value must be one of task, session, or staging"},
		{"bad_id", errChatUploadBadID, "Invalid id"},
		{"task_deleting", errChatUploadTaskDeleting, "The task is being deleted, so attachments cannot be uploaded"},
		{"mkdir", errChatUploadMkdir, "Failed to create directory: "},
		{"parse", errChatUploadParse, "Failed to parse the upload, or it exceeded the size limit: "},
		{"no_file", errChatUploadNoFile, "There is no file to upload (form field file)"},
		{"save_failed", errChatUploadSaveFailed, "Failed to save: "},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestChatUploadHandlerResponsesLocalized checks the chatUpload paths that finish without touching
// the DB, down to the real HTTP response body. The scope/id validations return before touching the
// Manager, and no-file uses scope=session (not a task) so it needs only a temp dir, no engine or DB.
// task-deleting hits the same deletion barrier as the goals test (engine.deleting) and returns 409.
func TestChatUploadHandlerResponsesLocalized(t *testing.T) {
	// multipart body with fields but no "file" part: ParseMultipartForm succeeds with zero files.
	noFileBody := func() (*bytes.Buffer, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("other", "x")
		_ = mw.Close()
		return &buf, mw.FormDataContentType()
	}

	t.Run("scope-invalid", func(t *testing.T) {
		s := &Server{}
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=bogus&id=x", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status code = %d, want = 400 (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeErrorField(t, rec.Body.Bytes())
		assertEnglishMessage(t, "scope-invalid", got,
			"The scope value must be one of task, session, or staging")
	})

	t.Run("bad-id", func(t *testing.T) {
		s := &Server{}
		// scope=session (not a task) returns at the id validation before the Manager is touched.
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=session&id=../evil", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status code = %d, want = 400 (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeErrorField(t, rec.Body.Bytes())
		assertEnglishMessage(t, "bad-id", got, "Invalid id")
	})

	t.Run("no-file", func(t *testing.T) {
		s := &Server{m: &Manager{dir: t.TempDir()}}
		body, ctype := noFileBody()
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=session&id=sess1", body)
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status code = %d, want = 400 (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeErrorField(t, rec.Body.Bytes())
		assertEnglishMessage(t, "no-file", got, "There is no file to upload (form field file)")
	})

	t.Run("task-deleting", func(t *testing.T) {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=task&id=t1", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status code = %d, want = 409 (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeErrorField(t, rec.Body.Bytes())
		assertEnglishMessage(t, "task-deleting", got,
			"The task is being deleted, so attachments cannot be uploaded")
	})
}
