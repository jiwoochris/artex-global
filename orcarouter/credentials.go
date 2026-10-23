package orcarouter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Source records how a credential was obtained. Downstream code must not
// branch on it: both adapters produce the same durable OrcaRouter API key.
type Source string

const (
	SourceAPIKey Source = "api_key"
	SourcePKCE   Source = "pkce"
)

// Credential is the single result both authentication adapters return.
type Credential struct {
	Key        string
	Source     Source
	Scope      string
	Generation uint64
}

// CredentialAcquirer is the small seam described in the integration spec: the
// provider and the model catalog only ever see Credential.Key, while the
// paste-a-key path and the browser connect path are two implementations.
type CredentialAcquirer interface {
	Acquire(ctx context.Context) (Credential, error)
}

// APIKeyAdapter is the paste-an-existing-key path.
type APIKeyAdapter struct {
	Key string
	// Scope is the scope the caller intends to use. OrcaRouter keys are not
	// scoped at the wire level; this is recorded for audit only.
	Scope string
}

// Acquire validates obvious input mistakes only. The sk-orca- prefix is not
// proof that a key is valid; the first real request establishes that.
func (a APIKeyAdapter) Acquire(ctx context.Context) (Credential, error) {
	key := strings.TrimSpace(a.Key)
	if key == "" {
		return Credential{}, errors.New("orcarouter: API key is empty")
	}
	if strings.ContainsAny(key, "\r\n\t ") {
		return Credential{}, errors.New("orcarouter: API key contains whitespace")
	}
	scope := a.Scope
	if scope == "" {
		scope = ScopeAPI
	}
	return Credential{Key: key, Source: SourceAPIKey, Scope: scope}, nil
}

// PKCEAdapter runs the browser connect flow and persists the issued key via
// the host project's existing secret store before returning it.
type PKCEAdapter struct {
	Flow *Flow
	// Connector overrides Flow for tests.
	Connector Connector
	// Persist stores the issued key. The key is only handed to this callback
	// after the exchange succeeded; it is never logged.
	Persist func(ctx context.Context, key string) error
}

// Acquire performs Flow A (loopback redirect) and persists the durable key.
func (a PKCEAdapter) Acquire(ctx context.Context) (Credential, error) {
	var result ExchangeResult
	var err error
	switch {
	case a.Connector != nil:
		result, err = a.Connector.Connect(ctx)
	case a.Flow != nil:
		result, err = a.Flow.Connect(ctx)
	default:
		return Credential{}, errors.New("orcarouter: connect flow is not configured")
	}
	if err != nil {
		return Credential{}, err
	}
	if a.Persist != nil {
		if err := a.Persist(ctx, result.Key); err != nil {
			return Credential{}, fmt.Errorf("orcarouter: storing the issued key failed: %w", err)
		}
	}
	return Credential{Key: result.Key, Source: SourcePKCE, Scope: result.Scope}, nil
}

// ErrNeedsReauth marks a durable credential that OrcaRouter rejected. There is
// no refresh grant: the only recovery is a new connect flow.
var ErrNeedsReauth = errors.New("orcarouter: credential revoked; reconnect required")

// ErrRateLimited is returned for a 429 so callers can back off without
// discarding the credential.
var ErrRateLimited = errors.New("orcarouter: rate limited")

// ErrForbidden marks a 403 (scope/workspace denial), which is not a revoked
// credential and must not trigger a reconnect prompt.
var ErrForbidden = errors.New("orcarouter: access denied")

// ClassifyStatus maps an OrcaRouter/relay status to a terminal or transient
// error. Only 401 means the durable key itself must be replaced.
func ClassifyStatus(status int) error {
	switch {
	case status == 401:
		return ErrNeedsReauth
	case status == 403:
		return ErrForbidden
	case status == 429:
		return ErrRateLimited
	case status >= 500:
		return fmt.Errorf("orcarouter: upstream status %d", status)
	default:
		return fmt.Errorf("orcarouter: request failed with status %d", status)
	}
}

// Registry is the generation-safe in-process credential state. It exists so a
// late 401 from an old request can never mark a freshly reconnected key as
// broken, and so the app can show "needs reauthentication" without inventing a
// refresh grant.
type Registry struct {
	mu          sync.Mutex
	generation  uint64
	cred        *Credential
	needsReauth bool
}

// Set installs a credential and returns its generation. Installing a new
// credential always clears the needsReauth flag.
func (r *Registry) Set(c Credential) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	c.Generation = r.generation
	r.cred = &c
	r.needsReauth = false
	return c.Generation
}

// Current returns the active credential or ErrNeedsReauth.
func (r *Registry) Current() (Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.needsReauth {
		return Credential{}, ErrNeedsReauth
	}
	if r.cred == nil {
		return Credential{}, ErrNeedsReauth
	}
	return *r.cred, nil
}

// MarkUnauthorized marks the credential as revoked, but only when the rejected
// request used the generation that is still current. It never refreshes and
// never deletes the stored key.
func (r *Registry) MarkUnauthorized(generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cred == nil || r.cred.Generation != generation || r.generation != generation {
		return false
	}
	r.needsReauth = true
	return true
}

// NeedsReauth reports the current terminal state.
func (r *Registry) NeedsReauth() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.needsReauth
}

// MaskKey renders a credential for display without exposing it. Short or
// malformed values are fully masked.
func MaskKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 12 {
		return "••••"
	}
	return key[:8] + "…" + key[len(key)-4:]
}
