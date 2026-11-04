package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestWorkspaceErrorConstantsLocalized guards F3b (workspace.go): every user-facing
// error string the workspace file manager hands back is its exact English source text.
// Reverting any literal to another language fails this test. The constants are
// DB-independent, so this always runs (no postgres needed).
func TestWorkspaceErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"illegalPath", errWsIllegalPath, "Invalid path"},
		{"pathNotFound", errWsPathNotFound, "The path does not exist"},
		{"notDir", errWsNotDir, "Not a directory"},
		{"fileNotFound", errWsFileNotFound, "The file does not exist"},
		{"isDir", errWsIsDir, "It is a directory, so it cannot be read as a file"},
		{"targetIsDir", errWsTargetIsDir, "The target path is a directory"},
		{"cannotDeleteRoot", errWsCannotDeleteRoot, "The workspace root directory cannot be deleted"},
		{"uploadDirMissing", errWsUploadDirMissing, "The target directory does not exist"},
		{"uploadParse", errWsUploadParse, "Failed to parse the upload, or it exceeded the size limit: "},
		{"noUploadFile", errWsNoUploadFile, "There is no file to upload (form field file)"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// wsErrBody pulls the {"error": "..."} string writeErr produces.
func wsErrBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse the response JSON: %v (body %q)", err, rec.Body.String())
	}
	return out.Error
}

// TestWorkspaceHandlerResponsesLocalized drives the workspace file-manager handlers
// over real HTTP. They only touch s.m.dir and the filesystem (never s.m.pg), so a
// temp-dir Manager is enough — no DB. This proves the English constants actually land
// in the HTTP response body, not just that the constants are English.
func TestWorkspaceHandlerResponsesLocalized(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{dir: dir}}

	cases := []struct {
		name        string
		handler     http.HandlerFunc
		req         *http.Request
		code        int
		wantEnglish string
	}{
		{"read-missing", s.wsRead, httptest.NewRequest(http.MethodGet, "/api/workspace/read?path=nope.txt", nil), 404, "The file does not exist"},
		{"read-dir", s.wsRead, httptest.NewRequest(http.MethodGet, "/api/workspace/read?path=sub", nil), 400, "It is a directory, so it cannot be read as a file"},
		{"list-missing", s.wsList, httptest.NewRequest(http.MethodGet, "/api/workspace/list?path=nope", nil), 404, "The path does not exist"},
		{"list-not-dir", s.wsList, nil, 400, "Not a directory"}, // req built below (needs a real file)
		{"delete-root", s.wsDelete, httptest.NewRequest(http.MethodDelete, "/api/workspace/delete?path=", nil), 400, "The workspace root directory cannot be deleted"},
	}

	// list-not-dir needs an actual file to point at.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases[3].req = httptest.NewRequest(http.MethodGet, "/api/workspace/list?path=a.txt", nil)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.handler(rec, c.req)
			if rec.Code != c.code {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, c.code, rec.Body.String())
			}
			got := wsErrBody(t, rec)
			if got != c.wantEnglish {
				t.Fatalf("error = %q, want %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, c.name, got, c.wantEnglish)
		})
	}
}
