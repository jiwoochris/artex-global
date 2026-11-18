package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP is a minimal OIDC provider: discovery, JWKS and a token endpoint that
// checks PKCE and returns an RS256 ID token carrying the nonce from /authorize.
type fakeIdP struct {
	srv           *httptest.Server
	key           *rsa.PrivateKey
	nonce, chall  string
	email         string
	emailVerified any
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: k, email: "ops@noraneko.cc", emailVerified: true}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.chall {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		claims := jwt.MapClaims{
			"iss": f.srv.URL, "aud": "artex", "sub": "user-1", "nonce": f.nonce,
			"email": f.email, "email_verified": f.emailVerified,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "k1"
		idt, err := tok.SignedString(k)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idt})
	})
	return f
}

func oidcTestServer(t *testing.T, issuer string) *Server {
	t.Helper()
	t.Setenv("ARTEX_OIDC_ISSUER", issuer)
	t.Setenv("ARTEX_OIDC_CLIENT_ID", "artex")
	t.Setenv("ARTEX_OIDC_CLIENT_SECRET", "secret")
	t.Setenv("ARTEX_OIDC_REDIRECT_URL", "https://artex.example/api/auth/oidc/callback")
	t.Setenv("ARTEX_OIDC_ALLOWED_EMAILS", "Ops@Noraneko.cc, admin@noraneko.cc")
	oidcMu.Lock()
	oidcCached = nil
	oidcMu.Unlock()
	return &Server{jwtKey: []byte(strings.Repeat("k", 32))}
}

// start runs /start and returns the flow cookie and the state from the redirect,
// recording the nonce and PKCE challenge on the fake provider.
func start(t *testing.T, s *Server, f *fakeIdP) (*http.Cookie, string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.oidcStart(w, httptest.NewRequest("GET", "/api/auth/oidc/start", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("start: status %d, location %q", w.Code, w.Header().Get("Location"))
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "artex" {
		t.Fatalf("start: bad authorize query %v", q)
	}
	f.nonce, f.chall = q.Get("nonce"), q.Get("code_challenge")
	return w.Result().Cookies()[0], q.Get("state")
}

func callback(s *Server, ck *http.Cookie, state string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/auth/oidc/callback?code=c&state="+url.QueryEscape(state), nil)
	if ck != nil {
		r.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	s.oidcCallback(w, r)
	return w
}

func TestOIDCSignInIssuesSession(t *testing.T) {
	f := newFakeIdP(t)
	s := oidcTestServer(t, f.srv.URL)
	ck, state := start(t, s, f)
	w := callback(s, ck, state)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.HasPrefix(loc, "/login#token=") {
		t.Fatalf("callback: status %d, location %q", w.Code, loc)
	}
	tok, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/login#token="))
	if !s.verifyToken(tok) {
		t.Fatal("issued session token does not verify")
	}
}

func TestOIDCRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *fakeIdP)
		state  func(string) string
		cookie bool
		want   string
	}{
		{"email not on allowlist", func(f *fakeIdP) { f.email = "intruder@example.com" }, nil, true, "forbidden"},
		{"email not verified", func(f *fakeIdP) { f.emailVerified = false }, nil, true, "forbidden"},
		{"state mismatch", nil, func(string) string { return "other" }, true, "state"},
		{"no flow cookie", nil, nil, false, "state"},
		{"wrong nonce", func(f *fakeIdP) { f.nonce = "replayed" }, nil, true, "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIdP(t)
			s := oidcTestServer(t, f.srv.URL)
			ck, state := start(t, s, f)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			if tc.state != nil {
				state = tc.state(state)
			}
			if !tc.cookie {
				ck = nil
			}
			w := callback(s, ck, state)
			if loc := w.Header().Get("Location"); loc != "/login?sso_error="+tc.want {
				t.Fatalf("got %d %q, want sso_error=%s", w.Code, loc, tc.want)
			}
		})
	}
}

func TestOIDCFailsClosedWithoutConfig(t *testing.T) {
	s := oidcTestServer(t, "")
	w := httptest.NewRecorder()
	s.oidcStart(w, httptest.NewRequest("GET", "/api/auth/oidc/start", nil))
	if loc := w.Header().Get("Location"); loc != "/login?sso_error=unavailable" {
		t.Fatalf("got %q", loc)
	}
}

func TestPasswordRoutesAreGone(t *testing.T) {
	s := oidcTestServer(t, "")
	h := s.Handler()
	for _, p := range []string{"/api/auth/login", "/api/auth/init", "/api/auth/change-password"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", p, strings.NewReader(`{"username":"ARTEX","password":"x"}`)))
		if w.Code == http.StatusOK {
			t.Fatalf("%s still answers 200", p)
		}
		if strings.Contains(w.Body.String(), `"token"`) {
			t.Fatalf("%s still issues a token", p)
		}
	}
}
