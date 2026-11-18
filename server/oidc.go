package server

// Single sign-on through the platform OIDC provider (Ory Hydra at sso.noraneko.cc).
// This deployment signs in only this way: the password routes (/api/auth/init, /login,
// /change-password) are not registered. A successful callback issues the same HS256
// session token the rest of the server already checks, so requireAuth is unchanged.
//
// Configuration (all required; without it every sign-in attempt fails closed):
//   ARTEX_OIDC_ISSUER          e.g. https://sso.noraneko.cc/
//   ARTEX_OIDC_CLIENT_ID
//   ARTEX_OIDC_CLIENT_SECRET
//   ARTEX_OIDC_REDIRECT_URL    e.g. https://artex.noraneko.cc/api/auth/oidc/callback
//   ARTEX_OIDC_ALLOWED_EMAILS  comma-separated; only these verified emails may sign in

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	oidcCookie    = "artex_oidc"
	oidcCookieTTL = 10 * time.Minute
	oidcTimeout   = 10 * time.Second
)

type oidcConfig struct {
	issuer, clientID, clientSecret, redirectURL string
	allowed                                     map[string]bool
}

func oidcConfigFromEnv() (oidcConfig, error) {
	c := oidcConfig{
		issuer:       strings.TrimSpace(os.Getenv("ARTEX_OIDC_ISSUER")),
		clientID:     strings.TrimSpace(os.Getenv("ARTEX_OIDC_CLIENT_ID")),
		clientSecret: strings.TrimSpace(os.Getenv("ARTEX_OIDC_CLIENT_SECRET")),
		redirectURL:  strings.TrimSpace(os.Getenv("ARTEX_OIDC_REDIRECT_URL")),
		allowed:      map[string]bool{},
	}
	for _, e := range strings.Split(os.Getenv("ARTEX_OIDC_ALLOWED_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			c.allowed[e] = true
		}
	}
	if c.issuer == "" || c.clientID == "" || c.clientSecret == "" || c.redirectURL == "" || len(c.allowed) == 0 {
		return c, errors.New("ARTEX_OIDC_ISSUER, _CLIENT_ID, _CLIENT_SECRET, _REDIRECT_URL and _ALLOWED_EMAILS must all be set")
	}
	return c, nil
}

// oidcClient is discovered lazily and cached, so the server starts even while the
// provider is down; a failed discovery is retried on the next sign-in.
type oidcClient struct {
	cfg      oidcConfig
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

var (
	oidcMu     sync.Mutex
	oidcCached *oidcClient
)

func getOIDCClient(ctx context.Context) (*oidcClient, error) {
	oidcMu.Lock()
	defer oidcMu.Unlock()
	cfg, err := oidcConfigFromEnv()
	if err != nil {
		return nil, err
	}
	if oidcCached != nil && oidcCached.cfg.issuer == cfg.issuer && oidcCached.cfg.clientID == cfg.clientID {
		oidcCached.cfg = cfg // allowlist and secret may change without a new discovery
		oidcCached.oauth.ClientSecret = cfg.clientSecret
		return oidcCached, nil
	}
	p, err := oidc.NewProvider(ctx, cfg.issuer)
	if err != nil {
		return nil, err
	}
	oidcCached = &oidcClient{
		cfg: cfg,
		oauth: oauth2.Config{
			ClientID:     cfg.clientID,
			ClientSecret: cfg.clientSecret,
			RedirectURL:  cfg.redirectURL,
			Endpoint:     p.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.clientID}),
	}
	return oidcCached, nil
}

// oidcFlow is the per-attempt state kept in a short-lived HttpOnly cookie, signed
// with the server's JWT key so a client cannot forge or swap it.
type oidcFlow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Expires  int64  `json:"e"`
}

func (s *Server) oidcSign(payload string) string {
	m := hmac.New(sha256.New, s.jwtKey)
	m.Write([]byte("oidc-flow:" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) encodeFlow(f oidcFlow) (string, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + s.oidcSign(p), nil
}

func (s *Server) decodeFlow(v string) (oidcFlow, bool) {
	var f oidcFlow
	p, sig, ok := strings.Cut(v, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.oidcSign(p))) {
		return f, false
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil || json.Unmarshal(b, &f) != nil || time.Now().Unix() > f.Expires {
		return f, false
	}
	return f, true
}

func setFlowCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcCookie, Value: value, Path: "/api/auth/oidc", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// oidcFail sends the browser back to the login page with a short reason code the
// page turns into a message. Details go to the server log only.
func oidcFail(w http.ResponseWriter, r *http.Request, code string, err error) {
	log.Printf("[auth] sso sign-in failed: %s: %v (client=%s)", code, err, clientLogID(r))
	setFlowCookie(w, "", -1)
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(code), http.StatusFound)
}

// GET /api/auth/oidc/start — redirects to the provider with state, nonce and PKCE.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
	defer cancel()
	c, err := getOIDCClient(ctx)
	if err != nil {
		oidcFail(w, r, "unavailable", err)
		return
	}
	state, err1 := randomToken(32)
	nonce, err2 := randomToken(32)
	if err1 != nil || err2 != nil {
		oidcFail(w, r, "unavailable", errors.Join(err1, err2))
		return
	}
	f := oidcFlow{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(oidcCookieTTL).Unix()}
	v, err := s.encodeFlow(f)
	if err != nil {
		oidcFail(w, r, "unavailable", err)
		return
	}
	setFlowCookie(w, v, int(oidcCookieTTL.Seconds()))
	http.Redirect(w, r, c.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(f.Verifier)), http.StatusFound)
}

// GET /api/auth/oidc/callback — checks state, exchanges the code, verifies the ID
// token and the email allowlist, then hands the session token to the login page in
// the URL fragment (never sent to a server, so it stays out of access logs).
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
	defer cancel()
	ck, err := r.Cookie(oidcCookie)
	if err != nil {
		oidcFail(w, r, "state", err)
		return
	}
	f, ok := s.decodeFlow(ck.Value)
	q := r.URL.Query()
	if !ok || q.Get("state") == "" || !hmac.Equal([]byte(q.Get("state")), []byte(f.State)) {
		oidcFail(w, r, "state", errors.New("state cookie missing, expired or mismatched"))
		return
	}
	if e := q.Get("error"); e != "" {
		oidcFail(w, r, "denied", errors.New(e))
		return
	}
	c, err := getOIDCClient(ctx)
	if err != nil {
		oidcFail(w, r, "unavailable", err)
		return
	}
	tok, err := c.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(f.Verifier))
	if err != nil {
		oidcFail(w, r, "exchange", err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		oidcFail(w, r, "token", err)
		return
	}
	if !hmac.Equal([]byte(idt.Nonce), []byte(f.Nonce)) {
		oidcFail(w, r, "token", errors.New("nonce mismatch"))
		return
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
	}
	if err := idt.Claims(&claims); err != nil {
		oidcFail(w, r, "token", err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" || (claims.EmailVerified != nil && !*claims.EmailVerified) || !c.cfg.allowed[email] {
		oidcFail(w, r, "forbidden", errors.New("email not allowed or not verified: "+email))
		return
	}
	hash, err := s.cachedPassHash()
	if err != nil {
		oidcFail(w, r, "unavailable", err)
		return
	}
	session, err := signJWT(s.signingKey(hash))
	if err != nil {
		oidcFail(w, r, "unavailable", err)
		return
	}
	setFlowCookie(w, "", -1)
	log.Printf("[auth] sso sign-in: %s (client=%s)", email, clientLogID(r))
	http.Redirect(w, r, "/login#token="+url.QueryEscape(session), http.StatusFound)
}
