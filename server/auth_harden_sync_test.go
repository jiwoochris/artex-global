package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// 상류 a951e4a 이식(백로그: 인증 읽기 fail-closed + 최초 설정 원자화 + 비밀번호 상한)의
// 회귀 방어. GetSetting 은 "키 없음"과 "읽기 실패"의 반환값이 error 하나만 다르다(둘 다
// value=""). 비밀번호 핸들러가 error 를 "미설정"으로 취급하면 데이터베이스 장애 중 초기화
// 입구가 열린다. 아래 두 테스트는 커넥션 풀을 닫아 읽기 실패를 만들어 fail-closed(503)를
// 확인한다.
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

// 비밀번호 바이트 상한(bcrypt 72바이트)을 서버가 강제하는지 확인한다. 73바이트는 400 으로
// 거부되어야 한다(이전에는 bcrypt 가 해시 단계에서 실패해 500 이 났다).
func TestAuthInitRejectsTooLongPasswordDB(t *testing.T) {
	rig := newAuthTestRig(t)
	tok := rig.s.currentSetupToken()

	code, body := rig.do("POST", "/api/auth/init", "", map[string]any{
		"password":    strings.Repeat("a", 73),
		"setup_token": tok,
	})
	rig.expect(400, code, body, authErrPasswordTooLong)
}

// 토큰 검증과 기동 시 첫 실행 처리도 읽기 실패를 "비밀번호 없음"으로 접으면 안 된다. 접으면
// 빈 해시가 캐시되고(passHashLoaded=true), signingKey("") 가 원시 jwt.key 로 떨어진다.
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

// ARTEX_ADMIN_PASSWORD 초기화도 최초 1회만 써야 한다: 다른 값으로 기동한 두 번째 인스턴스가
// 기존 비밀번호를 덮어쓰면 안 된다.
func TestBootstrapAdminPasswordEnvDoesNotOverwriteDB(t *testing.T) {
	rig := newAuthTestRig(t)
	t.Setenv(adminPasswordEnv, "first-admin-pass-123")
	rig.s.bootstrapAuth()
	first, err := rig.s.reloadPassHash()
	if err != nil || first == "" {
		t.Fatalf("첫 인스턴스가 비밀번호를 초기화해야 합니다 (err=%v)", err)
	}

	// 두 번째 인스턴스: 시작 시점 읽기는 비어 있지 않으므로 바로 돌아가야 하지만, 읽기와
	// 쓰기 사이의 경합을 흉내 내려고 쓰기 경로를 직접 확인한다.
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
