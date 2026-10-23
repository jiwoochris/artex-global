package orcarouter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Flow implements the OrcaRouter browser connect flow (Flow A, loopback
// redirect). Flow A is the right fit for this project because the ARTEX server
// is a self-hosted process that can bind an ephemeral 127.0.0.1 port on the
// same machine the browser runs on; the user approves once and the code is
// delivered automatically. S256 is always used, including for users who choose
// "show me a code" on the consent screen.
type Flow struct {
	Endpoints Endpoints
	// HTTP is used for the code exchange only.
	HTTP *http.Client
	// Listen binds the loopback callback listener. Overridable in tests.
	Listen func(network, addr string) (net.Listener, error)
	// Rand supplies verifier/state entropy. Overridable in tests.
	Rand io.Reader
	// AppName is displayed on the consent screen.
	AppName string
	// Timeout bounds how long a connect attempt waits for the browser.
	Timeout time.Duration
}

// NewFlow resolves the auth and API origins from the environment.
func NewFlow() (*Flow, error) {
	eps, err := ResolveEndpoints()
	if err != nil {
		return nil, err
	}
	return &Flow{
		Endpoints: eps,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Listen:    net.Listen,
		Rand:      rand.Reader,
		AppName:   AppName,
		Timeout:   10 * time.Minute,
	}, nil
}

// Attempt is one in-flight authorization. The verifier never leaves the
// process: it is only sent in the exchange body, never in a URL or log.
type Attempt struct {
	flow      *Flow
	verifier  string
	state     string
	Callback  string
	Authorize string
	listener  net.Listener
	server    *http.Server
	codeCh    chan string
	errCh     chan error
	done      chan struct{}
	closeOnce sync.Once
}

// Start binds the loopback listener first (so the port is known before the
// browser opens), then builds the authorize URL with a fresh S256 challenge.
func (f *Flow) Start(ctx context.Context) (*Attempt, error) {
	if f == nil {
		return nil, errors.New("orcarouter: connect flow is not configured")
	}
	verifier, err := randomToken(f.Rand, 32)
	if err != nil {
		return nil, fmt.Errorf("orcarouter: generating verifier: %w", err)
	}
	state, err := randomToken(f.Rand, 16)
	if err != nil {
		return nil, fmt.Errorf("orcarouter: generating state: %w", err)
	}
	ln, err := f.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("orcarouter: starting loopback listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	callback := fmt.Sprintf("http://127.0.0.1:%d/cb", port)

	challenge := codeChallenge(verifier)
	u, err := url.Parse(f.Endpoints.AuthBase + "/auth")
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("orcarouter: building authorize URL: %w", err)
	}
	q := u.Query()
	q.Set("callback_url", callback)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("app_name", f.AppName)
	q.Set("scope", ScopeAPI)
	u.RawQuery = q.Encode()

	a := &Attempt{
		flow:      f,
		verifier:  verifier,
		state:     state,
		Callback:  callback,
		Authorize: u.String(),
		listener:  ln,
		codeCh:    make(chan string, 1),
		errCh:     make(chan error, 1),
		done:      make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cb", a.handleCallback)
	a.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = a.server.Serve(ln)
	}()
	return a, nil
}

func (a *Attempt) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Compare state before looking at the code or error at all.
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 {
		http.Error(w, "State mismatch. Close this tab and retry the connection from ARTEX.", http.StatusBadRequest)
		a.fail(ErrStateMismatch)
		return
	}
	if e := q.Get("error"); e != "" {
		http.Error(w, "Authorization was not completed. You can close this tab.", http.StatusBadRequest)
		a.fail(ErrDenied)
		return
	}
	code := q.Get("code")
	if code == "" {
		http.Error(w, "Missing authorization code. Close this tab and retry.", http.StatusBadRequest)
		a.fail(ErrDenied)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><meta charset=\"utf-8\"><title>OrcaRouter</title>"+
		"<p>Connected to OrcaRouter. You can close this tab and return to ARTEX.</p>")
	select {
	case a.codeCh <- code:
	default:
	}
}

func (a *Attempt) fail(err error) {
	select {
	case a.errCh <- err:
	default:
	}
}

// Wait blocks until the browser delivers a code, denies, or the context
// deadline expires. The listener is always closed.
func (a *Attempt) Wait(ctx context.Context) (string, error) {
	defer a.Close()
	timeout := a.flow.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case code := <-a.codeCh:
		return code, nil
	case err := <-a.errCh:
		return "", err
	case <-timer.C:
		return "", ErrTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	case <-a.done:
		return "", ErrCanceled
	}
}

// Close stops serving the loopback callback.
func (a *Attempt) Close() {
	a.closeOnce.Do(func() {
		close(a.done)
		if a.server != nil {
			_ = a.server.Close()
		}
		if a.listener != nil {
			_ = a.listener.Close()
		}
	})
}

// VerifierForTest exposes the verifier to package tests only.
func (a *Attempt) verifierForTest() string { return a.verifier }

// StateForTest exposes the state to package tests only.
func (a *Attempt) stateForTest() string { return a.state }

// Sentinel errors for terminal authorization outcomes.
var (
	ErrDenied         = errors.New("orcarouter: authorization denied")
	ErrStateMismatch  = errors.New("orcarouter: authorization state mismatch")
	ErrTimeout        = errors.New("orcarouter: authorization timed out")
	ErrCanceled       = errors.New("orcarouter: authorization canceled")
	ErrCodeRejected   = errors.New("orcarouter: authorization code rejected or expired")
	ErrBadRequest     = errors.New("orcarouter: authorization request rejected")
	ErrScopeDowngrade = errors.New("orcarouter: granted scope does not permit inference")
)

// ExchangeResult is the successful code-for-key response.
type ExchangeResult struct {
	Key   string
	Scope string
	// UserID identifies the consenting account; it is not a secret.
	UserID string
}

// Connector is the one-shot connect seam used by the PKCE adapter. *Flow is
// the production implementation; tests inject a fake.
type Connector interface {
	Connect(ctx context.Context) (ExchangeResult, error)
}

// Connect runs a full loopback authorization: start, wait for the browser,
// then exchange the code at the auth origin.
func (f *Flow) Connect(ctx context.Context) (ExchangeResult, error) {
	a, err := f.Start(ctx)
	if err != nil {
		return ExchangeResult{}, err
	}
	defer a.Close()
	code, err := a.Wait(ctx)
	if err != nil {
		return ExchangeResult{}, err
	}
	return f.Exchange(ctx, code, a.verifier)
}

// Exchange redeems the auth code at the AUTH origin. The API origin is never
// used here: https://api.orcarouter.ai/v1/auth/keys is a 404 by design.
func (f *Flow) Exchange(ctx context.Context, code, verifier string) (ExchangeResult, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(verifier) == "" {
		return ExchangeResult{}, ErrCodeRejected
	}
	body, err := json.Marshal(map[string]string{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})
	if err != nil {
		return ExchangeResult{}, errors.New("orcarouter: encoding exchange request failed")
	}
	endpoint := f.Endpoints.AuthBase + "/api/v1/auth/keys"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return ExchangeResult{}, errors.New("orcarouter: building exchange request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors can embed the URL but never the verifier or code.
		return ExchangeResult{}, fmt.Errorf("orcarouter: code exchange failed: network error")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusBadRequest:
			return ExchangeResult{}, ErrBadRequest
		case http.StatusForbidden:
			return ExchangeResult{}, ErrCodeRejected
		case http.StatusTooManyRequests:
			return ExchangeResult{}, ErrRateLimited
		default:
			return ExchangeResult{}, ClassifyStatus(resp.StatusCode)
		}
	}
	var parsed struct {
		Key    string `json:"key"`
		Scope  string `json:"scope"`
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || strings.TrimSpace(parsed.Key) == "" {
		return ExchangeResult{}, errors.New("orcarouter: exchange response was not usable")
	}
	// The granted scope is authoritative. Do not trust the requested one.
	if parsed.Scope != "" && parsed.Scope != ScopeAPI {
		return ExchangeResult{}, ErrScopeDowngrade
	}
	return ExchangeResult{Key: parsed.Key, Scope: parsed.Scope, UserID: parsed.UserID}, nil
}

// randomToken returns unpadded base64url of n cryptographically random bytes.
func randomToken(r io.Reader, n int) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// codeChallenge is base64url(sha256(verifier)) without padding.
func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
