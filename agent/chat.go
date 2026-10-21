package agent

import (
	"context"
	"github.com/Autumn-27/artex/locale"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// ChatAgent is the generic, task-independent conversational runner behind the chat
// page. It generalizes MainAgent.Chat: any agent (built-in OR a custom one, by
// key) can be chatted with, multi-turn history resumed from the transcript. It is
// built on SDK DefaultTools (Bash/Read/Write/Edit/LS/Glob/Grep) plus its visible
// skills/MCP and bound tools. Finding tools use a conversation-owned graph;
// task orchestration and its execution context remain separate.
type ChatAgent struct {
	nativeOllamaFn func() bool
	prov           llm.Provider
	model          string
	workDir        string
	tx             *transcript.Store
	window         int
	proxyAddr      string
	proxyCACert    string
	webSearch      WebSearchOpts
	guard          *guard.Guard // optional; nil disables intercept hooks for chat
	nonStreamingFn func() bool  // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn   func() bool  // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn    func() int   // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNativeOllamaResolver resolves whether the current run uses native Ollama.
func (c *ChatAgent) SetNativeOllamaResolver(fn func() bool) { c.nativeOllamaFn = fn }

func NewChatAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window int) *ChatAgent {
	return &ChatAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window}
}

// SetNonStreaming wires a resolver deciding whether chat runs use the
// non-streaming model path (true = non-streaming). nil/unset = streaming.
func (c *ChatAgent) SetNonStreaming(fn func() bool) { c.nonStreamingFn = fn }

func (c *ChatAgent) nonStreaming() bool { return c.nonStreamingFn != nil && c.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether chat runs use the experimental
// noa context-compression mechanism. nil/unset = off (built-in compaction). Read
// per run so the settings toggle takes effect without rebuilding the agent.
func (c *ChatAgent) SetNoaEnabled(fn func() bool) { c.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (c *ChatAgent) SetMaxTokens(fn func() int) { c.maxTokensFn = fn }

func (c *ChatAgent) maxTokens() int {
	if c.maxTokensFn == nil {
		return 0
	}
	return c.maxTokensFn()
}

// SetProxy points the chat agent's WebFetch/Bash at the recording proxy plus the
// CA cert it trusts (empty addr = direct). Kept for parity with the other agents.
func (c *ChatAgent) SetProxy(addr, caCert string) { c.proxyAddr, c.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the chat agent (off by default).
func (c *ChatAgent) SetWebSearch(o WebSearchOpts) { c.webSearch = o }

// SetGuard attaches a guard (with user-configured intercept rules) to this chat
// agent. Must be called before Chat; safe to call multiple times.
func (c *ChatAgent) SetGuard(g *guard.Guard) { c.guard = g }

// chatWorkDirSpec returns a working-directory notice appended to every chat
// agent's system prompt. Mirrors artifactSpec but without pentest-specific
// wording such as payload or captured response bodies would be odd in a general assistant.
func chatWorkDirSpec(workDir string, langs ...locale.Lang) string {
	return locale.Text(locale.First(langs), "\n\n**File output rules**: Write all files to the working directory ") + workDir + locale.Text(locale.First(langs), " (the default CWD; use relative paths or this absolute path). Do not write to /tmp or other absolute paths.")
}

// chatSystem preserves the role default if its stored template is missing or invalid.
func chatSystem(agentKey, dataDir, workDir string, langs ...locale.Lang) string {
	def := DefaultAssistantPrompt
	switch agentKey {
	case "auto":
		def = autoDefaultTmpl
	case "pentest":
		def = pentestDefaultTmpl
	case "reporter":
		def = ReporterDefaultPrompt
	case "retester":
		def = RetesterDefaultPrompt
	}
	return renderSystem(agentKey, def, chatVars{DataDir: dataDir, Now: nowStr()}, langs...) + chatWorkDirSpec(workDir, langs...)
}

// chatVars carries the runtime variables a custom agent's prompt may reference.
// DataDir (server data root) + Now (server wall-clock, refreshed each turn) are the
// universal ones; any other {{.X}} fails to render and falls back to the role default.
type chatVars struct{ DataDir, Now string }

// Chat runs ONE turn of a conversation with the agent identified by agentKey,
// resuming prior history keyed by sessionID. maxTurns is the per-turn agent step
// budget (0 = unlimited). maxDuration is the wall-clock run budget per turn
// (0 = unlimited); the timer resets each time Chat is called, so a new user
// message always starts a fresh countdown. webSearch gates network search for
// THIS agent (the global backend/key still come from the chat agent's config,
// but each agent decides on/off). emit receives each execution step (thinking /
// tool_use / tool_result / text / result), tagged with the agent key as the
// worker lane.
func (c *ChatAgent) Chat(ctx context.Context, agentKey, sessionID, message string, maxTurns int, maxDuration time.Duration, webSearch bool, emit func(db.Activity)) (string, error) {
	// gate the global web-search opts by this agent's own flag.
	ws := c.webSearch
	if !webSearch {
		ws.Enabled = false
	}

	// Per-session working directory: <workDir>/sessions/<sessionID>/
	// Isolates file writes across conversations, mirroring how workers use i<intentID>/.
	sessionWorkDir := filepath.Join(c.workDir, "sessions", sessionID)
	_ = os.MkdirAll(sessionWorkDir, 0o755)
	ctx = intercept.WithReviewWorkingDirectory(ctx, sessionWorkDir)

	// Pure assistant: DefaultTools as the base; AugmentTools layers in the key's
	// visible skills/MCP and lets the DB tools table filter/override. DefaultTools
	// have no tools-table rows, so they always pass through.
	base := actool.DefaultTools()
	// Attribution: the caller may already know which task/exploration this run
	// belongs to (a reporter trigger fired by a task finding runs in its own
	// conversation but belongs to that task). Only fill in the session id —
	// overwriting the task/exploration would drop the run's tool_usage rows off
	// their source task.
	ri := RunInfoFrom(ctx)
	ri.SessionID = sessionID
	ctx = WithRunInfo(ctx, ri)
	tools, def, cleanup := AugmentTools(ctx, agentKey, base)
	defer cleanup()

	system, boundary := deferredSystem(chatSystem(agentKey, c.workDir, sessionWorkDir, locale.FromContext(ctx)), def)
	opts := agentcore.Options{
		Provider:        c.prov,
		SystemPrompt:    append(system, outputLanguageInstruction(locale.FromContext(ctx))),
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // Route through capture proxy and trust its CA for re-signed HTTPS certificates.
		WebFetchProxy:   c.proxyAddr,
		WebFetchCACert:  c.proxyCACert,
		// Optional web search: ddgs needs no key, brave-free needs BraveKey, tavily needs TavilyKey.
		// WebSearchProxy is an independent egress proxy, unrelated to capture MITM; empty means direct.
		EnableWebSearch:       ws.Enabled,
		WebSearchBackend:      ws.Backend,
		BraveSearchAPIKey:     ws.BraveKey,
		TavilySearchAPIKey:    ws.TavilyKey,
		DeepSeekSearchBaseURL: ws.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  ws.DeepSeekAPIKey,
		DeepSeekSearchModel:   ws.DeepSeekModel,
		WebSearchProxy:        ws.Proxy,
		BashEnv:               proxyEnv(c.proxyAddr, c.proxyCACert), // Bash subprocesses inherit the proxy and trusted CA.
		WorkingDir:            sessionWorkDir,
		MaxTurns:              maxTurns,
		MaxDuration:           maxDuration,
		Compaction:            compactionConfig(c.window),
		Todos:                 actool.NewTodoStore(),
		// large tool output spills to cmd-output/ under the session dir.
		// Use the SDK's default 30000-character tool.Capture limit.
		ToolOutputDir: filepath.Join(sessionWorkDir, "cmd-output"),
		// At the step budget, SDK settlement produces a summary. Prompt and turn budget
		// are editable per agent; empty/0 uses the generic ten-turn default.
		Settlement:   wrapupSettlement(agentKey, nil, locale.FromContext(ctx)),
		NonStreaming: c.nonStreaming(), // Non-streaming profiles use Provider.Complete.
		MaxTokens:    c.maxTokens(),    // 0 omits the limit and uses the server default.
	}
	if c.guard != nil {
		opts.Hooks = c.guard.Hooks()
	}
	if c.tx != nil { // persist raw human↔AI conversation; one accumulating file per thread
		opts.Transcript = c.tx
		opts.SessionID = sessionID
	}
	// Experimental noa compaction stores persistent archives under <workDir>/noa/<SessionID>.
	secureWebSearch(&opts, ws.PolicyResolver, ws.DeepSeekMeter)
	if err := bindChatRunOptions(ctx, &opts); err != nil {
		return "", err
	}
	applyRuntimePolicy(&opts, RuntimePolicy{NativeOllama: c.nativeOllamaFn != nil && c.nativeOllamaFn(), ContextWindow: c.window})
	noaSessionState := enableNoa(&opts, c.noaEnabledFn, c.workDir, chatNoaSession(ctx, "chat-"+sessionID), noaWarn("chat-"+sessionID))
	ctx = attachChatContext(ctx, &opts, def, noaSessionState)
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload prior conversation so the agent has context across turns (each Chat is
	// a fresh session). First turn: no file yet → Resume loads nothing and proceeds.
	if c.tx != nil {
		if err := s.Resume(opts.SessionID); err != nil {
			return "", err
		}
	}
	// re-unlock skill-gated MCPs from prior Skill() calls in the reloaded history so
	// revealed tools stay callable across the fresh session.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = agentKey
			emit(r)
		}
	})
	return text, err
}
