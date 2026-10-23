package orcarouter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// LoginState is the externally visible status of one connect attempt.
type LoginState struct {
	ID           string `json:"id"`
	State        string `json:"state"` // pending | success | error | canceled
	Error        string `json:"error,omitempty"`
	ProfileID    int64  `json:"profile_id,omitempty"`
	AuthorizeURL string `json:"authorize_url,omitempty"`
}

const (
	StatePending  = "pending"
	StateSuccess  = "success"
	StateError    = "error"
	StateCanceled = "canceled"
)

// KeySaver persists an issued key into the host's existing secret store.
type KeySaver func(ctx context.Context, profileID int64, key string) (int64, error)

// LoginManager owns in-flight browser authorizations. Each attempt has its own
// generation and only the newest attempt for a profile may persist a key, so a
// late callback from an abandoned attempt cannot overwrite a fresh credential.
type LoginManager struct {
	// FlowFactory builds a configured flow. Defaults to NewFlow.
	FlowFactory func() (*Flow, error)
	// Save persists the issued key. Required.
	Save KeySaver
	// Timeout bounds a single attempt. Defaults to 10 minutes.
	Timeout time.Duration

	mu       sync.Mutex
	attempts map[string]*loginAttempt
	latest   map[int64]string // profile id -> current attempt id
	gen      uint64
}

type loginAttempt struct {
	state      LoginState
	ended      bool
	attempt    *Attempt
	cancel     context.CancelFunc
	profileID  int64
	generation uint64
}

func NewLoginManager(save KeySaver) *LoginManager {
	return &LoginManager{Save: save, attempts: map[string]*loginAttempt{}, latest: map[int64]string{}}
}

func (m *LoginManager) flow() (*Flow, error) {
	if m.FlowFactory != nil {
		return m.FlowFactory()
	}
	return NewFlow()
}

// Start opens a loopback listener, returns the authorize URL, and waits in the
// background for the browser callback. The verifier never leaves the process
// and is never returned to the browser.
func (m *LoginManager) Start(ctx context.Context, profileID int64) (LoginState, error) {
	if m.Save == nil {
		return LoginState{}, errors.New("orcarouter: no key store configured")
	}
	f, err := m.flow()
	if err != nil {
		return LoginState{}, err
	}
	a, err := f.Start(ctx)
	if err != nil {
		return LoginState{}, err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		a.Close()
		return LoginState{}, errors.New("orcarouter: generating attempt id failed")
	}
	id := hex.EncodeToString(idBytes)

	m.mu.Lock()
	if m.attempts == nil {
		m.attempts = map[string]*loginAttempt{}
	}
	if m.latest == nil {
		m.latest = map[int64]string{}
	}
	m.gen++
	gen := m.gen
	if profileID > 0 {
		// Invalidate any earlier attempt for the same profile.
		m.latest[profileID] = id
	}
	m.attempts[id] = &loginAttempt{
		state:      LoginState{ID: id, State: StatePending, ProfileID: profileID, AuthorizeURL: a.Authorize},
		attempt:    a,
		profileID:  profileID,
		generation: gen,
	}
	m.pruneLocked()
	m.mu.Unlock()

	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	m.mu.Lock()
	if at := m.attempts[id]; at != nil {
		at.cancel = cancel
	}
	m.mu.Unlock()
	go func() {
		defer cancel()
		m.run(runCtx, f, a, id, profileID, gen)
	}()
	return LoginState{ID: id, State: StatePending, ProfileID: profileID, AuthorizeURL: a.Authorize}, nil
}

func (m *LoginManager) run(ctx context.Context, f *Flow, a *Attempt, id string, profileID int64, gen uint64) {
	defer func() {
		if r := recover(); r != nil {
			m.finish(id, StateError, "connect flow failed", profileID, gen)
		}
	}()
	code, err := a.Wait(ctx)
	if err != nil {
		if errors.Is(err, ErrCanceled) || errors.Is(err, context.Canceled) {
			m.finish(id, StateCanceled, "", profileID, gen)
			return
		}
		if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			m.finish(id, StateError, "authorization timed out", profileID, gen)
			return
		}
		m.finish(id, StateError, safeError(err), profileID, gen)
		return
	}
	result, err := f.Exchange(ctx, code, a.verifier)
	if err != nil {
		m.finish(id, StateError, safeError(err), profileID, gen)
		return
	}
	if !m.stillCurrent(id, profileID, gen) {
		return
	}
	savedID, err := m.Save(ctx, profileID, result.Key)
	if err != nil {
		m.finish(id, StateError, "the issued key could not be stored", profileID, gen)
		return
	}
	m.mu.Lock()
	at := m.attempts[id]
	if at != nil && !at.ended {
		at.ended = true
		at.state.State = StateSuccess
		at.state.ProfileID = savedID
	}
	m.mu.Unlock()
}

func (m *LoginManager) stillCurrent(id string, profileID int64, gen uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	at := m.attempts[id]
	if at == nil || at.ended || at.generation != gen {
		return false
	}
	if profileID > 0 && m.latest[profileID] != id {
		return false
	}
	return true
}

func (m *LoginManager) finish(id, state, msg string, profileID int64, gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at := m.attempts[id]
	if at == nil || at.generation != gen || at.ended {
		return
	}
	at.ended = true
	at.state.State = state
	at.state.Error = msg
	if at.cancel != nil {
		at.cancel()
	}
}

// Status returns the current status of an attempt.
func (m *LoginManager) Status(id string) (LoginState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at := m.attempts[id]
	if at == nil {
		return LoginState{}, false
	}
	return at.state, true
}

// Cancel aborts an attempt. Safe to call for completed or unknown ids.
func (m *LoginManager) Cancel(id string) {
	m.mu.Lock()
	at := m.attempts[id]
	m.mu.Unlock()
	if at == nil {
		return
	}
	if at.attempt != nil {
		at.attempt.Close()
	}
	if at.cancel != nil {
		at.cancel()
	}
	m.mu.Lock()
	if at.state.State == StatePending {
		at.state.State = StateCanceled
		at.ended = true
	}
	m.mu.Unlock()
}

// pruneLocked drops finished attempts older than the timeout window.
func (m *LoginManager) pruneLocked() {
	if len(m.attempts) < 64 {
		return
	}
	for id, at := range m.attempts {
		if at.ended {
			delete(m.attempts, id)
		}
	}
}

// safeError keeps terminal messages actionable without echoing anything that
// could carry a credential or the verifier.
func safeError(err error) string {
	switch {
	case errors.Is(err, ErrDenied):
		return "authorization was denied"
	case errors.Is(err, ErrStateMismatch):
		return "authorization state mismatch; start again"
	case errors.Is(err, ErrCodeRejected):
		return "authorization code was rejected or expired; start again"
	case errors.Is(err, ErrBadRequest):
		return "authorization request was rejected"
	case errors.Is(err, ErrScopeDowngrade):
		return "granted scope does not permit inference"
	case errors.Is(err, ErrRateLimited):
		return "too many authorization attempts; try again later"
	case errors.Is(err, ErrForbidden):
		return "access denied"
	default:
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "authorization failed"
		}
		return msg
	}
}
