// Package agent wires real LLM-driven planner and work agents (on top of the
// agent-core SDK) to the PostgreSQL graphs. See the architecture design documentation.
// §4.3 (planner) and §4.4 (work agent).
//
// Provider configuration is read from the environment so the system runs with
// any Anthropic- or OpenAI-format endpoint. If no key is configured, FromEnv
// returns ok=false and the exploration engine stays idle (an LLM is required).
package agent

import (
	"bytes"
	"context"
	"fmt"
	"github.com/Autumn-27/artex/locale"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	runtimeconfig "github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/llmnet"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/artex/ollama"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"
)

// Config describes the LLM backend resolved from the environment.
type Config struct {
	PolicyResolver func() (bool, []string)

	KeepAlive            string
	MaxConcurrent        int
	LocalLLMOnly         bool
	LocalLLMAllowedCIDRs []string
	JSONSchema           map[string]any
	JSONOutput           bool
	Format               llm.Format
	BaseURL              string
	APIKey               string
	Model                string
	// Proxy routes all LLM requests through the given proxy URL (http/https/socks5,
	// optionally with user:pass@ credentials). Empty means direct — it does NOT
	// fall back to the standard *_PROXY environment variables.
	Proxy string
	// RatePerSecond / RatePerMinute cap the shared request rate across ALL agents
	// using the provider (0 = that window unlimited).
	RatePerSecond float64
	RatePerMinute float64
	// ContextWindowK is the model's context window in K tokens (user-configured),
	// used to size compaction thresholds. 0 = default; see CompactionWindow.
	ContextWindowK int
	// ThinkingType independently controls thinking.type:
	// Empty omits the field for compatibility; disabled explicitly turns it off;
	// enabled turns it on. It is independent of ReasoningEffort because some endpoints
	// expose only an effort parameter, so either setting may be used on its own.
	ThinkingType string
	// ReasoningEffort independently controls reasoning intensity:
	// Empty omits it; low/medium/high/xhigh/max select the corresponding effort.
	// OpenAI uses reasoning_effort; Anthropic uses output_config.effort.
	ReasoningEffort string
	// Stream controls profile SSE usage: true (default) streams, false sends
	// stream:false and gets complete JSON through Provider.Complete. Non-streaming
	// avoids broken gateway SSE (empty frames or lost reasoning deltas), at the cost
	// of live progress/token counts. Maps to agentcore.Options.NonStreaming = !Stream.
	Stream bool
	// MaxTokens limits output tokens per reply, independently of ContextWindowK.
	// Zero becomes 32768 on agent harness turns. Direct Anthropic calls default
	// to 8192; direct native Ollama calls use their own default (4096).
	// Agent turns pass this value through agentcore.Options.MaxTokens.
	MaxTokens int
	// MaxTokensField chooses the request field for MaxTokens in openai format:
	// Empty uses max_tokens; max_completion_tokens selects the newer field.
	// OpenAI reasoning models reject max_tokens with unsupported_parameter, while
	// many compatible gateways require it. Let users choose per endpoint; do not infer.
	MaxTokensField string
	// Nonempty SessionHeaderKey adds a custom HTTP header to each LLM request,
	// carrying the current session ID (conv-<id>, exp<x>-worker-i<intent>, etc.;
	// see WorkerSessionID). Some gateways use it for prompt caching/sticky routing.
	// Empty omits it. transcript.WithSessionID attaches the value to context, and
	// RoundTripper reads it, so a shared provider can send different session values.
	SessionHeaderKey string
	// Retry holds resolved profile -> global -> built-in retry parameters, resolved
	// by the server. See RetryConfig for layers; zero values use built-in defaults.
	Retry RetryConfig
}

// RetryConfig travels with an LLM profile. Attempts have consistent semantics:
// zero uses defaults, negative disables retries, positive overrides the count.
// An interval of zero retains exponential backoff; positive selects a fixed delay.
type RetryConfig struct {
	// ConnectAttempts/ConnectInterval cover pre-stream connection reset/timeout/429/5xx,
	// mapping to llm.Config.MaxRetries/RetryInterval: default 3, exponential 0.5s to 8s.
	ConnectAttempts int
	ConnectInterval time.Duration
	// EmptyAttempts/EmptyInterval cover completed responses without content blocks
	// in openai format, mapping to EmptyResponseRetries/EmptyResponseInterval.
	// Defaults: two retries using the same exponential ladder.
	EmptyAttempts int
	EmptyInterval time.Duration
	// StreamAttempts/StreamInterval add a same-provider safe-window retry layer above
	// the SDK for broken streams/overload/in-stream 429 only before any output is delivered.
	// server/task_llm.go consumes it; defaults are two retries, exponential 0.5s to 4s.
	StreamAttempts int
	StreamInterval time.Duration
}

// compaction window resolution bounds (in K tokens). Below the floor the
// threshold math (window − summary reserve − buffer) would go non-positive and
// compaction would fire every turn; above the cap it would never fire.
const (
	defaultWindowK = 200  // unset → assume a 200K window (Claude default)
	minWindowK     = 32   // floor so effectiveWindow stays comfortably positive
	maxWindowK     = 1000 // cap at 1M tokens (user request)
)

// CompactionWindow returns the model context window in TOKENS for compaction
// thresholds, resolved from the user-configured size (ContextWindowK). 0/unset →
// a 200K default; otherwise clamped to [32K, 1M] so compaction stays effective.
func (c Config) CompactionWindow() int {
	k := c.ContextWindowK
	if c.Provider() == "ollama" {
		if k <= 0 {
			k = 32
		}
		if k < 16 {
			k = 16
		}
		if k > maxWindowK {
			k = maxWindowK
		}
		return k * 1024
	}
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if k > maxWindowK {
		k = maxWindowK
	}
	return k * 1000
}

// compactionConfig builds the agent-core compaction config for a context window
// in tokens. agentcore.NewSession wires the summarizer (same provider) when this
// is set on Options.Compaction.
func compactionConfig(windowTokens int) *compaction.Config {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return &compaction.Config{ContextWindow: windowTokens}
}

// FromEnv resolves the LLM provider config:
//
//	ARTEX_LLM_PROVIDER = anthropic|openai|opencode-go (default: inferred from keys)
//	ARTEX_LLM_MODEL    = model id        (default: per provider)
//	ARTEX_LLM_BASE_URL = endpoint        (optional)
//	ARTEX_LLM_PROXY    = proxy URL        (optional; http/https/socks5)
//	ANTHROPIC_API_KEY / OPENAI_API_KEY / OPENCODE_API_KEY = credentials
func FromEnv() (Config, bool) {
	file := runtimeconfig.ResolveLLM()
	prov := strings.TrimSpace(file.Provider)
	if prov == "" {
		switch {
		case os.Getenv("ANTHROPIC_API_KEY") != "":
			prov = "anthropic"
		case os.Getenv("OPENAI_API_KEY") != "":
			prov = "openai"
		case os.Getenv("OPENCODE_API_KEY") != "":
			prov = "opencode-go"
		default:
			return Config{}, false
		}
	}
	c := ConfigFrom(prov, file.Model, file.BaseURL, file.APIKey, file.Proxy)
	// Native Ollama never falls back to a cloud credential.
	_, explicitKey := runtimeconfig.LLMEnv("API_KEY")
	if c.APIKey == "" && !explicitKey {
		if prov == "opencode-go" {
			// The wire format resolved to "openai", but the credential is the
			// OpenCode subscription key — never OPENAI_API_KEY.
			c.APIKey = os.Getenv("OPENCODE_API_KEY")
		} else {
			switch c.Provider() {
			case "ollama":
				c.APIKey = os.Getenv("OLLAMA_API_KEY")
			case "anthropic":
				c.APIKey = os.Getenv("ANTHROPIC_API_KEY")
			default:
				c.APIKey = os.Getenv("OPENAI_API_KEY")
			}
		}
	}
	if file.Stream != nil {
		c.Stream = *file.Stream
	}
	if file.ContextWindowK != 0 {
		c.ContextWindowK = file.ContextWindowK
	}
	if file.MaxTokens != 0 {
		c.MaxTokens = file.MaxTokens
	}
	if file.KeepAlive != "" {
		c.KeepAlive = file.KeepAlive
	}
	c.MaxConcurrent = file.MaxConcurrent
	c.ThinkingType = file.ThinkingType
	c.ReasoningEffort = file.ReasoningEffort
	c.LocalLLMOnly, c.LocalLLMAllowedCIDRs = runtimeconfig.LocalLLMPolicy()
	return c, c.Ready()
}

// ConfigFrom builds a Config from UI-provided strings (provider defaults to
// anthropic; model defaults per provider). Inputs are trimmed and the base URL
// is normalized to the API base the provider expects (the provider appends the
// endpoint path itself), so a full endpoint URL is tolerated.
func ConfigFrom(provider, model, baseURL, apiKey, proxy string) Config {
	c := Config{
		Model:   strings.TrimSpace(model),
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		Proxy:   strings.TrimSpace(proxy),
		Stream:  true, // Streaming defaults on; callers override from the profile.
	}
	switch strings.TrimSpace(provider) {
	case "ollama":
		c.Format = llm.Format("ollama")
		c.BaseURL = ollama.NormalizeBaseURL(c.BaseURL)
		c.KeepAlive = ollama.DefaultKeepAlive
		c.ContextWindowK = 32
		c.MaxTokens = 4096
		if c.Model == "" {
			c.Model = ollama.DefaultModel
		}
	case "openai":
		c.Format = llm.FormatOpenAI
		// provider appends "/chat/completions"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/chat/completions"), "/")
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "opencode-go":
		// OpenCode Go (the OpenCode subscription gateway) is an OpenAI-compatible
		// endpoint: it resolves to the openai wire format at
		// https://opencode.ai/zen/go/v1 and is authenticated with
		// OPENCODE_API_KEY, never OPENAI_API_KEY (see FromEnv). The gateway
		// rejects requests without x-opencode-session (HTTP 400 MissingSessionID),
		// so the session header is required; the UI template prefills it too.
		c.Format = llm.FormatOpenAI
		c.SessionHeaderKey = "x-opencode-session"
		if c.BaseURL == "" {
			c.BaseURL = "https://opencode.ai/zen/go/v1"
		} else {
			// provider appends "/chat/completions"; tolerate a full endpoint URL.
			c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/chat/completions"), "/")
		}
		if c.Model == "" {
			c.Model = "glm-5.3-flash"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		// provider appends "/responses"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/responses"), "/")
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		// provider appends "/v1/messages".
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/v1/messages"), "/")
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	return c
}

// isFalsy reports whether an env-var string explicitly requests "off". Empty or
// unrecognized → false (so an unset var keeps the streaming default).
func isFalsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// Provider returns the short provider name ("anthropic"/"openai").
func (c Config) Provider() string {
	switch c.Format {
	case llm.Format("ollama"):
		return "ollama"
	case llm.FormatOpenAI:
		return "openai"
	case llm.FormatOpenAIResponses:
		return "openai-responses"
	}
	return "anthropic"
}

// NewProvider builds an llm.Provider from the config. When a rate is set, the
// limiter lives on the single provider instance — so planner + all workers +
// main agent (which share this provider) are bounded by one shared rate limit.
func (c Config) NewProvider() (llm.Provider, error) {
	client, err := c.HTTPClient()
	if err != nil {
		return nil, err
	}
	if c.Provider() == "ollama" {
		native, err := ollama.NewClient(c.BaseURL, c.APIKey, client)
		if err != nil {
			return nil, err
		}
		return llm.RateLimited(ollama.NewProvider(native, c.Model, ollama.Options{ContextWindow: c.CompactionWindow(), KeepAlive: c.KeepAlive, ThinkingType: c.ThinkingType, MaxTokens: c.MaxTokens, JSON: c.JSONOutput, Schema: c.JSONSchema, ConnectAttempts: c.Retry.ConnectAttempts, ConnectInterval: c.Retry.ConnectInterval, EmptyAttempts: c.Retry.EmptyAttempts, EmptyInterval: c.Retry.EmptyInterval}), llm.RateLimit{PerSecond: c.RatePerSecond, PerMinute: c.RatePerMinute}), nil
	}
	// Block SDK environment-key inheritance for deliberately keyless local endpoints.
	if c.APIKey == "" {
		if !llmnet.IsLocalEndpoint(c.BaseURL) {
			return nil, locale.Errorf("llm: API key required for non-local compatible endpoint")
		}
		c.APIKey = "scopeweaver-local"
	}
	lc := llm.Config{
		Format:     c.Format,
		BaseURL:    c.BaseURL,
		APIKey:     c.APIKey,
		Model:      c.Model,
		HTTPClient: client,
	}
	// Pass thinking.type and effort independently; empty omits each field.
	// Either, both, or neither may be sent; official Claude 5.5 defaults to adaptive.
	rejectDisabled := strings.HasPrefix(c.Model, "claude-sonnet-") || strings.HasPrefix(c.Model, "claude-opus-") || (strings.HasPrefix(c.Model, "claude-haiku-") && (c.ReasoningEffort == "xhigh" || c.ReasoningEffort == "max"))
	lc.ThinkingType = c.ThinkingType
	if c.officialClaude55() && (lc.ThinkingType == "" || (lc.ThinkingType == "disabled" && rejectDisabled)) {
		lc.ThinkingType = "adaptive"
	}
	lc.ReasoningEffort = c.ReasoningEffort
	// Choose the output-limit field name (empty means max_tokens). Its value comes
	// from agentcore.Options.MaxTokens per round; the provider only chooses the key.
	lc.MaxTokensField = c.MaxTokensField
	// Pass SDK retry semantics unchanged: counts 0=default/negative=off; interval 0=exponential/positive=fixed.
	lc.MaxRetries = c.Retry.ConnectAttempts
	lc.RetryInterval = c.Retry.ConnectInterval
	lc.EmptyResponseRetries = c.Retry.EmptyAttempts
	lc.EmptyResponseInterval = c.Retry.EmptyInterval
	if c.RatePerSecond > 0 || c.RatePerMinute > 0 {
		lc.RateLimit = &llm.RateLimit{PerSecond: c.RatePerSecond, PerMinute: c.RatePerMinute}
	}
	provider, err := llm.NewProvider(lc)
	if err != nil {
		return nil, err
	}
	provider = cloudProviderAdapter{Provider: provider, format: c.Format, claude: c.officialClaude55(), rejectDisabled: rejectDisabled}
	if c.Model == "glm-5.3" {
		return glm53Provider{Provider: provider}, nil
	}
	return provider, nil
}

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
var nonFailoverHTTPStatus = regexp.MustCompile(`(?:status(?:\s+code)?|http(?:\s+status)?)\s*[=:]?\s*(?:401|403|5\d\d)\b`)
var transientQuotaLimit = regexp.MustCompile(`(?i)(?:\b(?:rpm|tpm|rpd|qps)\b|quota[_\s-]*metric|rate[_\s-]*limit|too many requests|(?:requests?|tokens?)\s+(?:per|/)\s*(?:second|minute)|(?:per|/)\s*(?:second|minute)\s+(?:requests?|tokens?)|generate[_\s-]*requests[_\s-]*per[_\s-]*(?:minute|second)|tokens?[_\s-]*per[_\s-]*(?:minute|second))`)

func IsQuotaExhaustedMessage(message string) bool {
	message = strings.ToLower(message)
	// Authentication/authorization and provider-side 5xx failures never rotate,
	// even when a gateway happens to echo a quota-looking phrase in the body.
	if nonFailoverHTTPStatus.MatchString(message) {
		return false
	}
	// Provider APIs frequently describe an ordinary rate limit as "quota
	// exceeded", especially Google-style responses containing a quota metric.
	// These limits recover with time and must stay on the current provider.
	if transientQuotaLimit.MatchString(message) {
		return false
	}
	markers := []string{
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted",
		"exceeded your current quota", "billing_hard_limit_reached",
		"billing hard limit", "billing_not_active", "credit balance", "insufficient credit",
		"insufficient balance", "balance is too low", "payment required", "status 402",
		"余额不足", "额度不足", "额度已用尽", "欠费",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// gRPC RESOURCE_EXHAUSTED is overloaded for both account quota and ordinary
	// request-rate limiting. Preserve it as an explicit exhaustion signal only
	// when the same error does not identify a transient rate limit.
	return strings.Contains(message, "resource_exhausted") &&
		!strings.Contains(message, "rate limit") &&
		!strings.Contains(message, "too many requests")
}

// quotaAwareTransport preserves Norma's normal retry behavior except for a 429
// whose body explicitly says the account quota/balance is exhausted. Norma's
// retry loop treats every 429 as transient; normalizing only that response to
// 402 lets a task router fail over immediately while retaining the original
// response body for provider-specific classification and audit logs.
type quotaAwareTransport struct {
	base   http.RoundTripper
	claude *claudeReplay
	// sessionHeaderKey, when non-empty, is the HTTP header name each request
	// carries; its value is the session id read from the request context. Empty
	// disables it. See Config.SessionHeaderKey.
	sessionHeaderKey string
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, explicit := req.Header["User-Agent"]; !explicit {
		req.Header.Set("User-Agent", "scopeweaver")
	}
	if t.claude != nil {
		if err := t.claude.rewrite(req); err != nil {
			return nil, err
		}
	}
	raw, _ := req.Context().Value(rawUsageKey{}).(*rawUsage)
	if raw != nil {
		raw.reset()
	}
	// Custom session-id header: name is user-configured, value is THIS run's
	// session id (norma stashes it on the context via transcript.WithSessionID).
	// Stable across a session's turns and distinct across sessions — exactly what
	// a session-keyed prompt cache wants. Skipped when no session id is present.
	if t.sessionHeaderKey != "" {
		if sid := transcript.SessionIDFrom(req.Context()); sid != "" {
			req.Header.Set(t.sessionHeaderKey, sid)
		}
	}
	// When LLM recording is on, the Recorder puts a Capture on the context so the
	// raw wire bodies can be persisted. This is the only layer that still sees
	// them: norma builds the request body internally and decodes the SSE response
	// before either reaches the recorder.
	capt := llmrec.CaptureFrom(req.Context())
	capt.SetRequest(requestBodySnapshot(req))

	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// Tee rather than read: a 200 is an SSE stream that must keep streaming. The
	// 429 branch below reads through this wrapper, so its body lands in the
	// capture before being replaced.
	resp.Body = capt.TeeResponse(resp.StatusCode, resp.Body)
	if raw != nil || t.claude != nil {
		var observation *claudeObservation
		if t.claude != nil {
			observation = &claudeObservation{replay: t.claude}
		}
		resp.Body = &observeBody{ReadCloser: resp.Body, sse: strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"), observe: func(event string, data []byte) {
			if raw != nil {
				raw.observe(event, data)
			}
			if observation != nil {
				observation.observe(event, data)
			}
		}}
	}

	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if readErr != nil {
		return resp, nil
	}
	if IsQuotaExhaustedMessage(string(body)) {
		resp.StatusCode = http.StatusPaymentRequired
		resp.Status = "402 Payment Required"
	}
	return resp, nil
}

// requestBodySnapshot copies an outgoing request body without consuming it.
// norma builds every model request from a *bytes.Reader, so net/http populates
// GetBody and the copy has no effect on what gets sent.
func requestBodySnapshot(req *http.Request) string {
	if req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	return string(b)
}

// Ready allows keyless native Ollama and explicit local compatible servers.
func (c Config) Ready() bool {
	return c.APIKey != "" || c.Provider() == "ollama" || llmnet.IsLocalEndpoint(c.BaseURL)
}

// HTTPClient is shared by inference, metadata, diagnostics, and judge consumers.
func (c Config) HTTPClient() (*http.Client, error) {
	var client *http.Client
	var err error
	if c.PolicyResolver != nil {
		client, err = llmnet.NewClientWithPolicy(c.Proxy, c.PolicyResolver)
	} else {
		client, err = llmnet.NewClient(c.Proxy, c.LocalLLMOnly, c.LocalLLMAllowedCIDRs)
	}
	if err != nil {
		return nil, err
	}
	var replay *claudeReplay
	if c.officialClaude55() {
		replay = &claudeReplay{}
	}
	client.Transport = quotaAwareTransport{base: client.Transport, sessionHeaderKey: strings.TrimSpace(c.SessionHeaderKey), claude: replay}
	return client, nil
}

func quotaAwareHTTPClient(proxy, sessionHeaderKey string) (*http.Client, error) {
	return (Config{Proxy: proxy, SessionHeaderKey: sessionHeaderKey}).HTTPClient()
}

// logTestConnection prints the raw HTTP status code(s) and response body of a
// connection test to the server log, leaving a diagnosable trail when users click Test:
// exactly what the gateway returned — 401 bodies, quota text, empty frames — not
// just the collapsed ok/err the UI shows. Bodies are clipped to keep a chatty
// SSE stream from flooding the log.
func logTestConnection(c Config, capt *llmrec.Capture) {
	attempts := capt.Attempts()
	if len(attempts) == 0 {
		log.Printf(locale.Text(locale.ServerDefault(), "[llm-test] %s / %s @ %s — no HTTP request sent (configuration or connection failed)"),
			c.Provider(), c.Model, c.BaseURL)
		return
	}
	for i, a := range attempts {
		log.Printf(locale.Text(locale.ServerDefault(), "[llm-test] %s / %s @ %s — attempt %d/%d HTTP %d\nResponse body: %s"),
			c.Provider(), c.Model, c.BaseURL, i+1, len(attempts), a.Status, clipBody(a.Body))
	}
}

// clipBody trims a wire body for logging. 4K is plenty to show an error JSON or
// the head of an SSE stream while bounding a runaway response.
func clipBody(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return locale.Text(locale.ServerDefault(), "(empty)")
	}
	const max = 4096
	if len(s) > max {
		return s[:max] + fmt.Sprintf(locale.Text(locale.ServerDefault(), "…(truncated, %d bytes total)"), len(s))
	}
	return s
}

// TestConnection makes a minimal real completion to verify the provider/model/
// endpoint/key actually work. Returns the round-trip latency and the model's
// reply text.
func TestConnection(ctx context.Context, c Config) (time.Duration, string, error) {
	prov, err := c.NewProvider()
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Capture raw wire status/body for connection diagnostics; decoding into norma
	// StreamEvents otherwise loses those details. quotaAwareTransport finds this
	// Capture in context and records every HTTP attempt's status and response body.
	ctx, capt := llmrec.NewCapture(ctx)
	defer logTestConnection(c, capt)
	// Connection tests bypass the agentcore session loop, which normally attaches
	// session IDs. Endpoints configured with SessionHeaderKey may require one,
	// such as opencode zen's x-opencode-session header (400 MissingSessionID if absent).
	// Attach a one-time random ID so tests use the same header path as real chat.
	// Endpoints without SessionHeaderKey ignore it, with no side effects.
	ctx = transcript.WithSessionID(ctx, "conntest-"+transcript.NewSessionID())
	start := time.Now()
	// Give MaxTokens enough room: reasoning models can spend thousands of tokens
	// before answering even a simple ping. A tiny budget stops during reasoning,
	// yielding finish=length and confusing interruption/resume indications despite
	// transport success. A sufficient budget lets the model emit OK and stop cleanly.
	// Keep EscalateMaxTokens false to avoid wasteful retry/resume loops after truncation.
	reply, err := agentcore.Run(ctx, agentcore.Options{
		Provider:       prov,
		SystemPrompt:   []string{locale.Text(locale.FromContext(ctx), "This is a connection test. Output exactly the two characters OK, with no reasoning, explanation, or other content.")},
		PermissionMode: acperm.ModeBypass,
		MaxTurns:       1,
		MaxTokens:      8192,
		NonStreaming:   !c.Stream, // Test using this profile's actual streaming/non-streaming mode.
	}, "ping")
	lat := time.Since(start)
	if err != nil {
		return lat, "", err
	}
	// err==nil alone is insufficient: a successful request may still return no text
	// due to reasoning exhaustion, safety filtering, or a compatibility layer dropping
	// content. Treat missing visible text as failure, matching unusable chat behavior.
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return lat, "", locale.Errorf("Model returned no content (request succeeded, but no text was returned)")
	}
	return lat, reply, nil
}
