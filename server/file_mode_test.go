package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertPrivateMode pins a perm on a path created by the server: directories must
// be 0700 and files 0600, so nothing captured or uploaded is world-readable on a
// shared host (L-5).
func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

// TestWorkspaceFileModes pins L-5 for the work-dir file manager: created
// directories are 0700 and written files are 0600 (they were 0755/0644 before).
func TestWorkspaceFileModes(t *testing.T) {
	dir := t.TempDir()
	s := &Server{m: &Manager{dir: dir}}

	mkdir := httptest.NewRequest(http.MethodPost, "/api/workspace/mkdir", strings.NewReader(`{"path":"notes"}`))
	mkdirRec := httptest.NewRecorder()
	s.wsMkdir(mkdirRec, mkdir)
	if mkdirRec.Code != http.StatusOK {
		t.Fatalf("mkdir status = %d (%s)", mkdirRec.Code, mkdirRec.Body.String())
	}
	assertPrivateMode(t, filepath.Join(dir, "notes"), 0o700)

	write := httptest.NewRequest(http.MethodPost, "/api/workspace/write", strings.NewReader(`{"path":"notes/a.txt","content":"hi"}`))
	writeRec := httptest.NewRecorder()
	s.wsWrite(writeRec, write)
	if writeRec.Code != http.StatusOK {
		t.Fatalf("write status = %d (%s)", writeRec.Code, writeRec.Body.String())
	}
	assertPrivateMode(t, filepath.Join(dir, "notes", "a.txt"), 0o600)
}

// TestChatUploadFileModes pins L-5 for chat attachments: the uploads directory is
// 0700 and every stored file is 0600.
func TestChatUploadFileModes(t *testing.T) {
	dir := t.TempDir()
	s := &Server{m: &Manager{dir: dir}}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "capture.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte("evidence")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=session&id=sess1", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.chatUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d (%s)", rec.Code, rec.Body.String())
	}

	uploads := filepath.Join(dir, "sessions", "sess1", "uploads")
	assertPrivateMode(t, uploads, 0o700)
	assertPrivateMode(t, filepath.Join(uploads, "capture.txt"), 0o600)
}
