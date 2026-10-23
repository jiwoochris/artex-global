package orcarouter

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func testFlow(t *testing.T, authURL string) *Flow {
	t.Helper()
	return &Flow{
		Endpoints: Endpoints{AuthBase: authURL, APIBase: "https://api.example.invalid/v1"},
		HTTP:      &http.Client{Timeout: 5 * time.Second},
		Listen:    net.Listen,
		AppName:   "ARTEX",
		Timeout:   5 * time.Second,
	}
}

func b64urlSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestStartBuildsS256AuthorizeURLAtAuthOrigin(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer auth.Close()
	f := testFlow(t, auth.URL)
	a, err := f.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Close()

	u, err := url.Parse(a.Authorize)
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	if got, want := u.Scheme+"://"+u.Host, auth.URL; got != want {
		t.Fatalf("authorize origin = %q, want %q", got, want)
	}
	if u.Path != "/auth" {
		t.Fatalf("authorize path = %q, want /auth", u.Path)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("challenge method = %q, want S256", q.Get("code_challenge_method"))
	}
	if want := b64urlSHA256(a.verifier); q.Get("code_challenge") != want {
		t.Fatalf("challenge = %q, want S256(verifier)", q.Get("code_challenge"))
	}
	if q.Get("state") != a.state {
		t.Fatalf("state not echoed in authorize URL")
	}
	if q.Get("scope") != ScopeAPI || q.Get("app_name") != "ARTEX" {
		t.Fatalf("missing scope/app_name: %v", q)
	}
	cb, err := url.Parse(q.Get("callback_url"))
	if err != nil || cb.Hostname() != "127.0.0.1" || cb.Scheme != "http" {
		t.Fatalf("callback_url must be a loopback http URL, got %q", q.Get("callback_url"))
	}
	// The verifier must never travel in a URL.
	if strings.Contains(a.Authorize, a.verifier) {
		t.Fatal("verifier leaked into the authorize URL")
	}
	if strings.Contains(a.Callback, a.verifier) {
		t.Fatal("verifier leaked into the callback URL")
	}
}

func TestLoopbackDeliversCodeAfterStateCheck(t *testing.T) {
	f := testFlow(t, "http://127.0.0.1:1")
	a, err := f.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Close()
	go func() {
		resp, err := http.Get(a.Callback + "?code=one-time&state=" + url.QueryEscape(a.state))
		if err == nil {
			resp.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	code, err := a.Wait(ctx)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != "one-time" {
		t.Fatalf("code = %q", code)
	}
}

func TestStateMismatchRejected(t *testing.T) {
	f := testFlow(t, "http://127.0.0.1:1")
	a, err := f.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Close()
	go func() {
		resp, err := http.Get(a.Callback + "?code=stolen&state=wrong")
		if err == nil {
			resp.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := a.Wait(ctx); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("wait err = %v, want ErrStateMismatch", err)
	}
}

func TestDeniedAuthorizationAndTimeout(t *testing.T) {
	f := testFlow(t, "http://127.0.0.1:1")
	a, err := f.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Close()
	go func() {
		resp, err := http.Get(a.Callback + "?error=access_denied&state=" + url.QueryEscape(a.state))
		if err == nil {
			resp.Body.Close()
		}
	}()
	if _, err := a.Wait(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatalf("wait err = %v, want ErrDenied", err)
	}

	f2 := testFlow(t, "http://127.0.0.1:1")
	f2.Timeout = 120 * time.Millisecond
	a2, err := f2.Start(context.Background())
	if err != nil {
		t.Fatalf("start2: %v", err)
	}
	defer a2.Close()
	if _, err := a2.Wait(context.Background()); !errors.Is(err, ErrTimeout) {
		t.Fatalf("wait err = %v, want ErrTimeout", err)
	}
}

func TestCancelEndsWait(t *testing.T) {
	f := testFlow(t, "http://127.0.0.1:1")
	a, err := f.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	a.Close()
	if _, err := a.Wait(context.Background()); !errors.Is(err, ErrCanceled) {
		t.Fatalf("wait err = %v, want ErrCanceled", err)
	}
}

func TestExchangeUsesAuthOriginAndVerifier(t *testing.T) {
	var (
		mu      sync.Mutex
		path    string
		body    map[string]string
		apiHits int
	)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		defer mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "sk-orca-test-issued", "scope": "api", "user_id": "u1"})
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		apiHits++
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()

	f := &Flow{
		Endpoints: Endpoints{AuthBase: auth.URL, APIBase: api.URL + "/v1"},
		HTTP:      &http.Client{Timeout: 5 * time.Second},
	}
	res, err := f.Exchange(context.Background(), "code-1", "verifier-1")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Key != "sk-orca-test-issued" || res.Scope != "api" || res.UserID != "u1" {
		t.Fatalf("result = %+v", res)
	}
	if path != "/api/v1/auth/keys" {
		t.Fatalf("exchange path = %q, want /api/v1/auth/keys", path)
	}
	if body["code"] != "code-1" || body["code_verifier"] != "verifier-1" || body["code_challenge_method"] != "S256" {
		t.Fatalf("exchange body = %v", body)
	}
	if apiHits != 0 {
		t.Fatalf("exchange hit the inference origin %d times", apiHits)
	}
}

func TestExchangeErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{400, ErrBadRequest},
		{403, ErrCodeRejected},
		{429, ErrRateLimited},
		{500, nil},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":"nope"}`)
		}))
		f := testFlow(t, srv.URL)
		_, err := f.Exchange(context.Background(), "c", "v")
		if tc.want == nil {
			if err == nil {
				t.Fatalf("status %d: want error", tc.status)
			}
		} else if !errors.Is(err, tc.want) {
			t.Fatalf("status %d: err=%v want %v", tc.status, err, tc.want)
		}
		srv.Close()
	}
}

func TestExchangeScopeDowngradeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"key":"sk-orca-test-issued","scope":"connector"}`)
	}))
	defer srv.Close()
	f := testFlow(t, srv.URL)
	if _, err := f.Exchange(context.Background(), "c", "v"); !errors.Is(err, ErrScopeDowngrade) {
		t.Fatalf("err = %v, want ErrScopeDowngrade", err)
	}
}

func TestExchangeNetworkErrorDoesNotLeakSecrets(t *testing.T) {
	f := &Flow{
		Endpoints: Endpoints{AuthBase: "http://127.0.0.1:1"},
		HTTP:      &http.Client{Timeout: 200 * time.Millisecond},
	}
	_, err := f.Exchange(context.Background(), "secret-code", "super-secret-verifier")
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), "super-secret-verifier") || strings.Contains(err.Error(), "secret-code") {
		t.Fatalf("error leaked credential material: %v", err)
	}
}

func TestVerifierAndStateAreFreshPerAttempt(t *testing.T) {
	f := testFlow(t, "http://127.0.0.1:1")
	a1, err := f.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a1.Close()
	a2, err := f.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	if a1.verifier == a2.verifier || a1.state == a2.state {
		t.Fatal("verifier/state reused across attempts")
	}
	if b64urlSHA256(a1.verifier) == b64urlSHA256(a2.verifier) {
		t.Fatal("challenge reused across attempts")
	}
}
