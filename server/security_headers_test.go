package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newHandlerTestServer builds the smallest *Server that can serve Handler(): the
// routes these tests exercise need neither a PostgreSQL manager nor a live
// engine, and the JWT key is fixed so tokens are reproducible.
func newHandlerTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{m: &Manager{dir: t.TempDir()}, jwtKey: []byte("0123456789abcdef0123456789abcdef")}
}

// TestSecurityHeaders pins M-2: every response the server emits — the API, the
// embedded UI, and error responses — carries the hardening headers, and a normal
// API response still works with them.
func TestSecurityHeaders(t *testing.T) {
	h := newHandlerTestServer(t).Handler()

	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Content-Security-Policy":      contentSecurityPolicy,
	}
	for _, c := range []struct {
		name string
		path string
		code int // 0 = any status (the UI path differs between the stub and the embedui build)
	}{
		{"api", "/api/health", http.StatusOK},
		{"ui", "/", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
			if c.code != 0 && rec.Code != c.code {
				t.Fatalf("%s status = %d, want %d", c.path, rec.Code, c.code)
			}
			for name, value := range want {
				if got := rec.Header().Get(name); got != value {
					t.Errorf("%s header %s = %q, want %q", c.path, name, got, value)
				}
			}
			// A normal API response still works: health answers JSON.
			if c.path == "/api/health" {
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("health content type = %q, want application/json", ct)
				}
				if body := rec.Body.String(); !strings.Contains(body, `"service":"artex"`) {
					t.Errorf("health body = %q, want the service payload", body)
				}
			}
		})
	}
}

// TestSecurityHeadersCSPKeepsTheExportWorking guards the policy against a future
// tightening that would silently break the statically exported UI: the export
// ships inline <script> (theme boot + Turbopack manifest) and React writes inline
// style attributes, so both directives must allow 'unsafe-inline', while framing
// and object embedding stay refused.
func TestSecurityHeadersCSPKeepsTheExportWorking(t *testing.T) {
	for _, directive := range []string{
		"script-src 'self' 'unsafe-inline'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:", // the coverage graph renders a data: SVG
		"frame-ancestors 'none'",
		"object-src 'none'",
		"base-uri 'none'",
	} {
		if !strings.Contains(contentSecurityPolicy, directive) {
			t.Errorf("contentSecurityPolicy is missing %q: %s", directive, contentSecurityPolicy)
		}
	}
}

// TestRequestBodiesAreCapped pins L-4's body limit: an oversized JSON body is
// rejected with 413 before any handler parses it, an ordinary body reaches the
// handler intact, and multipart uploads stay exempt (they carry their own, larger
// bounded reader — a 20 MiB skill zip and a 512 MiB work-dir upload are legal).
func TestRequestBodiesAreCapped(t *testing.T) {
	h := newHandlerTestServer(t).Handler()

	t.Run("oversized body is 413", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(strings.Repeat("a", maxRequestBytes+1)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("normal body reaches the handler", func(t *testing.T) {
		var got string
		capped := limitRequestBodies(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			w.WriteHeader(http.StatusNoContent)
		}))
		req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"description":"smoke"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		capped.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
		if got != `{"description":"smoke"}` {
			t.Fatalf("handler saw body %q, want the request body unchanged", got)
		}
	})

	t.Run("multipart upload is exempt from the JSON cap", func(t *testing.T) {
		called := false
		capped := limitRequestBodies(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}))
		big := strings.NewReader(strings.Repeat("a", maxRequestBytes+1))
		req := httptest.NewRequest(http.MethodPost, "/api/chat/upload", big)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		capped.ServeHTTP(httptest.NewRecorder(), req)
		if !called {
			t.Fatal("a multipart upload above the JSON cap must still reach its handler")
		}
	})
}

// TestStreamRoutesAreIdentified pins the route set that gets the two stream-only
// treatments (the ?token= query parameter and the cleared write deadline) and
// proves ordinary routes are not in it.
func TestStreamRoutesAreIdentified(t *testing.T) {
	for _, p := range []string{"/api/logs/stream", "/api/update/stream", "/api/exploration/activity/stream", "/api/side-questions/abc/events"} {
		if !isStreamPath(p) {
			t.Errorf("isStreamPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/api/stats", "/api/tasks", "/api/logs"} {
		if isStreamPath(p) {
			t.Errorf("isStreamPath(%q) = true, want false", p)
		}
	}
}
