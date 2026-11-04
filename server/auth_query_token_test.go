package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestQueryParamTokenOnlyOnStreams pins L-1: the JWT is accepted from the query
// string only on the SSE routes (EventSource cannot set an Authorization header).
// On every other route ?token= is ignored, so a token that leaked into a Referer
// header, a proxy log or the browser history cannot be replayed against the API.
func TestQueryParamTokenOnlyOnStreams(t *testing.T) {
	s := newHandlerTestServer(t)
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	t.Run("extractToken ignores the query on a normal route", func(t *testing.T) {
		if got := extractToken(httptest.NewRequest(http.MethodGet, "/api/stats?token="+tok, nil)); got != "" {
			t.Fatalf("extractToken on /api/stats = %q, want empty", got)
		}
	})

	t.Run("extractToken reads the query on a stream route", func(t *testing.T) {
		if got := extractToken(httptest.NewRequest(http.MethodGet, "/api/logs/stream?token="+tok, nil)); got != tok {
			t.Fatalf("extractToken on /api/logs/stream = %q, want the token", got)
		}
	})

	t.Run("header wins on every route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/stats?token=stale-query-token", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if got := extractToken(req); got != tok {
			t.Fatalf("extractToken = %q, want the Authorization header token", got)
		}
		req = httptest.NewRequest(http.MethodGet, "/api/logs/stream?token=stale-query-token", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if got := extractToken(req); got != tok {
			t.Fatalf("extractToken on a stream = %q, want the Authorization header token", got)
		}
	})

	t.Run("cookie is still accepted everywhere", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
		req.AddCookie(&http.Cookie{Name: "artex_token", Value: tok})
		if got := extractToken(req); got != tok {
			t.Fatalf("extractToken = %q, want the cookie token", got)
		}
	})

	// End to end through the real mux: a query-only token is refused by a normal
	// API route and accepted by the streaming route it exists for.
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats?token="+tok, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/stats?token=<jwt> status = %d, want 401", rec.Code)
	}

	// streamLogs returns as soon as the request context is done, so a cancelled
	// context makes the SSE route testable without waiting for a frame.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	streamReq := httptest.NewRequest(http.MethodGet, "/api/logs/stream?token="+tok, nil).WithContext(ctx)
	streamRec := httptest.NewRecorder()
	h.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusOK {
		t.Fatalf("GET /api/logs/stream?token=<jwt> status = %d, want 200 (body %s)", streamRec.Code, streamRec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil).WithContext(ctx))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/logs/stream without a token status = %d, want 401", rec.Code)
	}
}
