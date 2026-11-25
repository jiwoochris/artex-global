package agent

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/internal/oob"
)

// Adapter between the internal/oob session and the oobSession seam:
//   - interactor: address minting (stateless), stubbable in orchestration tests;
//   - liveSession: full session = interactor + polling pump (StartPolling
//     accumulates callbacks, Poll drains them).
//
// The private key stays inside *oob.Session (memory); nothing here serializes it.

// urlClient is the minimal surface of *oob.Session that interactor needs.
type urlClient interface {
	URL() string
	Close() error
}

type pollingClient interface {
	StopPolling() error
}

// interactor adapts an OOB client into address minting.
type interactor struct {
	client urlClient
}

// Generate returns (probeID, canonicalHost) for one fresh callback address.
// The official client's URL() mints a unique nonce-prefixed hostname per call;
// probeID is the first DNS label — the full nonce mapping lives in the
// hostname itself.
func (it *interactor) Generate() (string, string, error) {
	if it == nil || it.client == nil {
		return "", "", fmt.Errorf("oob: 会话未初始化")
	}
	host := it.client.URL()
	if host == "" {
		return "", "", fmt.Errorf("oob: 无法生成回调地址，会话可能已关闭")
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	id := host
	if i := strings.Index(host, "."); i > 0 {
		id = host[:i]
	}
	return id, host, nil
}

func (it *interactor) Close() error {
	if it == nil || it.client == nil {
		return nil
	}
	return it.client.Close()
}

func (it *interactor) StopPolling() error {
	if it == nil || it.client == nil {
		return nil
	}
	if p, ok := it.client.(pollingClient); ok {
		return p.StopPolling()
	}
	return nil
}

// liveSession is the full per-run OOB session: address minting plus a polling
// pump. Interactions arrive via StartPolling's callback, accumulate in a
// buffer, and Poll drains them (bounded wait — the official client polls on
// its own interval).
type liveSession struct {
	it     interactor
	mu     sync.Mutex
	buf    []OOBInteraction
	seen   map[string]struct{}
	closed bool
	health func() (int64, error, time.Time, time.Time)
	// serverHost is the callback hostname without port; it lets the probe label
	// be found even when the target prefixed it (data.<probe>.<server>).
	serverHost string
}

// pollSource is the subset of *oob.Session the live pump needs; tests inject
// controlled fakes here so dedup/lifecycle behavior is verifiable offline.
type pollSource interface {
	urlClient
	StartPolling(interval time.Duration, cb func(oob.Interaction)) error
	Health() (int64, error, time.Time, time.Time)
}

// hostSource exposes the callback server hostname (no port).
type hostSource interface {
	Host() string
}

// newLiveSession wires a real *oob.Session into the seam and starts the
// polling pump. Caller owns shutdown via Close (StopPolling → Close).
func newLiveSession(s *oob.Session, pollInterval time.Duration) (*liveSession, error) {
	if s == nil {
		return nil, fmt.Errorf("oob: 会话未初始化")
	}
	return newLiveSessionFrom(s, s, pollInterval)
}

// newLiveSessionFrom builds a live session from any pollSource (real client or
// test double). Interactions are deduplicated by event identity (protocol +
// full callback ID + timestamp + remote) — Interactsh may redeliver an event,
// and the same event must never be attributed twice to a probe or task.
func newLiveSessionFrom(mint urlClient, src pollSource, pollInterval time.Duration) (*liveSession, error) {
	if src == nil || mint == nil {
		return nil, fmt.Errorf("oob: 会话未初始化")
	}
	ls := &liveSession{it: interactor{client: mint}, seen: make(map[string]struct{})}
	if hs, ok := src.(hostSource); ok {
		ls.serverHost = hs.Host()
	}
	err := src.StartPolling(pollInterval, func(in oob.Interaction) {
		key := interactionKey(in)
		ls.mu.Lock()
		if _, dup := ls.seen[key]; dup {
			ls.mu.Unlock()
			return
		}
		ls.seen[key] = struct{}{}
		ls.buf = append(ls.buf, OOBInteraction{
			Protocol:     in.Protocol,
			UniqueID:     probeIDForInteraction(in.FullID, in.Raw, ls.serverHost),
			FullID:       in.FullID,
			Remote:       in.Remote,
			Raw:          in.Raw,
			TimestampISO: in.Timestamp.UTC().Format(time.RFC3339),
		})
		ls.mu.Unlock()
	})
	if err != nil {
		return nil, err
	}
	ls.health = src.Health
	return ls, nil
}

// interactionKey identifies one callback event across redeliveries. Timestamp
// is included so two genuinely distinct callbacks with the same ID are kept.
func interactionKey(in oob.Interaction) string {
	return strings.Join([]string{in.Protocol, in.FullID, in.Remote, in.Timestamp.UTC().Format(time.RFC3339Nano)}, "|")
}

func (ls *liveSession) Generate() (string, string, error) {
	if ls.isClosed() {
		return "", "", fmt.Errorf("oob: 会话已关闭，请让本次 run 分配新的回调地址")
	}
	return ls.it.Generate()
}

// probeIDForInteraction maps an interaction back to the first DNS label issued
// by Generate. Interactsh's UniqueID is the session correlation ID, not a
// per-probe label, so only FullId or the raw request can identify the probe.
func probeIDForInteraction(fullID, raw, serverHost string) string {
	for _, candidate := range []string{fullID, rawInteractionHost(raw)} {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "https://")
		candidate = strings.TrimPrefix(candidate, "http://")
		candidate = strings.TrimSuffix(candidate, "/")
		if host, _, err := net.SplitHostPort(candidate); err == nil {
			candidate = host
		}
		if candidate == "" {
			continue
		}
		// A target may prefix the probe label (data.<probe>.<server> for DNS
		// exfiltration). The probe label is the one right before the server
		// hostname, not the first label.
		if serverHost != "" {
			labels := strings.Split(strings.TrimSuffix(candidate, "."), ".")
			serverLabels := strings.Split(strings.TrimSuffix(serverHost, "."), ".")
			if len(labels) > len(serverLabels) {
				suffix := strings.Join(labels[len(labels)-len(serverLabels):], ".")
				if strings.EqualFold(suffix, serverHost) {
					return labels[len(labels)-len(serverLabels)-1]
				}
			}
		}
		if i := strings.IndexByte(candidate, '.'); i > 0 {
			return candidate[:i]
		}
		return candidate
	}
	return ""
}

func rawInteractionHost(raw string) string {
	for _, field := range strings.Fields(raw) {
		field = strings.Trim(field, `"'<>(),;`)
		if strings.Contains(field, ".") && !strings.ContainsAny(field, "/?=:") {
			return field
		}
	}
	return ""
}

// Poll drains buffered interactions. The official client polls on its own
// interval; the bounded wait is a grace loop until the deadline or delivery.
// A service failure recorded during the window is returned as an error so the
// tool records service_unavailable instead of a false no_interaction.
func (ls *liveSession) Poll(wait time.Duration) ([]OOBInteraction, error) {
	deadline := time.Now().Add(wait)
	start := time.Now()
	for {
		ls.mu.Lock()
		if len(ls.buf) > 0 {
			out := ls.buf
			ls.buf = nil
			ls.mu.Unlock()
			return out, nil
		}
		ls.mu.Unlock()
		if ls.isClosed() {
			return nil, errSessionClosed
		}
		if time.Now().After(deadline) {
			if ls.health == nil {
				return nil, nil
			}
			return nil, ls.pollWindowHealth(start)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

var (
	errNoCompletedPoll = errors.New("oob: 观察窗口内没有完成一次轮询，无法判断是否有交互")
	errSessionClosed   = errors.New("oob: 会话已关闭，观察窗口未完成")
	errStaleProbe      = errors.New("oob: probe_id не выдан в текущей сессии")
)

// trackedSession records the probe IDs issued in the current run. A resumed
// worker transcript may carry a probe ID from an earlier session; polling it
// against a new correlation ID would record a false healthy absence.
type trackedSession struct {
	inner  oobSession
	mu     sync.Mutex
	issued map[string]struct{}
}

func (t *trackedSession) Generate() (string, string, error) {
	id, host, err := t.inner.Generate()
	if err == nil {
		t.mu.Lock()
		t.issued[id] = struct{}{}
		t.mu.Unlock()
	}
	return id, host, err
}

func (t *trackedSession) Poll(wait time.Duration) ([]OOBInteraction, error) {
	return t.inner.Poll(wait)
}

func (t *trackedSession) Close() error { return t.inner.Close() }

func (t *trackedSession) knows(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.issued[id]
	return ok
}

func (ls *liveSession) pollWindowHealth(since time.Time) error {
	if ls.health == nil {
		return nil
	}
	_, err, lastErrAt, lastOK := ls.health()
	if err != nil && lastErrAt.After(since) {
		return err
	}
	if !lastOK.After(since) {
		return errNoCompletedPoll
	}
	return nil
}

func (ls *liveSession) isClosed() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.closed
}

// Close stops polling then closes the client (official contract:
// StopPolling before Close).
func (ls *liveSession) Close() error {
	if ls == nil {
		return nil
	}
	ls.mu.Lock()
	if ls.closed {
		ls.mu.Unlock()
		return nil
	}
	ls.closed = true
	ls.mu.Unlock()
	_ = ls.it.StopPolling()
	return ls.it.Close()
}

// oobTools builds the OOB worker tools for one run. Returns nil when the
// session is nil — the tools simply don't exist for the agent then.
func oobTools(sess oobSession) []actool.CoreTool {
	if sess == nil {
		return nil
	}
	tsx := &ToolSet{}
	return []actool.CoreTool{tsx.oobGenerate(sess), tsx.oobPoll(sess)}
}

// lazySession defers the Interactsh registration until the first OOB tool call.
// A worker run that never needs OOB therefore never contacts the service.
// Creation is shared across the run and errors are retried on the next call;
// no path falls back to another server.
type lazySession struct {
	mu     sync.Mutex
	new    func() (*oob.Session, error)
	live   *liveSession
	closed bool
}

func newLazySession(cfg oob.Config) *lazySession {
	return &lazySession{new: func() (*oob.Session, error) { return oob.NewClient(cfg) }}
}

func (l *lazySession) ensure() (*liveSession, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, fmt.Errorf("oob: 会话已关闭")
	}
	if l.live != nil {
		return l.live, nil
	}
	s, err := l.new()
	if err != nil {
		return nil, err
	}
	live, err := newLiveSession(s, 3*time.Second)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	l.live = live
	return live, nil
}

func (l *lazySession) Generate() (string, string, error) {
	live, err := l.ensure()
	if err != nil {
		return "", "", err
	}
	return live.Generate()
}

func (l *lazySession) Poll(wait time.Duration) ([]OOBInteraction, error) {
	live, err := l.ensure()
	if err != nil {
		return nil, err
	}
	return live.Poll(wait)
}

func (l *lazySession) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	live := l.live
	l.mu.Unlock()
	if live != nil {
		return live.Close()
	}
	return nil
}

// OOBSessionFactory returns a per-run session factory from an oob.Config.
// Each call returns an independent lazy session; registration happens on the
// first OOB tool call, shutdown is StopPolling→Close, and the private key
// lives only in *oob.Session memory.
func OOBSessionFactory(cfg oob.Config) func() (oobSession, func(), error) {
	return func() (oobSession, func(), error) {
		ls := newLazySession(cfg)
		return ls, func() { _ = ls.Close() }, nil
	}
}

// OOBFactoryFromOptions builds a worker session factory from the resolved
// config options. Returns nil when OOB is disabled — the worker then simply
// has no OOB tools.
func OOBFactoryFromOptions(enabled bool, serverURL, token string) func() (oobSession, func(), error) {
	if !enabled {
		return nil
	}
	return OOBSessionFactory(oob.Config{Enabled: true, ServerURL: serverURL, Token: token})
}
