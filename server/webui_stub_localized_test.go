//go:build !embedui

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebUIStubErrorLocalized guards the no-embed stub's HTTP response: a request
// served by the default (!embedui) build must get the exact English 404 body. This is
// the last user-facing HTTP error response in server/ that the earlier `writeErr(`-only
// sweeps missed (it uses http.Error, not writeErr). If an upstream re-sync reintroduces
// the old localized literal, this test fails.
func TestWebUIStubErrorLocalized(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	(&Server{}).webuiHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status code is not 404: %d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	assertEnglishMessage(t, "webui.stub", body,
		"The frontend is not embedded in this binary (during development run it with next dev; build the distributable with -tags embedui).")
	// Command references must be preserved verbatim (not a translation target).
	for _, want := range []string{"next dev", "-tags embedui"} {
		if !strings.Contains(body, want) {
			t.Fatalf("command reference %q is missing from the response: %q", want, body)
		}
	}
}
