package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Regression guard test that keeps in Korean the validation error responses returned via
// writeErr by the custom tool CRUD · test-run endpoints in customtool.go. It reuses F3a's
// assertKoreanError (contains Hangul · zero Chinese Han) for the Korean check, and
// decodeErrorField from the task_categories tests to extract the response body (same package
// server). The actool.Errorf tool results the agent reads and the tool schema descriptions
// are on the brain boundary, so they are not translation targets and this test does not
// touch them either.

// TestCustomToolErrorConstantsLocalized asserts that all 8 constants exposed via writeErr are
// Korean. Two of them (errCustomToolKeyExists · errCustomToolEditCustomOnly) are only reached
// after the handler goes through pg.GetTool (DB), so the real HTTP tests below cannot reach
// them. They are pinned via constant assertions, and "which branch uses this constant" was
// verified by tracing the code paths (same verification depth as the DB paths in goals_api ·
// task_control).
func TestCustomToolErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ name, msg string }{
		{"keyFormat", errCustomToolKeyFormat},
		{"kindInvalid", errCustomToolKindInvalid},
		{"httpSchemaRequired", errCustomToolHTTPSchemaRequired},
		{"keyExists", errCustomToolKeyExists},
		{"editCustomOnly", errCustomToolEditCustomOnly},
		{"badBody", errCustomToolBadBody},
		{"shellNoExec", errCustomToolShellNoExec},
		{"unknownKindPrefix", errCustomToolUnknownKindPrefix},
	} {
		assertKoreanError(t, "customtool."+c.name, c.msg)
	}
}

// newCustomToolServer builds a Server that passes the pg gate (s.pg) but has no real DB
// connection. &db.DB{} has a nil embedded *sql.DB, but the pointer itself is non-nil, so pg()
// returns it as-is without writing a 503. All the validation branches below return "before"
// any real DB call such as pg.GetTool, so the nil DB is never dereferenced (verified via the
// code paths).
func newCustomToolServer() *Server {
	return &Server{m: &Manager{pg: &db.DB{}}}
}

// TestCustomToolCreateValidationLocalized runs the 3 input-validation paths of
// pgCreateCustomTool over real HTTP and confirms the response bodies are Korean. All three
// paths return before pg.GetTool (line 68).
func TestCustomToolCreateValidationLocalized(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"bad-key", `{"key":"BadKey","kind":"command"}`, errCustomToolKeyFormat},
		{"bad-kind", `{"key":"goodkey","kind":"bogus"}`, errCustomToolKindInvalid},
		{"http-no-schema", `{"key":"goodkey","kind":"http"}`, errCustomToolHTTPSchemaRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCustomToolServer()
			req := httptest.NewRequest(http.MethodPost, "/api/tools/custom", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			s.pgCreateCustomTool(rec, req)
			if rec.Code != 400 && rec.Code != 409 {
				t.Fatalf("검증 오류 상태 코드를 기대했으나 %d (본문 %q)", rec.Code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.want {
				t.Fatalf("응답 본문 불일치: got %q want %q", got, c.want)
			}
			assertKoreanError(t, "customtool.create."+c.name, got)
		})
	}
}

// TestCustomToolTestRunValidationLocalized runs the 3 input-validation paths of
// pgTestCustomTool over real HTTP and confirms the response bodies are Korean. None of the
// three makes a DB call.
func TestCustomToolTestRunValidationLocalized(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"bad-body", `not-json`, errCustomToolBadBody},
		{"shell", `{"kind":"shell"}`, errCustomToolShellNoExec},
		{"unknown-kind", `{"kind":"bogus"}`, errCustomToolUnknownKindPrefix + "bogus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCustomToolServer()
			req := httptest.NewRequest(http.MethodPost, "/api/tools/custom/test", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			s.pgTestCustomTool(rec, req)
			if rec.Code != 400 {
				t.Fatalf("상태 코드 400 을 기대했으나 %d (본문 %q)", rec.Code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.want {
				t.Fatalf("응답 본문 불일치: got %q want %q", got, c.want)
			}
			// unknown-kind has a Latin req.Kind("bogus") suffix appended, so only zero Han is checked.
			assertKoreanError(t, "customtool.test."+c.name, errCustomToolUnknownKindPrefix)
		})
	}
}
