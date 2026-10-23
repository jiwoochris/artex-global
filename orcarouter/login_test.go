package orcarouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

type saveRecorder struct {
	mu       sync.Mutex
	calls    []string
	profiles []int64
	delay    time.Duration
}

func (s *saveRecorder) save(_ context.Context, profileID int64, key string) (int64, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, key)
	s.profiles = append(s.profiles, profileID)
	id := profileID
	if id == 0 {
		id = 42
	}
	return id, nil
}

func (s *saveRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func newAuthServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/keys" {
			t.Errorf("exchange hit %q", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func newManager(t *testing.T, authURL string, rec *saveRecorder, timeout time.Duration) *LoginManager {
	t.Helper()
	return &LoginManager{
		FlowFactory: func() (*Flow, error) {
			f := testFlow(t, authURL)
			if timeout > 0 {
				f.Timeout = timeout
			}
			return f, nil
		},
		Save: rec.save,
	}
}

func deliver(t *testing.T, st LoginState, code string) {
	t.Helper()
	u, err := url.Parse(st.AuthorizeURL)
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	cb := u.Query().Get("callback_url")
	state := u.Query().Get("state")
	target := cb + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	if !containsPrefix(target, cb) {
		t.Fatalf("callback %q not loopback", cb)
	}
	go func() {
		resp, err := http.Get(target)
		if err == nil {
			resp.Body.Close()
		}
	}()
}

func waitState(t *testing.T, m *LoginManager, id string, want string, timeout time.Duration) LoginState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, ok := m.Status(id)
		if ok && st.State == want {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := m.Status(id)
	t.Fatalf("state = %q (%s), want %q", st.State, st.Error, want)
	return LoginState{}
}

func containsPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func TestLoginSuccessPersistsIssuedKey(t *testing.T) {
	auth := newAuthServer(t, 200, `{"key":"sk-orca-test-issued","scope":"api","user_id":"u1"}`)
	defer auth.Close()
	rec := &saveRecorder{}
	m := newManager(t, auth.URL, rec, 5*time.Second)

	st, err := m.Start(context.Background(), 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if st.State != StatePending || st.AuthorizeURL == "" {
		t.Fatalf("start state = %+v", st)
	}
	deliver(t, st, "code-1")
	done := waitState(t, m, st.ID, StateSuccess, 5*time.Second)
	if done.ProfileID != 42 {
		t.Fatalf("profile id = %d", done.ProfileID)
	}
	if rec.count() != 1 || rec.calls[0] != "sk-orca-test-issued" {
		t.Fatalf("save calls = %v", rec.calls)
	}
}

func TestLoginDenialEndsCleanlyWithoutSaving(t *testing.T) {
	auth := newAuthServer(t, 200, `{"key":"sk-orca-test-issued","scope":"api"}`)
	defer auth.Close()
	rec := &saveRecorder{}
	m := newManager(t, auth.URL, rec, 5*time.Second)
	st, _ := m.Start(context.Background(), 0)

	u, _ := url.Parse(st.AuthorizeURL)
	cb := u.Query().Get("callback_url")
	state := u.Query().Get("state")
	go func() {
		resp, err := http.Get(cb + "?error=access_denied&state=" + url.QueryEscape(state))
		if err == nil {
			resp.Body.Close()
		}
	}()
	done := waitState(t, m, st.ID, StateError, 5*time.Second)
	if done.Error != "authorization was denied" {
		t.Fatalf("error = %q", done.Error)
	}
	if rec.count() != 0 {
		t.Fatal("denied attempt must not persist a key")
	}
}

func TestLoginExchangeRateLimitIsActionable(t *testing.T) {
	auth := newAuthServer(t, 429, `{}`)
	defer auth.Close()
	m := newManager(t, auth.URL, &saveRecorder{}, 5*time.Second)
	st, _ := m.Start(context.Background(), 0)
	deliver(t, st, "code-1")
	done := waitState(t, m, st.ID, StateError, 5*time.Second)
	if done.Error != "too many authorization attempts; try again later" {
		t.Fatalf("error = %q", done.Error)
	}
}

func TestLoginTimeoutReleasesState(t *testing.T) {
	auth := newAuthServer(t, 200, `{}`)
	defer auth.Close()
	m := newManager(t, auth.URL, &saveRecorder{}, 150*time.Millisecond)
	st, _ := m.Start(context.Background(), 0)
	done := waitState(t, m, st.ID, StateError, 3*time.Second)
	if done.Error != "authorization timed out" {
		t.Fatalf("error = %q", done.Error)
	}
}

func TestLoginCancel(t *testing.T) {
	auth := newAuthServer(t, 200, `{}`)
	defer auth.Close()
	m := newManager(t, auth.URL, &saveRecorder{}, 5*time.Second)
	st, _ := m.Start(context.Background(), 0)
	m.Cancel(st.ID)
	waitState(t, m, st.ID, StateCanceled, 2*time.Second)
}

// TestStaleAttemptCannotOverwriteNewerLogin is the generation-safety case: an
// abandoned attempt that finishes late must not replace the credential issued
// by the newer attempt.
func TestStaleAttemptCannotOverwriteNewerLogin(t *testing.T) {
	auth := newAuthServer(t, 200, `{"key":"sk-orca-test-issued","scope":"api"}`)
	defer auth.Close()
	rec := &saveRecorder{}
	m := newManager(t, auth.URL, rec, 5*time.Second)

	first, _ := m.Start(context.Background(), 7)
	second, _ := m.Start(context.Background(), 7)
	deliver(t, first, "code-old")
	time.Sleep(300 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatal("stale attempt overwrote the credential")
	}
	deliver(t, second, "code-new")
	waitState(t, m, second.ID, StateSuccess, 5*time.Second)
	if rec.count() != 1 {
		t.Fatalf("save calls = %v", rec.calls)
	}
	if rec.profiles[0] != 7 {
		t.Fatalf("saved profile = %d", rec.profiles[0])
	}
}
