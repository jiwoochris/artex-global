package chatgptplan

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestOAuthRefreshModelsAndDisconnect(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "test-key"
	now := time.Now().UTC().Truncate(time.Second)
	var (
		mu             sync.Mutex
		nonce          string
		issuer         string
		refreshSeen    string
		revocationSeen string
	)

	var authServer *httptest.Server
	authServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jwks":
			e := big.NewInt(int64(key.PublicKey.E)).Bytes()
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
				"kty": "RSA", "kid": kid, "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(e),
			}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				claims := jwt.MapClaims{
					"iss": issuer, "aud": "oaiapp_test", "sub": "account-1",
					"email": "dev@example.com", "nonce": nonce,
					"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
				}
				tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
				tok.Header["kid"] = kid
				raw, signErr := tok.SignedString(key)
				if signErr != nil {
					t.Error(signErr)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "access-one", "refresh_token": "refresh-one",
					"id_token": raw, "expires_in": 3600, "token_type": "Bearer", "scope": scopes,
				})
			case "refresh_token":
				mu.Lock()
				refreshSeen = r.Form.Get("refresh_token")
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "access-two", "refresh_token": "refresh-two",
					"expires_in": 3600, "token_type": "Bearer", "scope": scopes,
				})
			default:
				http.Error(w, "bad grant", http.StatusBadRequest)
			}
		case "/revoke":
			_ = r.ParseForm()
			mu.Lock()
			revocationSeen = r.Form.Get("token") + ":" + r.Form.Get("token_type_hint")
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()
	issuer = authServer.URL

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-two" {
			t.Errorf("models authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{
			map[string]any{"slug": "hidden", "display_name": "Hidden", "visibility": "hidden"},
			map[string]any{"slug": "gpt-plan", "display_name": "GPT Plan", "visibility": "list"},
		}})
	}))
	defer apiServer.Close()

	storePath := filepath.Join(t.TempDir(), "tokens", "auth.json")
	m, err := New(storePath,
		WithEndpoints(Endpoints{
			AuthorizeURL: authServer.URL + "/authorize", TokenURL: authServer.URL + "/token",
			JWKSURL: authServer.URL + "/jwks", RevokeURL: authServer.URL + "/revoke",
			APIBaseURL: apiServer.URL, Issuer: issuer,
		}),
		withClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := m.Start("http://127.0.0.1:8787/api/auth/chatgpt-plan/callback")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorizationURL)
	nonce = parsed.Query().Get("nonce")
	if parsed.Query().Get("client_id") != dynamicClientID || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("unexpected authorization params: %s", parsed.RawQuery)
	}
	status, err := m.Complete(context.Background(), url.Values{
		"code": {"code-1"}, "state": {parsed.Query().Get("state")}, "client_id": {"oaiapp_test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Connected || status.Email != "dev@example.com" {
		t.Fatalf("status = %#v", status)
	}
	if info, err := os.Stat(storePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token store permissions: info=%v err=%v", info, err)
	}
	if token, err := m.AccessToken(context.Background()); err != nil || token != "access-one" {
		t.Fatalf("first token = %q, %v", token, err)
	}
	now = now.Add(2 * time.Hour)
	if token, err := m.AccessToken(context.Background()); err != nil || token != "access-two" {
		t.Fatalf("refreshed token = %q, %v", token, err)
	}
	models, err := m.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].Slug != "gpt-plan" {
		t.Fatalf("models = %#v, %v", models, err)
	}
	if err := m.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if refreshSeen != "refresh-one" {
		t.Errorf("refresh token used = %q", refreshSeen)
	}
	if revocationSeen != "refresh-two:refresh_token" {
		t.Errorf("revocation = %q", revocationSeen)
	}
	if m.Status().Connected {
		t.Error("manager remained connected")
	}
	reauthURL, err := m.Start("http://127.0.0.1:8787/api/auth/chatgpt-plan/callback")
	if err != nil {
		t.Fatal(err)
	}
	reauth, _ := url.Parse(reauthURL)
	if reauth.Query().Get("client_id") != "oaiapp_test" {
		t.Errorf("returning client_id = %q", reauth.Query().Get("client_id"))
	}
	if reauth.Query().Get("agent_name_hint") != "" || reauth.Query().Get("id_token_hint") != "" {
		t.Errorf("returning sign-in retained initial-only/token hint params: %s", reauth.RawQuery)
	}
}

func TestStartRejectsNonLoopbackRedirect(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Start("http://localhost:8787/callback")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error = %v", err)
	}
}
