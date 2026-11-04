package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yukiteruamano/artex/db"
)

// Regression guard for the validation error responses that the custom-tool CRUD and test-run
// endpoints in customtool.go return through writeErr: they are the exact English source messages
// (assertEnglishMessage). The body extraction reuses decodeErrorField from the task_categories test
// (same package server). The actool.Errorf tool results and tool schema
// descriptions read by the agent are model-facing and deliberately not covered here.

// TestCustomToolErrorConstantsLocalized asserts that all eight constants exposed through writeErr
// are their exact English source text. Two of them (errCustomToolKeyExists, errCustomToolEditCustomOnly) are only reachable
// after the handler goes through pg.GetTool (DB), so the HTTP tests below never hit them. They are
// pinned by the constant assertion instead, and which branch uses each constant was confirmed by
// tracing the code path (same verification depth as the goals_api and task_control DB paths).
func TestCustomToolErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ name, msg, wantEnglish string }{
		{"keyFormat", errCustomToolKeyFormat, "key must start with a lowercase letter and may contain only lowercase letters, digits, and underscores"},
		{"kindInvalid", errCustomToolKindInvalid, "The kind value must be one of command, script, http, or shell"},
		{"httpSchemaRequired", errCustomToolHTTPSchemaRequired, "The http tool requires a parameter JSON Schema (it cannot be empty)"},
		{"keyExists", errCustomToolKeyExists, "This key already exists (built-in or custom tool)"},
		{"editCustomOnly", errCustomToolEditCustomOnly, "Only custom tools can be edited"},
		{"badBody", errCustomToolBadBody, "The request body is invalid"},
		{"shellNoExec", errCustomToolShellNoExec, "A shell-type tool is a bash environment declaration, so there is nothing to execute"},
		{"unknownKindPrefix", errCustomToolUnknownKindPrefix, "Unknown tool type: "},
	} {
		assertEnglishMessage(t, "customtool."+c.name, c.msg, c.wantEnglish)
	}
}

// newCustomToolServer builds a Server that passes the pg gate (s.pg) without a real DB connection.
// &db.DB{} has a nil embedded *sql.DB but a non-nil pointer, so pg() returns it instead of writing
// a 503. Every validation branch below returns before the real DB calls such as pg.GetTool, so the
// nil DB is never dereferenced (confirmed by tracing the code path).
func newCustomToolServer() *Server {
	return &Server{m: &Manager{pg: &db.DB{}}}
}

// TestCustomToolCreateValidationLocalized runs the three input-validation paths of
// pgCreateCustomTool over real HTTP and checks that the response body is English. All three return
// before pg.GetTool (line 68).
func TestCustomToolCreateValidationLocalized(t *testing.T) {
	cases := []struct {
		name, body, wantEnglish string
	}{
		{"bad-key", `{"key":"BadKey","kind":"command"}`, "key must start with a lowercase letter and may contain only lowercase letters, digits, and underscores"},
		{"bad-kind", `{"key":"goodkey","kind":"bogus"}`, "The kind value must be one of command, script, http, or shell"},
		{"http-no-schema", `{"key":"goodkey","kind":"http"}`, "The http tool requires a parameter JSON Schema (it cannot be empty)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCustomToolServer()
			req := httptest.NewRequest(http.MethodPost, "/api/tools/custom", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			s.pgCreateCustomTool(rec, req)
			if rec.Code != 400 && rec.Code != 409 {
				t.Fatalf("expected a validation error status code, got %d (body %q)", rec.Code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.wantEnglish {
				t.Fatalf("response body mismatch: got %q want %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, "customtool.create."+c.name, got, c.wantEnglish)
		})
	}
}

// TestCustomToolTestRunValidationLocalized runs the three input-validation paths of
// pgTestCustomTool over real HTTP and checks that the response body is English. None of them make a
// DB call.
func TestCustomToolTestRunValidationLocalized(t *testing.T) {
	cases := []struct {
		name, body, wantEnglish string
	}{
		{"bad-body", `not-json`, "The request body is invalid"},
		{"shell", `{"kind":"shell"}`, "A shell-type tool is a bash environment declaration, so there is nothing to execute"},
		{"unknown-kind", `{"kind":"bogus"}`, "Unknown tool type: bogus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCustomToolServer()
			req := httptest.NewRequest(http.MethodPost, "/api/tools/custom/test", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			s.pgTestCustomTool(rec, req)
			if rec.Code != 400 {
				t.Fatalf("expected status code 400, got %d (body %q)", rec.Code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.wantEnglish {
				t.Fatalf("response body mismatch: got %q want %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, "customtool.test."+c.name, got, c.wantEnglish)
		})
	}
}
