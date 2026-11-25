package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Autumn-27/artex/db"
)

// Integration test that verifies, end to end against a real DB, the fix for the reported
// first-run admin account takeover (unauthenticated call to /api/auth/init). Covers:
// rejecting initialization without a setup token, successful initialization with a token,
// invalidation of existing tokens after a password change and after an external reset
// (reset-password.sh), login failure throttling, and initialization from an environment
// variable. Skipped like other integration tests when no DB is configured.

type authTestRig struct {
	t *testing.T
	s *Server
	h http.Handler
}

func newAuthTestRig(t *testing.T) *authTestRig {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	clear := func() { _, _ = d.Exec(`DELETE FROM settings WHERE key = $1`, authPassKey) }
	clear()
	t.Cleanup(clear)
	t.Setenv(setupTokenEnv, "")
	t.Setenv(adminPasswordEnv, "")

	s := &Server{m: &Manager{pg: d}, jwtKey: []byte(strings.Repeat("s", 32))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/init", s.authInit)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/change-password", s.authChangePassword)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return &authTestRig{t: t, s: s, h: s.requireAuth(mux)}
}

func (r *authTestRig) do(method, path, token string, body map[string]any) (int, map[string]any) {
	r.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (r *authTestRig) expect(code int, got int, body map[string]any, wantErr string) {
	r.t.Helper()
	if got != code {
		r.t.Fatalf("상태 코드 = %d, 기대 %d (body=%v)", got, code, body)
	}
	if wantErr != "" {
		if msg, _ := body["error"].(string); msg != wantErr {
			r.t.Fatalf("오류 문구 = %q, 기대 %q", msg, wantErr)
		}
	}
}

func TestAuthInitRequiresSetupTokenDB(t *testing.T) {
	rig := newAuthTestRig(t)
	s := rig.s
	s.bootstrapAuth()
	if h, _ := s.cachedPassHash(); h != "" {
		t.Fatal("빈 DB 에서는 비밀번호가 없어야 합니다")
	}
	setup := s.currentSetupToken()
	if setup == "" {
		t.Fatal("기동 시 설정 토큰이 만들어져야 합니다")
	}

	code, body := rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(200, code, body, "")
	if body["initialized"] != false || body["setup_token_required"] != true {
		t.Fatalf("초기화 전 status 가 다릅니다: %v", body)
	}

	// The report's PoC: a call that sends only a password without a token must be rejected.
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "attacker-set-password-123!"})
	rig.expect(403, code, body, authErrSetupTokenInvalid)
	if _, has := body["token"]; has {
		t.Fatal("거부 응답에 토큰이 실리면 안 됩니다")
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "attacker-set-password-123!", "setup_token": "guess"})
	rig.expect(403, code, body, authErrSetupTokenInvalid)
	if h, _ := s.reloadPassHash(); h != "" {
		t.Fatal("거부된 호출이 비밀번호를 저장하면 안 됩니다")
	}

	// Even with a correct token, the server checks the minimum length.
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "short", "setup_token": setup})
	rig.expect(400, code, body, authErrPasswordTooShort)
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "", "setup_token": setup})
	rig.expect(400, code, body, authErrPasswordEmpty)

	// Correct token + password → token issued, protected path passes.
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "operator-pass-123", "setup_token": " " + setup + "\n"})
	rig.expect(200, code, body, "")
	tok1, _ := body["token"].(string)
	if tok1 == "" {
		t.Fatal("초기화 성공 시 토큰이 있어야 합니다")
	}
	code, body = rig.do(http.MethodGet, "/api/ping", tok1, nil)
	rig.expect(200, code, body, "")
	code, body = rig.do(http.MethodGet, "/api/ping", "", nil)
	rig.expect(401, code, body, authErrUnauthorized)
	code, body = rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(200, code, body, "")
	if body["initialized"] != true || body["setup_token_required"] != false {
		t.Fatalf("초기화 뒤 status 가 다릅니다: %v", body)
	}

	// Once it has been set, it cannot be re-initialized even by someone who knows the setup token.
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "second-pass-123", "setup_token": setup})
	rig.expect(403, code, body, authErrPasswordAlreadySet)

	// Login: both username and password must match.
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "wrong-pass-123"})
	rig.expect(401, code, body, authErrBadCredential)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "admin", "password": "operator-pass-123"})
	rig.expect(401, code, body, authErrBadCredential)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-pass-123"})
	rig.expect(200, code, body, "")
	tok2, _ := body["token"].(string)
	code, body = rig.do(http.MethodGet, "/api/ping", tok2, nil)
	rig.expect(200, code, body, "")

	// Password change: all previous tokens die, only the new token in the response stays valid.
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "nope-nope-123", "new_password": "changed-pass-123"})
	rig.expect(401, code, body, authErrCurrentPasswordWrong)
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "operator-pass-123", "new_password": "short"})
	rig.expect(400, code, body, authErrPasswordTooShort)
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "operator-pass-123", "new_password": "changed-pass-123"})
	rig.expect(200, code, body, "")
	tok3, _ := body["token"].(string)
	if tok3 == "" {
		t.Fatal("비밀번호 변경 응답에 새 토큰이 있어야 합니다")
	}
	for _, old := range []string{tok1, tok2} {
		code, body = rig.do(http.MethodGet, "/api/ping", old, nil)
		rig.expect(401, code, body, authErrTokenInvalid)
	}
	code, body = rig.do(http.MethodGet, "/api/ping", tok3, nil)
	rig.expect(200, code, body, "")
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", tok1, map[string]any{"old_password": "changed-pass-123", "new_password": "another-pass-123"})
	rig.expect(401, code, body, authErrUnauthorized)

	// Even if the hash is changed outside the process, as reset-password.sh does, existing
	// tokens die without a restart and login works with the new password. Within the cache
	// expiry window (passHashCacheTTL) the previous token still passes; once the window has
	// elapsed (here shrunk to 0, i.e. immediately) it must be rejected.
	resetHash, err := bcrypt.GenerateFromPassword([]byte("reset-pass-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.SetSetting(authPassKey, string(resetHash)); err != nil {
		t.Fatal(err)
	}
	code, body = rig.do(http.MethodGet, "/api/ping", tok3, nil)
	rig.expect(200, code, body, "")
	prevTTL := passHashCacheTTL
	passHashCacheTTL = 0
	t.Cleanup(func() { passHashCacheTTL = prevTTL })
	code, body = rig.do(http.MethodGet, "/api/ping", tok3, nil)
	rig.expect(401, code, body, authErrTokenInvalid)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "changed-pass-123"})
	rig.expect(401, code, body, authErrBadCredential)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "reset-pass-123"})
	rig.expect(200, code, body, "")
	tok4, _ := body["token"].(string)
	code, body = rig.do(http.MethodGet, "/api/ping", tok4, nil)
	rig.expect(200, code, body, "")
}

func TestAuthLoginThrottledDB(t *testing.T) {
	rig := newAuthTestRig(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("operator-pass-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.s.m.pg.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	rig.s.bootstrapAuth()
	for i := 0; i < authFreeFailures; i++ {
		code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "brute-force-attempt"})
		rig.expect(401, code, body, authErrBadCredential)
	}
	code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-pass-123"})
	rig.expect(429, code, body, authErrTooManyAttempts)
	if _, has := body["token"]; has {
		t.Fatal("냉각 중에는 올바른 비밀번호라도 토큰을 주면 안 됩니다")
	}
	rig.s.limiter().reset(clientKey(httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)))
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-pass-123"})
	rig.expect(200, code, body, "")
}

func TestAuthBootstrapAdminPasswordEnvDB(t *testing.T) {
	rig := newAuthTestRig(t)
	t.Setenv(adminPasswordEnv, "env-admin-pass-123")
	rig.s.bootstrapAuth()
	code, body := rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(200, code, body, "")
	if body["initialized"] != true {
		t.Fatalf("환경 변수로 초기화되어야 합니다: %v", body)
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "attacker-set-password-123!", "setup_token": rig.s.currentSetupToken()})
	rig.expect(403, code, body, authErrPasswordAlreadySet)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "env-admin-pass-123"})
	rig.expect(200, code, body, "")
	tok, _ := body["token"].(string)
	code, body = rig.do(http.MethodGet, "/api/ping", tok, nil)
	rig.expect(200, code, body, "")

	// An environment variable value that is too short is ignored and the setup token flow remains.
	_, _ = rig.s.m.pg.Exec(`DELETE FROM settings WHERE key = $1`, authPassKey)
	t.Setenv(adminPasswordEnv, "short")
	s2 := &Server{m: rig.s.m, jwtKey: rig.s.jwtKey}
	s2.bootstrapAuth()
	if h, _ := s2.cachedPassHash(); h != "" {
		t.Fatal("8자 미만 환경 변수 비밀번호는 무시되어야 합니다")
	}
	if s2.currentSetupToken() == "" {
		t.Fatal("무시된 뒤에는 설정 토큰이 발급되어야 합니다")
	}
}
