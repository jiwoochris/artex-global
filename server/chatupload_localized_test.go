package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression guard test that keeps the error responses of the chat attachment upload API in
// chatupload.go in Korean. It reuses F3a's assertKoreanError (contains Hangul · zero Chinese
// Han) for the Korean check, and decodeErrorField from the task_categories tests to extract
// the response body (same package server).

// TestChatUploadErrorConstantsLocalized asserts that all 7 response constants are Korean.
// The three ...못했습니다: ("failed to ...") messages are prefix constants followed by
// err.Error(), so they end in ": ", but they pass the check as long as they contain Hangul
// and no Chinese Han characters.
func TestChatUploadErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"scope_invalid": errChatUploadScopeInvalid,
		"bad_id":        errChatUploadBadID,
		"task_deleting": errChatUploadTaskDeleting,
		"mkdir":         errChatUploadMkdir,
		"parse":         errChatUploadParse,
		"no_file":       errChatUploadNoFile,
		"save_failed":   errChatUploadSaveFailed,
	}
	for label, msg := range cases {
		assertKoreanError(t, label, msg)
	}
}

// TestChatUploadHandlerResponsesLocalized checks, down to the actual HTTP response body, the
// chatUpload paths that finish without touching the DB. The scope/id validation returns
// before touching the Manager, and no-file uses scope=session (not a task), so it finishes
// with only a temp directory and no engine · DB. task-deleting returns 409 via the same
// deletion barrier (engine.deleting) as the goals tests.
func TestChatUploadHandlerResponsesLocalized(t *testing.T) {
	// A multipart body with fields but no "file" — ParseMultipartForm passes but there are 0 files.
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
			t.Fatalf("상태 코드 = %d, 기대 = 400 (본문 %q)", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadScopeInvalid {
			t.Fatalf("응답 문구 = %q, 기대 = %q", got, errChatUploadScopeInvalid)
		}
		assertKoreanError(t, "scope-invalid", errChatUploadScopeInvalid)
	})

	t.Run("bad-id", func(t *testing.T) {
		s := &Server{}
		// scope=session (not a task), so it returns at id validation before touching the Manager.
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=session&id=../evil", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("상태 코드 = %d, 기대 = 400 (본문 %q)", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadBadID {
			t.Fatalf("응답 문구 = %q, 기대 = %q", got, errChatUploadBadID)
		}
		assertKoreanError(t, "bad-id", errChatUploadBadID)
	})

	t.Run("no-file", func(t *testing.T) {
		s := &Server{m: &Manager{dir: t.TempDir()}}
		body, ctype := noFileBody()
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=session&id=sess1", body)
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("상태 코드 = %d, 기대 = 400 (본문 %q)", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadNoFile {
			t.Fatalf("응답 문구 = %q, 기대 = %q", got, errChatUploadNoFile)
		}
		assertKoreanError(t, "no-file", errChatUploadNoFile)
	})

	t.Run("task-deleting", func(t *testing.T) {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=task&id=t1", nil)
		rec := httptest.NewRecorder()
		s.chatUpload(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("상태 코드 = %d, 기대 = 409 (본문 %q)", rec.Code, rec.Body.String())
		}
		if got := decodeErrorField(t, rec.Body.Bytes()); got != errChatUploadTaskDeleting {
			t.Fatalf("응답 문구 = %q, 기대 = %q", got, errChatUploadTaskDeleting)
		}
		assertKoreanError(t, "task-deleting", errChatUploadTaskDeleting)
	})
}
