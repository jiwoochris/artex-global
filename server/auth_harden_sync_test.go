package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard for the port of upstream a951e4a (backlog: fail-closed auth reads +
// atomic first-time setup + password length cap). GetSetting's return values for "key
// missing" and "read failed" differ only in the error (both have value=""). If a password
// handler treats the error as "not set", the initialization entrypoint opens up during a
// database outage. The two tests below close the connection pool to force a read failure
// and verify fail-closed behavior (503).
func TestAuthStatusFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	s.authStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))

	if w.Code != 503 {
		t.Fatalf("status=%d, 503 을 기대했습니다 (읽기 실패를 미초기화로 보면 사용자가 /setup 으로 가서 비밀번호를 덮어씁니다); body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Initialized *bool `json:"initialized"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err == nil && payload.Initialized != nil {
		t.Fatalf("읽기 실패 시 initialized 를 답하면 안 됩니다: %v", *payload.Initialized)
	}
}

func TestAuthInitFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"password":"correct horse battery","setup_token":"x"}`)
	s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", body))

	if w.Code != 503 {
		t.Fatalf("status=%d, 503 을 기대했습니다 (읽기 실패 시 통과시키면 인증되지 않은 요청이 기존 비밀번호를 덮어씁니다); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Fatalf("읽기 실패 시 token 을 발급하면 안 됩니다: %s", w.Body.String())
	}
}

// Verifies that the server enforces the password byte cap (bcrypt's 72 bytes). 73 bytes
// must be rejected with a 400 (previously bcrypt failed at the hashing step and caused a 500).
func TestAuthInitRejectsTooLongPasswordDB(t *testing.T) {
	rig := newAuthTestRig(t)
	tok := rig.s.currentSetupToken()

	code, body := rig.do("POST", "/api/auth/init", "", map[string]any{
		"password":    strings.Repeat("a", 73),
		"setup_token": tok,
	})
	rig.expect(400, code, body, authErrPasswordTooLong)
}

// Token validation and first-run handling at startup must not collapse a read failure into
// "no password" either. If they do, an empty hash gets cached (passHashLoaded=true) and
// signingKey("") falls back to the raw jwt.key.
func TestVerifyTokenFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m, jwtKey: []byte(strings.Repeat("s", 32))}
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	if s.verifyToken(tok) {
		t.Fatal("비밀번호 상태를 읽을 수 없는 동안에는 어떤 토큰도 통과하면 안 됩니다")
	}
}

func TestBootstrapAuthSkipsFirstRunWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(adminPasswordEnv, "env-admin-pass-123")

	s := &Server{m: m}
	s.bootstrapAuth()
	s.authMu.Lock()
	loaded := s.passHashLoaded
	s.authMu.Unlock()
	if loaded {
		t.Fatal("읽기 실패를 \"비밀번호 없음\"으로 캐시하면 안 됩니다")
	}
}

// ARTEX_ADMIN_PASSWORD initialization must also write only once, the first time: a second
// instance started with a different value must not overwrite the existing password.
func TestBootstrapAdminPasswordEnvDoesNotOverwriteDB(t *testing.T) {
	rig := newAuthTestRig(t)
	t.Setenv(adminPasswordEnv, "first-admin-pass-123")
	rig.s.bootstrapAuth()
	first, err := rig.s.reloadPassHash()
	if err != nil || first == "" {
		t.Fatalf("첫 인스턴스가 비밀번호를 초기화해야 합니다 (err=%v)", err)
	}

	// Second instance: the read at startup is non-empty so it should return right away, but
	// to simulate a race between the read and the write we check the write path directly.
	inserted, err := rig.s.m.pg.InsertSettingIfAbsent(authPassKey, "second-hash")
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("이미 있는 비밀번호 위에 insert 가 성공하면 안 됩니다")
	}
	t.Setenv(adminPasswordEnv, "second-admin-pass-456")
	s2 := &Server{m: rig.s.m, jwtKey: rig.s.jwtKey}
	s2.bootstrapAuth()
	if got, _ := s2.reloadPassHash(); got != first {
		t.Fatal("두 번째 인스턴스가 기존 비밀번호를 덮어썼습니다")
	}
}

func TestBootstrapAdminPasswordEnvRejectsOverBcryptLimitDB(t *testing.T) {
	rig := newAuthTestRig(t)
	t.Setenv(adminPasswordEnv, strings.Repeat("a", 73))
	rig.s.bootstrapAuth()
	if h, _ := rig.s.reloadPassHash(); h != "" {
		t.Fatal("72바이트를 넘는 환경 변수 비밀번호는 무시되어야 합니다")
	}
	if rig.s.currentSetupToken() == "" {
		t.Fatal("무시된 뒤에는 설정 토큰이 발급되어야 합니다")
	}
}
