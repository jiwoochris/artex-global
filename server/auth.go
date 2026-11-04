package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	jwtTTL         = 7 * 24 * time.Hour
	keyChars       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// setupTokenEnv pins the first-run setup token to a known value (headless /
	// automated deployments). When unset a random token is generated at startup and
	// printed to the server console only.
	setupTokenEnv = "ARTEX_SETUP_TOKEN"
	// adminPasswordEnv initialises the admin password at startup without the /setup
	// screen (headless deployments). Ignored once a password is set.
	adminPasswordEnv = "ARTEX_ADMIN_PASSWORD"
	// minPasswordLen mirrors the /setup screen's client-side rule, enforced server side.
	minPasswordLen = 8
	// maxPasswordBytes is bcrypt's hard limit: GenerateFromPassword errors past 72
	// bytes, so reject it up front with a clear message instead of a 500.
	maxPasswordBytes = 72
	setupTokenLen    = 32
)

// User-facing copy that the auth endpoints return over HTTP. A failed login or
// password setup surfaces this text verbatim as a toast, and the credential error
// wording matches the login screen (web messages auth.login.errorCredential).
// "token" is a technical term and stays as-is.
const (
	authErrUnauthorized         = "Authentication is required"
	authErrTokenInvalid         = "The token is invalid or has expired"
	authErrPasswordAlreadySet   = "A password has already been set"
	authErrPasswordEmpty        = "Enter a password"
	authErrPasswordTooShort     = "The password must be at least 8 characters"
	authErrPasswordTooLong      = "The password must not exceed 72 bytes"
	authErrNewPasswordEmpty     = "Enter a new password"
	authErrPasswordHash         = "Failed to encrypt the password"
	authErrSaveFailedPrefix     = "Failed to save: "
	authErrTokenGen             = "Failed to generate a token"
	authErrBadRequest           = "The request format is invalid"
	authErrPasswordNotInit      = "The password has not been initialized. Set a password first"
	authErrCurrentPasswordWrong = "The current password is incorrect"
	authErrBadCredential        = "Incorrect username or password"
	authErrSetupTokenInvalid    = "The setup token is incorrect. Enter the setup token printed on the [auth] line of the server console log"
	authErrTooManyAttempts      = "Too many failed attempts. Try again later"
	// Shared message for a failed password read. These handlers must not treat "could
	// not read" as "not set": that would open the initialisation door during a
	// database outage, letting an unauthenticated request overwrite an admin
	// password that is already configured.
	authErrDataSourceUnavailable = "The data source is temporarily unavailable. Try again shortly"
)

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy := filepath.Join(dataDir, jwtKeyFilename); legacy != path {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if data, rerr := os.ReadFile(legacy); rerr == nil {
				if werr := os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf(logT("[auth] moved JWT key from %s to %s (out of the discoverable workspace)"), legacy, path)
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	key, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate jwt key: %w", err)
	}
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf(logT("[auth] wrote new JWT key to %s"), path)
	return []byte(key), nil
}

// randomToken returns n characters drawn from keyChars with crypto/rand.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	for i := range buf {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return "", err
		}
		buf[i] = keyChars[v.Int64()]
	}
	return string(buf), nil
}

// signJWT issues a 7-day HS256 token for user ARTEX.
func signJWT(key []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "ARTEX",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
func verifyJWT(tokenStr string, key []byte) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	return err == nil && t.Valid
}

// signingKey derives the HS256 key that signs and verifies tokens for the given
// stored password hash: HMAC-SHA256(jwt.key, hash). Binding the key to the hash
// means every password change — /api/auth/change-password, reset-password.sh, or
// any direct DB edit — invalidates all tokens issued before it, so a session that
// was obtained with a leaked or attacker-set password dies with that password.
// With no password set the raw file key is used; production never issues a token
// in that state (authInit stores the hash first), and DB-less tests keep working.
func (s *Server) signingKey(hash string) []byte {
	if hash == "" {
		return s.jwtKey
	}
	m := hmac.New(sha256.New, s.jwtKey)
	m.Write([]byte(hash))
	return m.Sum(nil)
}

// readPassHash fetches the stored bcrypt hash ("" when unset / no DB). A read
// error is returned as-is, never folded into "": GetSetting yields "" both for an
// unset key and for a failed read, and callers must not treat the latter as "no
// password yet".
func (s *Server) readPassHash() (string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", nil
	}
	h, _, err := s.m.pg.GetSetting(authPassKey)
	if err != nil {
		return "", err
	}
	return h, nil
}

// passHashCacheTTL bounds how long token verification trusts the in-memory hash
// before re-reading it from the DB, so a password reset done outside this process
// (reset-password.sh) revokes old sessions within this window without a restart,
// while steady-state requests cost at most one settings lookup per window.
var passHashCacheTTL = 2 * time.Second

// cachedPassHash returns the password hash, re-reading it from the DB once the
// cache is older than passHashCacheTTL.
func (s *Server) cachedPassHash() (string, error) {
	s.authMu.Lock()
	fresh := s.passHashLoaded && time.Since(s.passHashAt) < passHashCacheTTL
	h := s.passHash
	s.authMu.Unlock()
	if fresh {
		return h, nil
	}
	return s.reloadPassHash()
}

func (s *Server) setPassHash(h string) {
	s.authMu.Lock()
	s.passHash = h
	s.passHashLoaded = true
	s.passHashAt = time.Now()
	s.authMu.Unlock()
}

// reloadPassHash re-reads the hash from the DB (the password may have been changed
// outside this process by reset-password.sh) and refreshes the cache.
//
// On a read error the cache is left untouched, so a DB blip is never cached as
// "no password".
func (s *Server) reloadPassHash() (string, error) {
	h, err := s.readPassHash()
	if err != nil {
		return "", err
	}
	s.setPassHash(h)
	return h, nil
}

// verifyToken validates tok against the key derived from the current password
// hash. On failure it refreshes the cached hash once and retries, so an
// out-of-process password reset takes effect without a restart.
func (s *Server) verifyToken(tok string) bool {
	if tok == "" {
		return false
	}
	hash, err := s.cachedPassHash()
	if err != nil {
		return false
	}
	if verifyJWT(tok, s.signingKey(hash)) {
		return true
	}
	if fresh, err := s.reloadPassHash(); err == nil && fresh != hash {
		return verifyJWT(tok, s.signingKey(fresh))
	}
	return false
}

// bootstrapAuth runs once at startup. It caches the stored password hash and, when
// no password is set yet, either initialises it from ARTEX_ADMIN_PASSWORD (headless
// deployments) or issues the first-run setup token that /api/auth/init requires.
func (s *Server) bootstrapAuth() {
	if s.m == nil || s.m.pg == nil {
		return
	}
	hash, err := s.reloadPassHash()
	if err != nil {
		// Unknown whether a password exists: neither initialise from the env var
		// nor issue a setup token now. authStatus re-reads on every request.
		log.Printf(logT("[auth] could not read the password state; skipping first-run initialisation: %v"), err)
		return
	}
	if hash != "" {
		return
	}
	if pw := os.Getenv(adminPasswordEnv); pw != "" {
		if utf8.RuneCountInString(pw) < minPasswordLen {
			log.Printf(logT("[auth] ignoring %s because it is shorter than %d characters; initialise with the setup token instead"), adminPasswordEnv, minPasswordLen)
		} else if len(pw) > maxPasswordBytes {
			log.Printf(logT("[auth] ignoring %s because it exceeds %d bytes; initialise with the setup token instead"), adminPasswordEnv, maxPasswordBytes)
		} else if h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost); err != nil {
			log.Printf(logT("[auth] failed to hash %s: %v"), adminPasswordEnv, err)
		} else if inserted, err := s.m.pg.InsertSettingIfAbsent(authPassKey, string(h)); err != nil {
			// Same first-set-only guarantee as authInit: an upsert here would let a
			// second instance starting with a different value overwrite the password.
			log.Printf(logT("[auth] failed to store %s: %v"), adminPasswordEnv, err)
		} else if !inserted {
			// Another instance set the password between our read and this write.
			_, _ = s.reloadPassHash()
			return
		} else {
			s.setPassHash(string(h))
			log.Printf(logT("[auth] initialised the admin password from environment variable %s (username ARTEX)"), adminPasswordEnv)
			return
		}
	}
	s.currentSetupToken()
}

// currentSetupToken returns the setup token that gates /api/auth/init while no
// password is set, generating (and printing to the console) one on first use.
// ARTEX_SETUP_TOKEN, when set, is used verbatim and never logged.
func (s *Server) currentSetupToken() string {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.setupToken != "" {
		return s.setupToken
	}
	if v := strings.TrimSpace(os.Getenv(setupTokenEnv)); v != "" {
		s.setupToken = v
		log.Printf(logT("[auth] the admin password has not been set yet. The setup token is the value of environment variable %s (not printed to the log)"), setupTokenEnv)
		return s.setupToken
	}
	tok, err := randomToken(setupTokenLen)
	if err != nil {
		log.Printf(logT("[auth] failed to generate the setup token: %v"), err)
		return ""
	}
	s.setupToken = tok
	// Registered with the log sink before it is printed: the console line keeps the
	// token, while the copies served by /api/logs (ring, SSE, server_logs) are redacted.
	logSink.addSecret(tok)
	log.Printf(logT("[auth] the admin password has not been set yet. Setup token: %s"), tok)
	log.Print(logT("[auth] enter the value above in the \"Setup token\" field on the first screen (/setup), or send it as setup_token in the POST /api/auth/init body. This token is never sent over the network and is only visible in this console log"))
	return s.setupToken
}

// checkSetupToken compares the client-supplied token with the current one in
// constant time. An empty server-side token (generation failed) never matches.
func (s *Server) checkSetupToken(given string) bool {
	want := s.currentSetupToken()
	given = strings.TrimSpace(given)
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(given)) == 1
}

func (s *Server) clearSetupToken() {
	s.authMu.Lock()
	s.setupToken = ""
	s.authMu.Unlock()
}

// limiter returns the per-client failed-attempt limiter shared by login and init.
func (s *Server) limiter() *authLimiter {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.loginLimiter == nil {
		s.loginLimiter = newAuthLimiter()
	}
	return s.loginLimiter
}

// authLimiter throttles repeated authentication failures per client address: the
// first few failures are free, after that each further failure doubles a cooldown
// during which the client gets 429. A success clears the client's record.
type authLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*authAttempts
}

type authAttempts struct {
	fails int
	last  time.Time
	until time.Time
}

const (
	authFreeFailures    = 5
	authFailureWindow   = 10 * time.Minute
	authCooldownBase    = 30 * time.Second
	authCooldownMax     = 10 * time.Minute
	authLimiterMaxItems = 10000
)

func newAuthLimiter() *authLimiter {
	return &authLimiter{now: time.Now, clients: map[string]*authAttempts{}}
}

// allow reports whether the client may attempt authentication right now.
func (l *authLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.clients[key]
	if a == nil {
		return true
	}
	now := l.now()
	if now.Sub(a.last) > authFailureWindow {
		delete(l.clients, key)
		return true
	}
	return !now.Before(a.until)
}

// fail records a failed attempt and, past the free allowance, starts a cooldown.
func (l *authLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.clients[key]
	if a == nil || now.Sub(a.last) > authFailureWindow {
		if len(l.clients) >= authLimiterMaxItems {
			l.clients = map[string]*authAttempts{}
		}
		a = &authAttempts{}
		l.clients[key] = a
	}
	a.fails++
	a.last = now
	if extra := a.fails - authFreeFailures; extra >= 0 {
		cool := authCooldownBase << uint(extra)
		if cool > authCooldownMax || cool <= 0 {
			cool = authCooldownMax
		}
		a.until = now.Add(cool)
	}
}

func (l *authLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.clients, key)
	l.mu.Unlock()
}

// clientKey identifies the client for throttling and audit logs. The throttle key
// is the TCP peer only — X-Forwarded-For is client-controlled and would let an
// attacker dodge the limiter (or lock out others) by forging it. The forwarded
// value is still surfaced in the log line so an operator behind a reverse proxy
// can see the real origin.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clientLogID(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		return r.RemoteAddr + " (X-Forwarded-For: " + xff + ")"
	}
	return r.RemoteAddr
}

// isLoopbackAddr reports whether a listen address only accepts local connections.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	if host == "" { // ":8787" / "0.0.0.0:8787" style — every interface
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// WarnIfExposed logs a warning when the admin password is still unset and the
// server accepts connections from other hosts: the setup token blocks a remote
// takeover, but the port should still not be reachable before setup is done.
func (s *Server) WarnIfExposed(addr string) {
	if s.m == nil || s.m.pg == nil || isLoopbackAddr(addr) {
		return
	}
	if h, err := s.cachedPassHash(); err != nil || h != "" {
		return
	}
	log.Printf(logT("[auth] warning: the admin password has not been set yet and the server is bound to %q, so other hosts can connect. Initialisation still requires the setup token, but do not expose this port to external networks until initial setup is complete (default 127.0.0.1:8787)"), addr)
}

// extractToken reads the JWT from the Authorization: Bearer header, the
// artex_token cookie, or — only on the SSE routes — the ?token= query param.
// The query parameter exists because EventSource cannot set an Authorization
// header; everywhere else it is refused, because a token in the URL leaks into
// Referer headers, reverse-proxy logs and browser history. The header is read
// first on every route, so an explicit bearer token always wins.
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("artex_token"); err == nil && c.Value != "" {
		return c.Value
	}
	if isStreamPath(r.URL.Path) {
		return r.URL.Query().Get("token")
	}
	return ""
}

// isStreamPath reports whether p is one of the Server-Sent Events endpoints
// (/api/logs/stream, /api/update/stream, /api/exploration/activity/stream,
// /api/side-questions/{id}/events). They are the only routes that accept the JWT
// as a query parameter and the only ones exempt from the server write timeout.
func isStreamPath(p string) bool {
	return strings.HasSuffix(p, "/stream") || strings.HasSuffix(p, "/events")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, authErrUnauthorized)
			return
		}
		if !s.verifyToken(tok) {
			writeErr(w, 401, authErrTokenInvalid)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// While it is not, setup_token_required tells the /setup screen to ask for the
// console setup token (and makes sure one has been issued and printed).
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	// A read failure must be a 503, not initialized:false: the frontend sends the user
	// to /setup on initialized:false, so dressing a database outage up as a 200 would
	// push the user onto the overwrite path for the existing password.
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, authErrDataSourceUnavailable)
		return
	}
	s.setPassHash(hash)
	if hash == "" {
		s.currentSetupToken()
	}
	writeJSON(w, 200, map[string]any{"initialized": hash != "", "setup_token_required": hash == ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already
// set. The route is reachable without a session (nothing exists yet to log in
// with), so it is gated by the setup token printed on the server console: a
// remote client who merely reaches the port cannot claim the admin account.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, authErrTooManyAttempts)
		return
	}
	var req struct {
		Password   string `json:"password"`
		SetupToken string `json:"setup_token"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	// Serialise the check-then-set so two concurrent first-run requests cannot both
	// pass the "not yet set" check.
	s.initMu.Lock()
	defer s.initMu.Unlock()
	existing, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, authErrDataSourceUnavailable)
		return
	}
	s.setPassHash(existing)
	if existing != "" {
		writeErr(w, 403, authErrPasswordAlreadySet)
		return
	}
	if !s.checkSetupToken(req.SetupToken) {
		s.limiter().fail(key)
		log.Printf(logT("[auth] initial setup rejected: setup token mismatch (client=%s)"), clientLogID(r))
		writeErr(w, 403, authErrSetupTokenInvalid)
		return
	}
	if req.Password == "" {
		writeErr(w, 400, authErrPasswordEmpty)
		return
	}
	if utf8.RuneCountInString(req.Password) < minPasswordLen {
		writeErr(w, 400, authErrPasswordTooShort)
		return
	}
	if len(req.Password) > maxPasswordBytes {
		writeErr(w, 400, authErrPasswordTooLong)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, authErrPasswordHash)
		return
	}
	// INSERT ... ON CONFLICT DO NOTHING instead of an upsert: the GetSetting check above
	// is only a fast-fail path, and the actual "set exactly once" guarantee is the
	// primary key constraint. Another request can win while bcrypt is busy for tens of
	// milliseconds, and a failing read check would be powerless anyway.
	inserted, err := pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err != nil {
		writeErr(w, 500, trMsg(authErrSaveFailedPrefix)+err.Error())
		return
	}
	if !inserted {
		writeErr(w, 403, authErrPasswordAlreadySet)
		return
	}
	s.setPassHash(string(hash))
	s.clearSetupToken()
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(string(hash)))
	if err != nil {
		writeErr(w, 500, authErrTokenGen)
		return
	}
	log.Printf(logT("[auth] admin password initialised (client=%s)"), clientLogID(r))
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password. Every previously issued token
// stops working (the signing key is bound to the hash); the response carries a
// fresh token so the caller's own session continues.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !s.verifyToken(extractToken(r)) {
		writeErr(w, 401, authErrUnauthorized)
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, authErrTooManyAttempts)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, authErrNewPasswordEmpty)
		return
	}
	if utf8.RuneCountInString(req.NewPassword) < minPasswordLen {
		writeErr(w, 400, authErrPasswordTooShort)
		return
	}
	if len(req.NewPassword) > maxPasswordBytes {
		writeErr(w, 400, authErrPasswordTooLong)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, authErrDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, authErrPasswordNotInit)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		s.limiter().fail(key)
		log.Printf(logT("[auth] password change rejected: current password mismatch (client=%s)"), clientLogID(r))
		writeErr(w, 401, authErrCurrentPasswordWrong)
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, authErrPasswordHash)
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, trMsg(authErrSaveFailedPrefix)+err.Error())
		return
	}
	s.setPassHash(string(newHash))
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(string(newHash)))
	if err != nil {
		writeErr(w, 500, authErrTokenGen)
		return
	}
	log.Printf(logT("[auth] admin password changed; all existing sessions are invalidated (client=%s)"), clientLogID(r))
	writeJSON(w, 200, map[string]any{"ok": true, "token": tok})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, authErrTooManyAttempts)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, authErrBadRequest)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, authErrDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, authErrPasswordNotInit)
		return
	}
	// Always run the bcrypt comparison so a wrong username costs the same as a
	// wrong password (no username oracle, no timing shortcut).
	pwErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password))
	if req.Username != "ARTEX" || pwErr != nil {
		s.limiter().fail(key)
		log.Printf(logT("[auth] login failed (client=%s)"), clientLogID(r))
		writeErr(w, 401, authErrBadCredential)
		return
	}
	s.setPassHash(hash)
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(hash))
	if err != nil {
		writeErr(w, 500, authErrTokenGen)
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
