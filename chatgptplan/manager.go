// Package chatgptplan implements OpenAI's "Sign in with ChatGPT" flow for
// local, open-source applications. It owns OAuth state and token persistence;
// callers only receive short-lived access tokens through AccessToken.
package chatgptplan

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	dynamicClientID = "dynamic_agent_client"
	resourceURL     = "https://api.openai.com/v1"
	requiredScope   = "chatgpt.tokens.use.direct"
	scopes          = "openid profile email offline_access resource.invoke " + requiredScope
)

// Endpoints is configurable so the protocol can be tested without contacting
// OpenAI. Production callers should use DefaultEndpoints.
type Endpoints struct {
	AuthorizeURL string
	TokenURL     string
	JWKSURL      string
	RevokeURL    string
	APIBaseURL   string
	Issuer       string
}

var DefaultEndpoints = Endpoints{
	AuthorizeURL: "https://auth.openai.com/api/accounts/authorize",
	TokenURL:     "https://auth.openai.com/api/accounts/oauth/token",
	JWKSURL:      "https://auth.openai.com/.well-known/jwks.json",
	RevokeURL:    "https://auth.openai.com/api/accounts/oauth/revoke",
	APIBaseURL:   resourceURL,
	Issuer:       "https://auth.openai.com",
}

type Option func(*Manager)

func WithEndpoints(e Endpoints) Option { return func(m *Manager) { m.endpoints = e } }
func WithHTTPClient(c *http.Client) Option {
	return func(m *Manager) {
		if c != nil {
			m.client = c
		}
	}
}
func withClock(now func() time.Time) Option { return func(m *Manager) { m.now = now } }

type Manager struct {
	mu        sync.Mutex
	storePath string
	client    *http.Client
	endpoints Endpoints
	now       func() time.Time
	stored    store
	pending   map[string]authorizationAttempt
}

type store struct {
	Version int      `json:"version"`
	HostID  string   `json:"host_id"`
	Account *account `json:"account,omitempty"`
}

type account struct {
	Subject      string    `json:"subject"`
	Email        string    `json:"email,omitempty"`
	ClientID     string    `json:"client_id"`
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type authorizationAttempt struct {
	Verifier    string
	Nonce       string
	RedirectURI string
	ClientID    string
	CreatedAt   time.Time
}

// Status is safe to return to the browser; it never contains credentials.
type Status struct {
	Connected bool      `json:"connected"`
	Email     string    `json:"email,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type Model struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
}

func New(storePath string, opts ...Option) (*Manager, error) {
	m := &Manager{
		storePath: strings.TrimSpace(storePath),
		client:    &http.Client{Timeout: 30 * time.Second},
		endpoints: DefaultEndpoints,
		now:       time.Now,
		pending:   make(map[string]authorizationAttempt),
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.storePath == "" {
		return nil, errors.New("chatgpt plan: token store path is empty")
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	if m.stored.HostID == "" {
		m.stored = store{Version: 1, HostID: "urn:uuid:" + uuid.NewString()}
		if err := m.saveLocked(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("chatgpt plan: read token store: %w", err)
	}
	if err := os.Chmod(m.storePath, 0o600); err != nil {
		return fmt.Errorf("chatgpt plan: protect token store: %w", err)
	}
	if err := json.Unmarshal(b, &m.stored); err != nil {
		return fmt.Errorf("chatgpt plan: parse token store: %w", err)
	}
	return nil
}

func (m *Manager) saveLocked() error {
	dir := filepath.Dir(m.storePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("chatgpt plan: create token directory: %w", err)
	}
	b, err := json.MarshalIndent(m.stored, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".chatgpt-auth-*")
	if err != nil {
		return fmt.Errorf("chatgpt plan: create token store: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, m.storePath); err != nil {
		return fmt.Errorf("chatgpt plan: replace token store: %w", err)
	}
	ok = true
	return nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Start creates a short-lived PKCE authorization attempt. redirectURI must be
// an IPv4 loopback URL as required by OpenAI's local-app profile.
func (m *Manager) Start(redirectURI string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" {
		return "", errors.New("chatgpt plan: redirect URI must be http://127.0.0.1:<port>/...")
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return "", err
	}
	nonce, err := randomURLSafe(32)
	if err != nil {
		return "", err
	}
	verifier, err := randomURLSafe(64)
	if err != nil {
		return "", err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))

	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.now().Add(-10 * time.Minute)
	for key, attempt := range m.pending {
		if attempt.CreatedAt.Before(cutoff) {
			delete(m.pending, key)
		}
	}
	clientID := dynamicClientID
	if m.stored.Account != nil && m.stored.Account.ClientID != "" {
		clientID = m.stored.Account.ClientID
	}
	m.pending[state] = authorizationAttempt{Verifier: verifier, Nonce: nonce, RedirectURI: redirectURI, ClientID: clientID, CreatedAt: m.now()}

	q := url.Values{
		"client_id":             {clientID},
		"ext_agent_host_id":     {m.stored.HostID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {scopes},
		"resource":              {resourceURL},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challengeBytes[:])},
		"code_challenge_method": {"S256"},
	}
	if clientID == dynamicClientID {
		q.Set("agent_name_hint", "ARTEX")
	} else if m.stored.Account != nil {
		if m.stored.Account.IDToken != "" {
			q.Set("id_token_hint", m.stored.Account.IDToken)
		}
		if m.stored.Account.Email != "" {
			q.Set("login_hint", m.stored.Account.Email)
		}
	}
	authURL, err := url.Parse(m.endpoints.AuthorizeURL)
	if err != nil {
		delete(m.pending, state)
		return "", err
	}
	authURL.RawQuery = q.Encode()
	return authURL.String(), nil
}

type tokenResponse struct {
	AccessToken       string `json:"access_token"`
	RefreshToken      string `json:"refresh_token"`
	IDToken           string `json:"id_token"`
	TokenType         string `json:"token_type"`
	Scope             string `json:"scope"`
	ExpiresIn         int64  `json:"expires_in"`
	EarliestRefreshAt int64  `json:"earliest_refresh_at"`
}

func (m *Manager) exchange(ctx context.Context, values url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoints.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("chatgpt plan: token request: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("chatgpt plan: token endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out tokenResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("chatgpt plan: parse token response: %w", err)
	}
	if out.AccessToken == "" {
		return out, errors.New("chatgpt plan: token response did not include access_token")
	}
	return out, nil
}

// Complete verifies state, exchanges the code and validates the signed ID token.
func (m *Manager) Complete(ctx context.Context, query url.Values) (Status, error) {
	state := query.Get("state")
	m.mu.Lock()
	attempt, ok := m.pending[state]
	if ok {
		delete(m.pending, state)
	}
	m.mu.Unlock()
	if !ok || state == "" {
		return Status{}, errors.New("chatgpt plan: invalid or expired OAuth state")
	}
	if oauthErr := strings.TrimSpace(query.Get("error")); oauthErr != "" {
		return Status{}, fmt.Errorf("chatgpt plan: authorization failed: %s", oauthErr)
	}
	if m.now().Sub(attempt.CreatedAt) > 10*time.Minute {
		return Status{}, errors.New("chatgpt plan: authorization attempt expired")
	}
	code := strings.TrimSpace(query.Get("code"))
	callbackClientID := strings.TrimSpace(query.Get("client_id"))
	if attempt.ClientID != dynamicClientID && callbackClientID != "" && callbackClientID != attempt.ClientID {
		return Status{}, errors.New("chatgpt plan: callback client_id does not match the selected registration")
	}
	clientID := callbackClientID
	if clientID == "" {
		clientID = attempt.ClientID
	}
	if code == "" || clientID == "" || clientID == dynamicClientID {
		return Status{}, errors.New("chatgpt plan: callback is missing the code or issued client_id")
	}
	tokens, err := m.exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"code_verifier": {attempt.Verifier},
		"redirect_uri":  {attempt.RedirectURI},
		"resource":      {resourceURL},
	})
	if err != nil {
		return Status{}, err
	}
	claims, err := m.validateIDToken(ctx, tokens.IDToken, clientID, attempt.Nonce)
	if err != nil {
		return Status{}, err
	}
	subject, _ := claims["sub"].(string)
	if subject == "" {
		return Status{}, errors.New("chatgpt plan: ID token is missing sub")
	}
	m.mu.Lock()
	previous := m.stored.Account
	m.mu.Unlock()
	if previous != nil && previous.Subject != "" && previous.Subject != subject {
		return Status{}, errors.New("chatgpt plan: returning account identity does not match the connected account")
	}
	email, _ := claims["email"].(string)
	if !hasScope(tokens.Scope, requiredScope) {
		return Status{}, fmt.Errorf("chatgpt plan: token is missing required scope %q", requiredScope)
	}
	if tokens.RefreshToken == "" {
		return Status{}, errors.New("chatgpt plan: token response did not include refresh_token")
	}
	expires := m.now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if tokens.ExpiresIn <= 0 {
		expires = m.now().Add(time.Hour)
	}
	m.mu.Lock()
	m.stored.Account = &account{
		Subject: subject, Email: email, ClientID: clientID, IDToken: tokens.IDToken,
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		TokenType: tokens.TokenType, Scope: tokens.Scope, ExpiresAt: expires,
	}
	err = m.saveLocked()
	status := m.statusLocked()
	m.mu.Unlock()
	return status, err
}

func hasScope(got, want string) bool {
	for _, scope := range strings.Fields(got) {
		if scope == want {
			return true
		}
	}
	return false
}

func (m *Manager) statusLocked() Status {
	a := m.stored.Account
	if a == nil || a.RefreshToken == "" || !hasScope(a.Scope, requiredScope) {
		return Status{}
	}
	return Status{Connected: true, Email: a.Email, ExpiresAt: a.ExpiresAt}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

// AccessToken returns a valid access token, serializing refreshes so a rotating
// refresh token can never be consumed concurrently.
func (m *Manager) AccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.stored.Account
	if a == nil || a.RefreshToken == "" || !hasScope(a.Scope, requiredScope) {
		return "", errors.New("chatgpt plan: no connected ChatGPT account")
	}
	if a.AccessToken != "" && m.now().Add(2*time.Minute).Before(a.ExpiresAt) {
		return a.AccessToken, nil
	}
	tokens, err := m.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {a.ClientID},
		"refresh_token": {a.RefreshToken},
		"resource":      {resourceURL},
	})
	if err != nil {
		return "", err
	}
	if tokens.RefreshToken != "" {
		a.RefreshToken = tokens.RefreshToken
	}
	if tokens.Scope != "" {
		a.Scope = tokens.Scope
	}
	if !hasScope(a.Scope, requiredScope) {
		return "", fmt.Errorf("chatgpt plan: refreshed token is missing required scope %q", requiredScope)
	}
	a.AccessToken = tokens.AccessToken
	a.TokenType = tokens.TokenType
	a.ExpiresAt = m.now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if tokens.ExpiresIn <= 0 {
		a.ExpiresAt = m.now().Add(time.Hour)
	}
	if err := m.saveLocked(); err != nil {
		return "", err
	}
	return a.AccessToken, nil
}

// Models lists models visible to the signed-in ChatGPT account.
func (m *Manager) Models(ctx context.Context) ([]Model, error) {
	token, err := m.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.endpoints.APIBaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chatgpt plan: models endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var payload struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(payload.Models))
	for _, model := range payload.Models {
		if model.Slug != "" && (model.Visibility == "" || model.Visibility == "list") {
			models = append(models, Model{Slug: model.Slug, DisplayName: model.DisplayName})
		}
	}
	return models, nil
}

// Disconnect best-effort revokes the refresh token, then removes all account
// credentials locally while preserving the stable host identifier.
func (m *Manager) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var revokeErr error
	if a := m.stored.Account; a != nil && a.RefreshToken != "" && m.endpoints.RevokeURL != "" {
		values := url.Values{"token": {a.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {a.ClientID}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoints.RevokeURL, strings.NewReader(values.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if resp, doErr := m.client.Do(req); doErr != nil {
				revokeErr = doErr
			} else {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				_ = resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					revokeErr = fmt.Errorf("revocation returned HTTP %d", resp.StatusCode)
				}
			}
		}
	}
	if m.stored.Account != nil {
		// Keep the verified account/client registration so a later sign-in reuses
		// the issued client ID. The renewable session credentials are all cleared,
		// and the old ID token is deliberately not retained as a login hint.
		m.stored.Account.IDToken = ""
		m.stored.Account.AccessToken = ""
		m.stored.Account.RefreshToken = ""
		m.stored.Account.TokenType = ""
		m.stored.Account.Scope = ""
		m.stored.Account.ExpiresAt = time.Time{}
	}
	if err := m.saveLocked(); err != nil {
		return err
	}
	return revokeErr
}

func (m *Manager) validateIDToken(ctx context.Context, raw, audience, nonce string) (jwt.MapClaims, error) {
	if raw == "" {
		return nil, errors.New("chatgpt plan: token response did not include id_token")
	}
	keys, err := m.fetchJWKS(ctx)
	if err != nil {
		return nil, err
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		key := keys[kid]
		if key == nil {
			return nil, fmt.Errorf("chatgpt plan: signing key %q not found", kid)
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(m.endpoints.Issuer), jwt.WithAudience(audience), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("chatgpt plan: validate ID token: %w", err)
	}
	gotNonce, _ := claims["nonce"].(string)
	if gotNonce == "" || gotNonce != nonce {
		return nil, errors.New("chatgpt plan: ID token nonce mismatch")
	}
	return claims, nil
}

func (m *Manager) fetchJWKS(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoints.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chatgpt plan: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chatgpt plan: JWKS endpoint returned HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, err
	}
	out := make(map[string]*rsa.PublicKey)
	for _, jwk := range set.Keys {
		if jwk.Kty != "RSA" || jwk.Kid == "" || (jwk.Alg != "" && jwk.Alg != "RS256") {
			continue
		}
		nBytes, nErr := base64.RawURLEncoding.DecodeString(jwk.N)
		eBytes, eErr := base64.RawURLEncoding.DecodeString(jwk.E)
		if nErr != nil || eErr != nil || len(eBytes) == 0 || len(eBytes) > 4 {
			continue
		}
		exponent := 0
		for _, b := range eBytes {
			exponent = exponent<<8 | int(b)
		}
		if exponent > 0 {
			out[jwk.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: exponent}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("chatgpt plan: JWKS did not contain a usable RSA signing key")
	}
	return out, nil
}
