package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Unit test guarding, without a DB, the fix for the reported first-run admin account takeover
// (unauthenticated call to /api/auth/init). It checks the setup token comparison, the token
// signature bound to the password hash, the CORS allowlist, the failed-attempt limiter and the
// loopback binding decision. The full flow through the DB is covered by auth_init_db_test.go.

func TestSigningKeyBoundToPasswordHash(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("k", 32))}
	if !bytes.Equal(s.signingKey(""), s.jwtKey) {
		t.Fatal("with no password the file key must be used as-is (DB-less test compatibility)")
	}
	tok, err := signJWT(s.signingKey("hash-A"))
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(tok, s.signingKey("hash-A")) {
		t.Fatal("a token must verify with the key derived from the same hash")
	}
	if verifyJWT(tok, s.signingKey("hash-B")) {
		t.Fatal("changing the password (hash) must invalidate older tokens")
	}
	if verifyJWT(tok, s.signingKey("")) {
		t.Fatal("a token signed with a hash-derived key must not verify with the original key")
	}
}

func TestVerifyTokenInvalidatedByPasswordChange(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("v", 32))}
	s.setPassHash("hash-A")
	tok, err := signJWT(s.signingKey("hash-A"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.verifyToken(tok) {
		t.Fatal("a token signed with the current hash must pass")
	}
	s.setPassHash("hash-B")
	if s.verifyToken(tok) {
		t.Fatal("an older token must not pass after the password change")
	}
	if s.verifyToken("") {
		t.Fatal("an empty token must not pass")
	}
}

func TestSetupTokenFromEnvAndRandom(t *testing.T) {
	t.Setenv(setupTokenEnv, "fixed-setup-token")
	s := &Server{}
	if !s.checkSetupToken("  fixed-setup-token \n") {
		t.Fatal("the environment token must match ignoring surrounding whitespace")
	}
	if s.checkSetupToken("fixed-setup-tokeN") || s.checkSetupToken("") {
		t.Fatal("a different or empty value must be rejected")
	}

	t.Setenv(setupTokenEnv, "")
	s = &Server{}
	tok := s.currentSetupToken()
	if len(tok) != setupTokenLen {
		t.Fatalf("random setup token length = %d, want %d", len(tok), setupTokenLen)
	}
	if s.currentSetupToken() != tok {
		t.Fatal("a token, once created, must stay the same until cleared")
	}
	if !s.checkSetupToken(tok) || s.checkSetupToken(tok[:len(tok)-1]) {
		t.Fatal("a random token must pass only on an exact match")
	}
	s.clearSetupToken()
	if s.currentSetupToken() == tok {
		t.Fatal("after init clears it a new token must be issued")
	}
}

func TestAuthLimiterCooldown(t *testing.T) {
	l := newAuthLimiter()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	const key = "203.0.113.9"

	for i := 0; i < authFreeFailures; i++ {
		if !l.allow(key) {
			t.Fatalf("must be allowed before failure %d", i+1)
		}
		l.fail(key)
	}
	if l.allow(key) {
		t.Fatalf("must be blocked for the cooldown after %d failures", authFreeFailures)
	}
	now = now.Add(authCooldownBase + time.Second)
	if !l.allow(key) {
		t.Fatal("must be allowed again after the first cooldown elapses")
	}
	l.fail(key)
	now = now.Add(authCooldownBase + time.Second)
	if l.allow(key) {
		t.Fatal("the cooldown must double with each extra failure")
	}
	now = now.Add(authCooldownBase)
	if !l.allow(key) {
		t.Fatal("must be allowed after the doubled cooldown elapses")
	}
	l.reset(key)
	l.fail(key)
	if !l.allow(key) {
		t.Fatal("after a successful reset one failure stays inside the free allowance")
	}
	for i := 0; i < 20; i++ {
		l.fail(key)
	}
	if got := l.clients[key].until.Sub(now); got > authCooldownMax {
		t.Fatalf("cooldown exceeds cap: %v > %v", got, authCooldownMax)
	}
	now = now.Add(authFailureWindow + time.Second)
	if !l.allow(key) {
		t.Fatal("once the failure window passes the record is cleared and access is allowed")
	}
	if l.allow("198.51.100.1") != true {
		t.Fatal("other clients must not be affected")
	}
}

func TestCORSAllowlist(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := corsWith(map[string]bool{"http://localhost:5173": true}, ok)

	call := func(method, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/auth/init", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	if rec := call(http.MethodPost, ""); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("requests without an Origin header (same origin) must not get CORS headers")
	}
	rec := call(http.MethodPost, "https://evil.example")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("an origin not in the allowlist must not get Access-Control-Allow-Origin")
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatal("Vary: Origin is required because responses differ per origin")
	}
	if rec := call(http.MethodOptions, "https://evil.example"); rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("preflight from a non-allowed origin: code=%d ACAO=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	rec = call(http.MethodPost, "http://LOCALHOST:5173")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://LOCALHOST:5173" {
		t.Fatalf("an allowed origin must be echoed back as-is (case-insensitive match): %q", got)
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal("an allowed origin must be permitted to use the Authorization header")
	}

	wild := corsWith(map[string]bool{"*": true}, ok)
	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	r.Header.Set("Origin", "https://anything.example")
	w := httptest.NewRecorder()
	wild.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "https://anything.example" {
		t.Fatal("an explicit \"*\" must allow every origin (previous behaviour)")
	}
}

func TestCORSAllowedOriginsEnv(t *testing.T) {
	t.Setenv(corsOriginsEnv, "")
	got := corsAllowedOrigins()
	for _, o := range defaultCORSOrigins {
		if !got[o] {
			t.Fatalf("default allowlist is missing %s", o)
		}
	}
	t.Setenv(corsOriginsEnv, " https://UI.example.com , http://10.0.0.2:5173,, ")
	got = corsAllowedOrigins()
	if !got["https://ui.example.com"] || !got["http://10.0.0.2:5173"] || len(got) != 2 {
		t.Fatalf("environment list parsed differently: %v", got)
	}
	if got["http://localhost:5173"] {
		t.Fatal("setting the environment variable must drop the defaults")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		":8787":           false,
		"0.0.0.0:8787":    false,
		"[::]:8787":       false,
		"10.0.0.5:8787":   false,
		"127.0.0.1:8787":  true,
		"127.0.0.1":       true,
		"localhost:8787":  true,
		"[::1]:8787":      true,
		"192.168.0.10:80": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestClientKeyIgnoresForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = "198.51.100.7:40000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientKey(r); got != "198.51.100.7" {
		t.Fatalf("the limiter key must be the TCP peer address, not the forgeable X-Forwarded-For: %q", got)
	}
	if id := clientLogID(r); !strings.Contains(id, "198.51.100.7:40000") || !strings.Contains(id, "1.2.3.4") {
		t.Fatalf("the log identifier must contain both values: %q", id)
	}
}
