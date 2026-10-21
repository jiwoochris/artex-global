package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatstate"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/artex/locale"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/traffic"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// BuildVersion is the backend application version, injected from cmd/artex at
// startup (which in turn gets it from -ldflags "-X main.version=<tag>").
// Defaults to "dev" for local builds. Exposed to the frontend via GET /api/health.
var BuildVersion = "dev"

// Server exposes the ARTEX backend over a JSON HTTP API for the shadcn/ui
// frontend.
type Server struct {
	m      *Manager
	engine *Engine
	ctx    context.Context

	skillDir string // root directory for skill subdirectories
	jwtKey   []byte // HS256 signing key loaded from / generated into dataDir/jwt.key

	// authMu guards the first-run setup token, the cached password hash (tokens
	// are signed with a key derived from it — see auth.go signingKey) and the
	// failed-attempt limiter. initMu serialises /api/auth/init check-then-set.
	authMu         sync.Mutex
	initMu         sync.Mutex
	setupToken     string
	passHash       string
	passHashLoaded bool
	passHashAt     time.Time
	loginLimiter   *authLimiter

	// concMu serializes concurrency-cap decisions (admission + reconcile) so a
	// scheduler tick and an HTTP settings change / task creation can't both count
	// free slots off the same snapshot and over-promote past the limit.
	concMu sync.Mutex

	cfgMu     sync.Mutex
	chatAgent *agent.ChatAgent // conversational runner for the chat page; nil w/o LLM
	// llmProv is the fully-decorated global provider (recorder + failover chain)
	// installed by applyLLM. Task routers use it after an explicit chain is cleared.
	llmProv   llm.Provider
	llmDirect llm.Provider // concrete global provider, before any failover pool
	llmCfg    agent.Config // current LLM config (key not exposed)
	llmOn     bool
	llmProf   string // active LLM profile name (for llmrec tagging)

	// chatBusy guards the per-task main-agent run: the chat handler launches the
	// agent on the server's background ctx (not the request ctx) and returns
	// immediately, so a page reload / proxy timeout can't abort a live run. This
	// map serializes turns per task — one main-agent run at a time per task, so
	// concurrent messages don't corrupt the shared exp<id>-main transcript.
	chatMu        sync.Mutex
	chatBusy      map[string]bool
	chatClosing   bool
	chatWG        sync.WaitGroup
	chatDrainDone chan struct{}
	chatDrainErr  error
	// chatCancel holds the cancel func for each in-flight conversation run (keyed by
	// convBusyKey), so a manual stop can abort JUST that session's agent run. Set/
	// cleared alongside chatBusy under chatMu. Aborting a run does NOT touch the P3
	// trigger queue — the drain goroutine simply proceeds to the next queued fire.
	chatCancel    map[string]context.CancelCauseFunc
	chatMutations map[string]map[string]context.CancelCauseFunc

	// triggerQ buffers P3 trigger fires PER AGENT. A per-agent "pump" launches runs up
	// to a concurrency limit derived from the agent policy: serial means limit 1 (+ optional
	// merge); parallel → limit = trigger_max_parallel (0=∞), no merge. triggerActive
	// counts in-flight runs per agent (replaces a boolean drain flag); a run's
	// completion decrements it and re-pumps to fill the freed slot. triggerCfg caches
	// the agent's last-read policy so the pump never queries the DB while holding queueMu.
	// Distinct agents always run concurrently. Queue is in-memory (matches chatBusy); a
	// restart drops pending fires — the scheduler re-fires from watermarks next tick.
	queueMu       sync.Mutex
	triggerQ      map[string][]triggeredRun
	triggerActive map[string]int
	triggerCfg    map[string]triggerBehavior

	// profChatAgents caches a per-profile ChatAgent (chat page), keyed by profile id.
	// Built lazily on first use; invalidated when any profile is saved/activated/deleted
	// so edits take effect.
	profMu         sync.Mutex
	profChatAgents map[int64]*agent.ChatAgent // per-profile ChatAgent cache (chat page)

	// provByProfile caches ONE provider per LLM profile id so every agent bound or
	// pinned to the same profile shares a single provider instance — hence one rate
	// limiter. applyLLM also resolves the persisted global-active profile through this
	// cache, so global fallback and task chains do not accidentally double the
	// configured request rate. Cleared on profile edits, then repopulated by active reapply.
	provCacheMu   sync.Mutex
	provByProfile map[int64]*provEntry
	provCacheGen  uint64

	// llmHealth is the process-wide circuit-breaker state for LLM failover.
	// It deliberately lives OUTSIDE the provider caches: rebuilding the chain
	// (saving an unrelated profile, flipping a setting) must not erase what we
	// learned about which backends are out of credit / rate-limited.
	llmHealth *llmpool.Registry

	// Explicit task chains use one task-scoped dynamic provider shared by goals,
	// planner, workers, and the main agent. Bundles are stable; their runtime reads
	// the persisted current profile at every new LLM call.
	taskAgentMu sync.Mutex
	taskAgents  map[string]*taskAgentBundle

	// Cold task archives run through one persistent FIFO worker. The buffered wake
	// channel coalesces enqueue bursts; the database remains the source of truth.
	archiveWake chan struct{}
	archiveWG   sync.WaitGroup
	side        *sideQuestionState
}

// provEntry is a cached provider + its config for one LLM profile id.
type provEntry struct {
	prov llm.Provider
	cfg  agent.Config
}

// triggeredRun is one queued P3 trigger fire awaiting its turn for an agent.
// taskID + mergeable let the drainer coalesce several event triggers (finding/goal)
// from the SAME task into one conversation before it starts (interval fires don't merge).
type triggeredRun struct {
	language  locale.Lang
	agentKey  string
	title     string
	message   string // Event body: trigger text, tool, arguments, and result; excludes task description/goal headers.
	taskID    int64  // source task for finding/goal triggers; 0 for interval/none
	taskDesc  string // Task description, shared within a task and rendered once when merging.
	taskGoal  string // Task goals, shared within a task and rendered once when merging.
	mergeable bool   // true for finding/goal event triggers (merge by taskID)
}

func New(ctx context.Context, m *Manager, skillDir string, dataDir string, keyDir string) *Server {
	s, err := newServer(ctx, m, skillDir, dataDir, keyDir)
	if err != nil {
		log.Fatalf("[server] startup: %v", err)
	}
	return s
}

func newServer(ctx context.Context, m *Manager, skillDir string, dataDir string, keyDir string) (*Server, error) {
	key, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		return nil, fmt.Errorf("JWT key: %w", err)
	}
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx, skillDir: skillDir, jwtKey: key, chatBusy: map[string]bool{},
		chatCancel: map[string]context.CancelCauseFunc{}, triggerQ: map[string][]triggeredRun{},
		triggerActive: map[string]int{}, triggerCfg: map[string]triggerBehavior{},
		profChatAgents: map[int64]*agent.ChatAgent{},
		provByProfile:  map[int64]*provEntry{}, llmHealth: newLLMHealthRegistry(m.pg),
		taskAgents: map[string]*taskAgentBundle{}, archiveWake: make(chan struct{}, 1)}
	// First-run protection: cache the password hash and, while none is set, issue
	// the console setup token (or apply ARTEX_ADMIN_PASSWORD) before any request.
	s.bootstrapAuth()
	// Recovery must finish before any internal producer or task runtime starts.
	if err := s.recoverChatControls(); err != nil {
		return nil, fmt.Errorf("chat startup recovery: %w", err)
	}
	s.initSideQuestions()
	// Circuit thresholds/cooldown are hot-path failure settings. Push the global retry policy into Registry
	// at startup and again whenever saveLLMRetryPolicy saves changes.
	s.applyRetryPolicy()
	// Every task uses a stable task router. An empty explicit chain is resolved by
	// that router through Agent bindings and then the global provider, so adding a
	// first chain to a running task takes effect on its very next LLM call.
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		if !s.taskRuntimeAvailable(t, "planner", "worker") {
			return nil, nil
		}
		b := s.agentsForTask(t)
		return b.pl, b.wk
	})
	// Global readiness (Ready()/llm_configured): a global active LLM provider is
	// installed. Task-level runnability is separate (ReadyFor → the resolver above).
	s.engine.SetReadiness(func() bool {
		s.cfgMu.Lock()
		defer s.cfgMu.Unlock()
		return s.llmOn
	})
	// Wire DB-stored prompt templates into agents (design sections 3.3 and 5a). With no
	// override row, agents keep their built-in defaults — behavior is unchanged.
	if m.pg != nil {
		agent.PromptOverride = func(key string) (string, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil {
				return "", false
			}
			t, err := m.pg.CurrentPrompt(a.ID)
			if err != nil || t == "" {
				return "", false
			}
			return t, true
		}
		// Wire DB-stored wrap-up (settlement) prompts. Empty column → built-in default.
		agent.WrapupOverride = func(key string) (string, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.WrapupPrompt == "" {
				return "", false
			}
			return a.WrapupPrompt, true
		}
		// Wire DB-stored wrap-up turn budgets. 0 / missing → built-in per-agent default.
		agent.WrapupMaxTurnsOverride = func(key string) (int, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.WrapupMaxTurns <= 0 {
				return 0, false
			}
			return a.WrapupMaxTurns, true
		}
		// Wire DB-stored TASK-TIMEOUT wrap-up prompt/turns (worker/planner). Empty/0 → default.
		agent.WrapupTaskTimeoutOverride = func(key string) (string, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.TaskTimeoutWrapupPrompt == "" {
				return "", false
			}
			return a.TaskTimeoutWrapupPrompt, true
		}
		agent.WrapupTaskTimeoutTurnsOverride = func(key string) (int, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.TaskTimeoutWrapupMaxTurns <= 0 {
				return 0, false
			}
			return a.TaskTimeoutWrapupMaxTurns, true
		}
		wireAgentAugment(m.pg, s.skillDir, s.hostTools) // Assemble visible skills/MCP and traffic/orchestration host tools into agent toolsets.
		domainReg := s.domainToolRegistry()
		wireTools(m.pg, domainReg, s.knownToolLanguages()) // Built-in tools: filter by agent, override descriptions/schemas, and inject defaults.
		seedPrompts(m.pg)                                  // Seed built-in agent prompt bodies into agent_prompts only when empty.
		s.seedOrchestrationTools()                         // Seed P2 cross-task orchestration tools for per-agent bindings.
		if err := s.seedFindingRetester(); err != nil {
			log.Printf("[retester] seed: %v", err)
		}
		go s.evidenceStore().RunGC(s.ctx)
		s.seedPythonInterpreter() // Detect and store the Python interpreter for custom scripts at startup, only when empty.
		// Finding notification delivery runs independently beside Scheduler: its three-second responsiveness
		// differs from trigger scheduling, and a stuck delivery must not block agent triggers.
		go newNotifier(s).Run(s.ctx)
		// Fill the tool cache for any enabled MCP that has none yet (notably the
		// seeded browser MCP on first run). Async so it never blocks startup.
		go s.discoverEmptyMCPsOnStartup()
		logSink.SetDB(ctx, m.pg) // restore last 100 log rows and enable async persistence
	}
	// precedence: persisted DB config > env.
	if cfg, ok := s.loadLLMConfig(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] saved LLM config init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from DB: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else if cfg, ok := agent.FromEnv(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] env provider init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from env: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else {
		log.Printf("[engine] no LLM provider configured — engine idle until set via /api/llm or env")
	}
	s.restoreTaskRuntimes()
	if err := s.resumeChatControls(); err != nil {
		log.Printf("[chat] %v", err)
	}
	go s.reconcileConcurrency()
	s.startTaskArchiveWorker()
	s.wireInterceptReviewer() // LLM fallback approval judges commands that match no interception rule.
	if m.pg != nil {
		go newScheduler(s).Run(s.ctx) // Start admissions only after recovery and task restoration.
	}
	return s, nil
}

// Restored deadline and worker loops must inherit the same context as new tasks,
// including the side-question checkpoint publisher installed during startup.
func (s *Server) restoreTaskRuntimes() {
	m := s.m
	// reload tasks persisted on disk so the task list survives a restart, and
	// restore persisted paused state (so a task paused before restart stays paused).
	for _, t := range m.LoadExisting() {
		lifecycle := t.lifecycleSnapshot()
		// clear stale 'running' intents from a prior crash/restart (no live worker
		// owns them) so they re-claim instead of spinning forever in the UI.
		if n, _ := t.Store.ResetRunningIntents(); n > 0 {
			log.Printf(locale.Text(locale.ServerDefault(), "[engine] task %s reset %d leftover running intents to open"), t.ID, n)
		}
		if lifecycle.Paused {
			s.engine.Pause(t.ID, agent.AbortPausedOnReload)
		}
		// Start deadline coordinators for every nonterminal timed task, independently of planner/worker loops.
		// Inactive tasks can thus settle after restart; already expired deadlines settle immediately.
		if !isTerminalStatus(lifecycle.Status) {
			s.engine.startDeadlineCoordinator(s.ctx, t)
		}
	}
	// Restore every task that had already been admitted before shutdown. Starting
	// only the active UI task left other non-queued tasks counted as concurrency
	// occupants without live loops, which could permanently block the persistent
	// FIFO. Paused loops remain idle; queued tasks are admitted below as slots allow.
	for _, t := range m.List() {
		lifecycle := t.lifecycleSnapshot()
		if !lifecycle.Queued && !isTerminalStatus(lifecycle.Status) {
			s.engine.Run(s.ctx, t)
		}
	}
}

// agentMaxTurns returns the configured max_turns for an agent key (0 = unlimited,
// also the fallback when no DB or no row).
func (s *Server) agentMaxTurns(key string) int {
	if s.m.pg == nil {
		return 0
	}
	a, err := s.m.pg.GetAgentByKey(key)
	if err != nil || a == nil {
		return 0
	}
	return a.MaxTurns
}

// agentRunSeconds returns the configured wall-clock run budget (seconds) for an
// agent key (0 = unlimited; 1200 fallback when no DB or no row, matching schema).
func (s *Server) agentRunSeconds(key string) int {
	if s.m.pg == nil {
		return 1200
	}
	a, err := s.m.pg.GetAgentByKey(key)
	if err != nil || a == nil {
		return 1200
	}
	return a.RunSecs
}

// loadLLMConfig reads the active LLM profile from PG (llm_profiles).
func (s *Server) loadLLMConfig() (agent.Config, bool) {
	p, err := s.m.pg.ActiveProfile()
	if err != nil || p == nil {
		return agent.Config{}, false
	}
	cfg := agent.ConfigFrom(p.Format, p.Model, p.BaseURL, p.APIKey, p.Proxy)
	cfg.RatePerSecond, cfg.RatePerMinute = p.RatePerSecond, p.RatePerMinute
	cfg.ContextWindowK = p.ContextWindowK
	cfg.ThinkingType = p.ThinkingType
	cfg.ReasoningEffort = p.ReasoningEffort
	cfg.Stream = p.Streaming
	cfg.MaxTokens, cfg.MaxTokensField = p.MaxTokens, p.MaxTokensField
	cfg.SessionHeaderKey = p.SessionHeaderKey
	cfg.KeepAlive, cfg.MaxConcurrent = p.KeepAlive, p.MaxConcurrent
	cfg = s.applyLLMPolicy(cfg)
	s.applyProfileRetry(&cfg, p)
	if !cfg.Ready() {
		return cfg, false
	}
	s.cfgMu.Lock()
	s.llmProf = p.Name
	s.cfgMu.Unlock()
	return cfg, true
}

// saveLLMConfig persists the LLM config as the active "default" profile in PG.
func (s *Server) saveLLMConfig(cfg agent.Config) error {
	// cfg.Provider() returns a supported format matching the DB CHECK constraint.
	format := cfg.Provider()
	var id int64
	// This legacy endpoint omits failover, transport, and output-limit parameters, so preserve stored values.
	// Otherwise saving would silently reset priority, pool_exclude, streaming, and output limits
	// (max_tokens / max_tokens_field) to zero values.
	var priority int
	var poolExclude bool
	streaming := true // Existing databases and new profiles default to streaming.
	var maxTokens int
	var maxTokensField string
	sessionHeaderKey := cfg.SessionHeaderKey
	var retry db.RetryOverride
	if profs, _ := s.m.pg.ListProfiles(); profs != nil {
		for _, p := range profs {
			if p.Name == "default" {
				id, priority, poolExclude, streaming = p.ID, p.Priority, p.PoolExclude, p.Streaming
				maxTokens, maxTokensField = p.MaxTokens, p.MaxTokensField
				if sessionHeaderKey == "" {
					sessionHeaderKey = p.SessionHeaderKey
				}
				retry = p.Retry
				if cfg.KeepAlive == "" {
					cfg.KeepAlive = p.KeepAlive
				}
				if cfg.MaxConcurrent == 0 {
					cfg.MaxConcurrent = p.MaxConcurrent
				}
				break
			}
		}
	}
	newID, err := s.m.pg.SaveProfile(&db.LLMProfile{
		ID: id, Name: "default", Format: format, Model: cfg.Model, BaseURL: cfg.BaseURL, Proxy: cfg.Proxy,
		APIKey: cfg.APIKey, RatePerSecond: cfg.RatePerSecond, RatePerMinute: cfg.RatePerMinute,
		ContextWindowK: cfg.ContextWindowK, ThinkingType: cfg.ThinkingType, ReasoningEffort: cfg.ReasoningEffort, IsDefault: true,
		Priority: priority, PoolExclude: poolExclude, Streaming: streaming,
		MaxTokens: maxTokens, MaxTokensField: maxTokensField, SessionHeaderKey: sessionHeaderKey,
		Retry: retry, KeepAlive: cfg.KeepAlive, MaxConcurrent: cfg.MaxConcurrent,
	})
	if err != nil {
		return err
	}
	return s.m.pg.SetActiveProfile(newID)
}

// reapplyActiveProfile hot-reloads the engine from the active DB profile so that
// saving or activating a profile takes effect without a restart. Best-effort:
// logs on failure and leaves the running engine untouched.
func (s *Server) reapplyActiveProfile() {
	cfg, ok := s.loadLLMConfig()
	if !ok {
		return
	}
	if err := s.applyLLM(cfg); err != nil {
		log.Printf("[engine] reapply active profile failed: %v", err)
		return
	}
	log.Printf("[engine] LLM reapplied from active profile: %s / %s", cfg.Provider(), cfg.Model)
}

// webSearchFor gates the global web-search opts (backend/key) by an agent's own
// web_search flag: the backend/key come from the global config, each agent decides on/off.
func (s *Server) webSearchFor(key string) agent.WebSearchOpts {
	o := s.m.WebSearchOpts()
	if a, err := s.m.pg.GetAgentByKey(key); err != nil || a == nil || !a.WebSearch {
		o.Enabled = false
	}
	return o
}

// nonStreamingResolver returns a resolver capturing a profile's streaming choice.
// The agents read it per run; a profile change rebuilds the agents (applyLLM),
// so the captured value is always the one in effect for this build.
func nonStreamingResolver(cfg agent.Config) func() bool {
	nonStreaming := !cfg.Stream
	return func() bool { return nonStreaming }
}

// maxTokensResolver mirrors nonStreamingResolver for the per-reply output cap.
func maxTokensResolver(cfg agent.Config) func() int {
	maxTokens := cfg.MaxTokens
	return func() int { return maxTokens }
}

// applyLLM (re)builds the planner/worker/main-agent from cfg and installs them on
// the running engine as the GLOBAL active pair. Safe to call at runtime (UI configures LLM).
func sameLLMConfig(a, b agent.Config) bool {
	a.PolicyResolver = nil
	b.PolicyResolver = nil
	return reflect.DeepEqual(a, b)
}

func (s *Server) applyLLM(cfg agent.Config) error {
	cfg = s.applyLLMPolicy(cfg)
	var prov llm.Provider
	// A persisted active profile must use the same cached provider as task chains
	// and Agent bindings, otherwise each path owns a separate rate limiter.
	if active, _ := s.m.pg.ActiveProfile(); active != nil {
		if activeCfg, ok := s.loadProfileConfig(active.ID); ok && sameLLMConfig(activeCfg, cfg) {
			prov, _, _ = s.providerForProfile(active.ID)
		}
	}
	if prov == nil {
		var err error
		prov, err = cfg.NewProvider()
		if err != nil {
			return err
		}
		// Non-persisted/env configs do not have a profile cache key.
		s.cfgMu.Lock()
		profName := s.llmProf
		s.cfgMu.Unlock()
		prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, profName, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled)
		prov = bindSideProvider(prov, cfg, 0, profName)
		// Process-wide in-flight cap (llm_max_concurrent); no-op when unset.
		prov = s.profileConcurrencyProvider(prov, cfg)
	}
	s.cfgMu.Lock()
	s.llmDirect = prov
	s.cfgMu.Unlock()
	// LLM failover, disabled by default, wraps the active profile in a chain and switches when unavailable.
	// This affects only global-active routing. Agent bindings/task pins use providerForProfile and remain
	// exclusive by default (see poolForBinding). Disabled/no alternatives returns the original provider.
	if act, err := s.m.pg.ActiveProfile(); err == nil && act != nil {
		prov = s.poolForActive(act.ID, prov, cfg)
	}
	// No global planner/worker pair: every task runs on its own task-routed pair
	// (agentsForTask), resolved through the engine's authoritative resolver. Global
	// readiness (Ready()/llm_configured) is reported from s.llmOn, set below.

	tx := transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))
	win := cfg.CompactionWindow()
	// chat stays the GLOBAL fallback since one ChatAgent serves many agent keys — its
	// per-agent binding is resolved at Chat time (runConversationSync →
	// chatAgentForProfile). The main-agent has no global instance: each task builds its
	// own via agentsForTask (task-routed), so nothing is constructed for it here.
	s.cfgMu.Lock()
	// chat agent serves MANY custom agents by key → it holds the GLOBAL opts
	// (backend/key) and gates Enabled per conversation agent at Chat time. Conversations use the active profile.
	s.chatAgent = agent.NewChatAgent(prov, cfg.Model, s.m.dir, tx, win) // chat page runner
	s.chatAgent.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	s.chatAgent.SetWebSearch(s.m.WebSearchOpts())
	s.chatAgent.SetGuard(s.chatGuard())
	s.chatAgent.SetNonStreaming(nonStreamingResolver(cfg))
	s.chatAgent.SetMaxTokens(maxTokensResolver(cfg))
	s.chatAgent.SetNativeOllamaResolver(func() bool { return cfg.Format == llm.Format("ollama") })
	s.chatAgent.SetNoaEnabled(s.m.NoaCompactionEnabled) // Experimental noa compaction, read once per run.
	s.llmProv = prov
	s.llmCfg = cfg
	s.llmOn = true
	s.cfgMu.Unlock()
	s.invalidateTaskAgents()

	// wake the active task so a task created while idle starts exploring.
	if t := s.m.ActiveTask(); t != nil {
		t.Notify()
	}
	return nil
}

// loadProfileConfig builds an agent.Config from a specific profile id (with its key).
// ok=false when the profile is missing or not ready for its provider.
func (s *Server) loadProfileConfig(id int64) (agent.Config, bool) {
	p, err := s.m.pg.ProfileByID(id)
	if err != nil || p == nil {
		return agent.Config{}, false
	}
	cfg := agent.ConfigFrom(p.Format, p.Model, p.BaseURL, p.APIKey, p.Proxy)
	cfg.RatePerSecond, cfg.RatePerMinute = p.RatePerSecond, p.RatePerMinute
	cfg.ContextWindowK = p.ContextWindowK
	cfg.ThinkingType = p.ThinkingType
	cfg.ReasoningEffort = p.ReasoningEffort
	cfg.Stream = p.Streaming
	cfg.MaxTokens, cfg.MaxTokensField = p.MaxTokens, p.MaxTokensField
	cfg.SessionHeaderKey = p.SessionHeaderKey
	cfg.KeepAlive, cfg.MaxConcurrent = p.KeepAlive, p.MaxConcurrent
	cfg = s.applyLLMPolicy(cfg)
	s.applyProfileRetry(&cfg, p)
	if !cfg.Ready() {
		return cfg, false
	}
	return cfg, true
}

// effectiveProfileForAgent resolves the LLM profile id an agent should run on, by
// precedence: agent binding (agents.llm_profile_id) → pin (task/conversation) → nil
// (caller falls back to the global active profile). A binding to a deleted profile
// can't happen (FK ON DELETE SET NULL); an otherwise-invalid one is dropped downstream
// by loadProfileConfig, letting the caller fall back.
func (s *Server) effectiveProfileForAgent(agentKey string, pinID *int64) *int64 {
	if s.m.pg != nil && agentKey != "" {
		if a, _ := s.m.pg.GetAgentByKey(agentKey); a != nil && a.LLMProfileID != nil {
			return a.LLMProfileID
		}
	}
	return pinID
}

// resolveChatAgent picks the ChatAgent for one conversation: the agent's own
// binding or this conversation's chosen profile first, the global active config
// only as a fallback. Both the send precheck and the background runner MUST use
// this — resolving differently in the two paths is how a conversation that had
// picked a valid profile still got rejected as LLM unconfigured when no global
// config was active.
func (s *Server) resolveChatAgent(c *db.Conversation) *agent.ChatAgent {
	ca := s.chatAgentRef()
	if eff := s.effectiveProfileForAgent(c.AgentKey, c.LLMProfileID); eff != nil {
		if pa := s.chatAgentForProfile(*eff); pa != nil {
			ca = pa
		}
	}
	return ca
}

// chatUnavailableReason explains why no ChatAgent could be resolved, so the user
// knows whether to add a config, activate one, or pick one for this conversation
// — rather than a flat "not configured" that hides which of those it is.
func (s *Server) chatUnavailableReason(langs ...locale.Lang) string {
	if s.m.pg != nil {
		if profiles, err := s.m.pg.ListProfiles(); err == nil && len(profiles) == 0 {
			return locale.Text(locale.First(langs), "No LLM is configured. Add a profile under System → LLM Profiles.")
		}
		if active, err := s.m.pg.ActiveProfile(); err == nil && active == nil {
			return locale.Text(locale.First(langs), "No active LLM profile. Activate one under System → LLM Profiles, or select a profile for this conversation.")
		}
	}
	return locale.Text(locale.First(langs), "LLM is not ready for chat. Check for an available active profile under System → LLM Profiles.")
}

// providerForProfile returns a cached provider+cfg for a profile id, so every agent
// bound/pinned to the same profile shares one provider instance (one rate limiter).
// ok=false when the profile is missing/invalid → caller falls back to the global pair.
func (s *Server) providerForProfile(id int64) (llm.Provider, agent.Config, bool) {
	s.provCacheMu.Lock()
	if e := s.provByProfile[id]; e != nil {
		s.provCacheMu.Unlock()
		return e.prov, e.cfg, true
	}
	generation := s.provCacheGen
	s.provCacheMu.Unlock()
	cfg, ok := s.loadProfileConfig(id)
	if !ok {
		return nil, agent.Config{}, false
	}
	prov, err := cfg.NewProvider()
	if err != nil {
		log.Printf("[engine] build provider for LLM profile %d failed: %v", id, err)
		return nil, agent.Config{}, false
	}
	// Wrap with recorder, tagged with this profile's name.
	if p, _ := s.m.pg.ProfileByID(id); p != nil {
		prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, p.Name, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled)
		prov = bindSideProvider(prov, cfg, id, p.Name)
	}
	// Process-wide in-flight cap (llm_max_concurrent); no-op when unset.
	prov = s.profileConcurrencyProvider(prov, cfg)
	s.provCacheMu.Lock()
	if generation != s.provCacheGen {
		s.provCacheMu.Unlock()
		return s.providerForProfile(id)
	}
	if e := s.provByProfile[id]; e != nil { // lost the race → keep the winner
		prov, cfg = e.prov, e.cfg
	} else {
		s.provByProfile[id] = &provEntry{prov: prov, cfg: cfg}
		log.Printf("[engine] built provider for LLM profile %d (%s / %s)", id, cfg.Provider(), cfg.Model)
	}
	s.provCacheMu.Unlock()
	return prov, cfg, true
}

// chatAgentForProfile returns a ChatAgent built from a specific LLM profile, cached
// per profile id. Returns nil if the profile is missing or not ready.
func (s *Server) chatAgentForProfile(id int64) *agent.ChatAgent {
	s.profMu.Lock()
	cached := s.profChatAgents[id]
	s.profMu.Unlock()
	if cached != nil {
		return cached
	}
	prov, cfg, ok := s.providerForProfile(id)
	if !ok {
		return nil
	}
	tx := transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))
	ca := agent.NewChatAgent(s.poolForBinding(id, prov, cfg), cfg.Model, s.m.dir, tx, cfg.CompactionWindow())
	ca.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	ca.SetWebSearch(s.m.WebSearchOpts())
	ca.SetGuard(s.chatGuard())
	ca.SetNonStreaming(nonStreamingResolver(cfg))
	ca.SetMaxTokens(maxTokensResolver(cfg))
	ca.SetNativeOllamaResolver(func() bool { return cfg.Format == llm.Format("ollama") })
	ca.SetNoaEnabled(s.m.NoaCompactionEnabled) // Experimental noa compaction, read once per run.
	s.profMu.Lock()
	if ex := s.profChatAgents[id]; ex != nil { // lost the race → keep the winner
		ca = ex
	} else {
		s.profChatAgents[id] = ca
	}
	s.profMu.Unlock()
	return ca
}

// invalidateProfileAgents drops the per-profile ChatAgent + provider caches so a profile
// save/activate/delete — or an agent's binding change — rebuilds pinned tasks' agents
// (and re-resolves each agent's bound model) on their next round.
func (s *Server) invalidateProfileAgents() {
	s.profMu.Lock()
	s.profChatAgents = map[int64]*agent.ChatAgent{}
	s.profMu.Unlock()
	s.provCacheMu.Lock()
	s.provCacheGen++
	s.provByProfile = map[int64]*provEntry{}
	s.provCacheMu.Unlock()
	s.invalidateTaskAgents()
}

func (s *Server) chatAgentRef() *agent.ChatAgent {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.chatAgent
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerSideRoutes(mux)

	// Auth routes — exempt from JWT check (handled in requireAuth)
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/init", s.authInit)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/change-password", s.authChangePassword)

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/logs", s.getLogs)
	mux.HandleFunc("GET /api/logs/history", s.getLogsHistory)
	mux.HandleFunc("GET /api/logs/stream", s.streamLogs)

	// One-click updates use normal JWT authentication. auth.go exempts only /api/auth/* and
	// /api/health, so endpoints modifying the running application require login.
	mux.HandleFunc("GET /api/update/check", s.updateCheck)
	mux.HandleFunc("POST /api/update/apply", s.updateApply)
	mux.HandleFunc("POST /api/update/rollback", s.updateRollback)
	mux.HandleFunc("GET /api/update/stream", s.updateStream)

	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("GET /api/task-categories", s.pgListTaskCategories)
	mux.HandleFunc("POST /api/task-categories", s.pgCreateTaskCategory)
	mux.HandleFunc("PATCH /api/task-categories/{id}", s.pgRenameTaskCategory)
	mux.HandleFunc("DELETE /api/task-categories/{id}", s.pgDeleteTaskCategory)
	mux.HandleFunc("POST /api/tasks/category/batch", s.updateTasksCategoryBatch)
	mux.HandleFunc("GET /api/task-templates", s.pgListTaskTemplates)
	mux.HandleFunc("POST /api/task-templates", s.pgCreateTaskTemplate)
	mux.HandleFunc("PATCH /api/task-templates/{id}", s.pgUpdateTaskTemplate)
	mux.HandleFunc("DELETE /api/task-templates/{id}", s.pgDeleteTaskTemplate)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.updateTaskMetadata)
	mux.HandleFunc("PATCH /api/tasks/{id}/category", s.updateTaskCategory)
	// Task-specific asset interception/allow rules.
	mux.HandleFunc("GET /api/tasks/{id}/intercept-rules", s.taskInterceptListRules)
	mux.HandleFunc("POST /api/tasks/{id}/intercept-rules", s.taskInterceptCreateRule)
	mux.HandleFunc("PUT /api/tasks/{id}/intercept-rules/{rid}", s.taskInterceptUpdateRule)
	mux.HandleFunc("DELETE /api/tasks/{id}/intercept-rules/{rid}", s.taskInterceptDeleteRule)
	mux.HandleFunc("POST /api/tasks/{id}/intercept-rules/{rid}/toggle", s.taskInterceptToggleRule)
	mux.HandleFunc("POST /api/tasks/control/batch", s.controlTasksBatch)
	mux.HandleFunc("GET /api/task-archives", s.listTaskArchives)
	mux.HandleFunc("GET /api/task-archives/{id}", s.getTaskArchive)
	mux.HandleFunc("POST /api/tasks/{id}/archive", s.queueTaskArchive)
	mux.HandleFunc("POST /api/tasks/archive/batch", s.queueTaskArchivesBatch)
	mux.HandleFunc("POST /api/task-archives/{id}/restore", s.queueTaskArchiveRestore)
	mux.HandleFunc("POST /api/task-archives/restore/batch", s.restoreTaskArchivesBatch)
	mux.HandleFunc("DELETE /api/task-archives/{id}", s.queueTaskArchiveDelete)
	mux.HandleFunc("POST /api/task-archives/delete/batch", s.deleteTaskArchivesBatch)
	mux.HandleFunc("GET /api/tasks/{id}/coverage", s.taskCoverage)
	mux.HandleFunc("GET /api/tasks/{id}/coverage-graph", s.taskCoverageGraph)
	mux.HandleFunc("GET /api/tasks/{id}/asset-refs", s.taskAssetRefs)
	mux.HandleFunc("POST /api/tasks/{id}/assets", s.attachTaskAssets)
	mux.HandleFunc("DELETE /api/tasks/{id}/assets/{assetID}", s.detachTaskAsset)
	mux.HandleFunc("GET /api/tasks/{id}/intent-assets", s.taskIntentAssets)

	// Workspace file manager rooted at workDir.
	mux.HandleFunc("GET /api/workspace/list", s.wsList)
	mux.HandleFunc("GET /api/workspace/read", s.wsRead)
	mux.HandleFunc("POST /api/workspace/write", s.wsWrite)
	mux.HandleFunc("POST /api/workspace/mkdir", s.wsMkdir)
	mux.HandleFunc("DELETE /api/workspace/delete", s.wsDelete)
	mux.HandleFunc("GET /api/workspace/download", s.wsDownload)
	mux.HandleFunc("POST /api/workspace/upload", s.wsUpload)
	mux.HandleFunc("GET /api/tasks/{id}/scope", s.taskScopeList)
	mux.HandleFunc("POST /api/tasks/{id}/scope", s.taskScopeAdd)
	mux.HandleFunc("DELETE /api/tasks/{id}/scope/{sid}", s.taskScopeDelete)
	mux.HandleFunc("GET /api/tasks/{id}/goals", s.listGoals)                       // Goal management: list all task goals.
	mux.HandleFunc("POST /api/tasks/{id}/goals", s.addGoal)                        // Goal management: add a goal manually and revive the task.
	mux.HandleFunc("PATCH /api/tasks/{id}/goals/{gid}", s.editGoal)                // Goal management: edit a goal and revive the task.
	mux.HandleFunc("DELETE /api/tasks/{id}/goals/{gid}", s.deleteGoal)             // Goal management: hard-delete a goal without revival.
	mux.HandleFunc("GET /api/tasks/{id}/constraints", s.listConstraints)           // Constraint management: list task operation constraints.
	mux.HandleFunc("POST /api/tasks/{id}/constraints", s.addConstraint)            // Constraint management: add without notifying the planner.
	mux.HandleFunc("PATCH /api/tasks/{id}/constraints/{cid}", s.editConstraint)    // Constraint management: edit.
	mux.HandleFunc("DELETE /api/tasks/{id}/constraints/{cid}", s.deleteConstraint) // Constraint management: delete.
	mux.HandleFunc("POST /api/tasks/{id}/control", s.control)
	mux.HandleFunc("PUT /api/tasks/{id}/llm", s.updateTaskLLMProfiles)
	mux.HandleFunc("GET /api/tasks/{id}/llm/resolution", s.taskLLMResolutionHandler)
	mux.HandleFunc("POST /api/tasks/{id}/intents/{iid}/control", s.controlIntent)
	mux.HandleFunc("POST /api/tasks/{id}/intents/{iid}/messages", s.sendWorkerMessage)
	mux.HandleFunc("POST /api/tasks/{id}/intents/{iid}/rerun", s.rerunIntent)    // Rerun one blocked/exhausted/stopped intent.
	mux.HandleFunc("POST /api/tasks/{id}/intents/rerun-blocked", s.rerunBlocked) // Rerun all blocked intents in the task.
	mux.HandleFunc("POST /api/active", s.setActive)

	mux.HandleFunc("GET /api/llm", s.getLLM)
	mux.HandleFunc("POST /api/llm", s.setLLM)
	mux.HandleFunc("POST /api/llm/test", s.testLLM)

	// asset system
	mux.HandleFunc("GET /api/assets", s.listAssets)
	mux.HandleFunc("GET /api/assets/counts", s.assetCounts)
	mux.HandleFunc("GET /api/assets/export", s.assetsExport)
	mux.HandleFunc("POST /api/assets", s.insertAssets)
	mux.HandleFunc("DELETE /api/assets", s.deleteAssets)

	// company system
	mux.HandleFunc("GET /api/companies", s.listCompanies)
	mux.HandleFunc("POST /api/companies", s.createCompany)
	mux.HandleFunc("GET /api/companies/{id}", s.getCompany)
	mux.HandleFunc("DELETE /api/companies/{id}", s.deleteCompany)
	mux.HandleFunc("POST /api/companies/{id}/scope", s.addCompanyScope)
	mux.HandleFunc("POST /api/companies/reattribute", s.reattribute)

	mux.HandleFunc("GET /api/exploration/frontier", s.frontier)
	mux.HandleFunc("GET /api/exploration/findings", s.findings)
	mux.HandleFunc("GET /api/exploration/findings/groups", s.findingGroups)
	mux.HandleFunc("GET /api/exploration/findings/asset-tree", s.findingAssetTree)
	mux.HandleFunc("GET /api/exploration/findings/stats", s.findingStats)
	mux.HandleFunc("GET /api/exploration/findings/export", s.findingsExport)
	s.registerFindingTraffic(mux)
	mux.HandleFunc("GET /api/exploration/findings/{id}", s.getFinding)
	mux.HandleFunc("GET /api/exploration/findings/{id}/lineage", s.findingLineage)
	mux.HandleFunc("POST /api/exploration/findings/{id}/deepen", s.deepenFinding)
	mux.HandleFunc("GET /api/exploration/findings/{id}/retests", s.listFindingRetests)
	mux.HandleFunc("GET /api/exploration/findings/retests/active", s.listActiveFindingRetests)
	mux.HandleFunc("POST /api/exploration/findings/{id}/retests", s.startFindingRetest)
	mux.HandleFunc("PATCH /api/exploration/findings/{id}", s.patchFinding)
	mux.HandleFunc("DELETE /api/exploration/findings/{id}", s.deleteFinding)
	mux.HandleFunc("GET /api/exploration/intents", s.intents)
	mux.HandleFunc("GET /api/exploration/graph", s.explorationGraph)
	mux.HandleFunc("GET /api/exploration/nodes", s.explorationNodes)
	mux.HandleFunc("GET /api/exploration/activity", s.activity)
	mux.HandleFunc("GET /api/exploration/activity/history", s.activityHistory)
	mux.HandleFunc("GET /api/exploration/main-sessions", s.mainSessions)
	mux.HandleFunc("POST /api/exploration/main-session/new", s.newMainSession)
	mux.HandleFunc("GET /api/exploration/activity/stream", s.streamActivity)
	mux.HandleFunc("GET /api/exploration/activity/{seq}", s.activityDetail)
	mux.HandleFunc("GET /api/exploration/tokens", s.tokenStats)
	mux.HandleFunc("GET /api/tokens/daily", s.tokenDailyStats)
	mux.HandleFunc("GET /api/tokens/conversations", s.conversationTokens)
	mux.HandleFunc("GET /api/tokens/usage", s.pgUsageStats) // Global llm_usage aggregation for the dashboard.

	mux.HandleFunc("GET /api/audit", s.getAudit)
	mux.HandleFunc("POST /api/gc", s.gc)
	mux.HandleFunc("GET /api/traffic", s.getTraffic)
	mux.HandleFunc("GET /api/traffic/hosts", s.getTrafficHosts)
	mux.HandleFunc("DELETE /api/traffic", s.deleteTraffic)
	mux.HandleFunc("DELETE /api/traffic/hosts", s.deleteTrafficHosts)
	mux.HandleFunc("DELETE /api/traffic/all", s.deleteAllTraffic)
	mux.HandleFunc("GET /api/traffic/exchange", s.getTrafficExchange)
	mux.HandleFunc("GET /api/traffic/blob", s.getTrafficBlob)
	mux.HandleFunc("GET /api/commands", s.pgListCommands)
	mux.HandleFunc("GET /api/commands/stats", s.pgToolStats) // Aggregate invocation counts by tool.
	mux.HandleFunc("GET /api/llm/records", s.pgListLLMRecords)
	mux.HandleFunc("DELETE /api/llm/records", s.pgDeleteLLMRecords)
	mux.HandleFunc("GET /api/llm/records/tasks", s.pgLLMTasks)
	mux.HandleFunc("GET /api/llm/records/by-model", s.pgTokenByModel) // Aggregate task token usage by model.
	mux.HandleFunc("GET /api/llm/records/{id}", s.pgGetLLMRecord)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	// Finding notification channels are independent resources with multiple instances and individual filters,
	// so they use dedicated REST endpoints rather than flat /api/settings keys.
	mux.HandleFunc("GET /api/notify/meta", s.notifyMeta)
	mux.HandleFunc("GET /api/notify/channels", s.notifyListChannels)
	mux.HandleFunc("POST /api/notify/channels", s.notifyCreateChannel)
	mux.HandleFunc("PATCH /api/notify/channels/{id}", s.notifyUpdateChannel)
	mux.HandleFunc("DELETE /api/notify/channels/{id}", s.notifyDeleteChannel)
	mux.HandleFunc("POST /api/notify/channels/{id}/test", s.notifyTestChannel)
	mux.HandleFunc("GET /api/notify/deliveries", s.notifyListDeliveries)
	mux.HandleFunc("POST /api/notify/deliveries/{id}/retry", s.notifyRetryDelivery)
	mux.HandleFunc("POST /api/settings/web-search/test", s.testWebSearch)
	mux.HandleFunc("GET /api/report", s.getReport)
	mux.HandleFunc("GET /api/chat/mentions", s.searchChatMentions)
	mux.HandleFunc("/api/conversations/{id}/controls", s.chatControls)
	mux.HandleFunc("/api/conversations/{id}/controls/{rest...}", s.chatControls)
	mux.HandleFunc("/api/tasks/{id}/chat/controls", s.chatControls)
	mux.HandleFunc("/api/tasks/{id}/chat/controls/{rest...}", s.chatControls)
	mux.HandleFunc("POST /api/chat", s.chat)
	mux.HandleFunc("POST /api/chat/upload", s.chatUpload) // Upload mode 1: save under the conversation/task workspace uploads directory.
	mux.HandleFunc("GET /api/tasks/{id}/chat/status", s.taskChatStatus)
	mux.HandleFunc("POST /api/tasks/{id}/chat/stop", s.stopChat)

	// --- PostgreSQL-backed administration API ---
	mux.HandleFunc("DELETE /api/tasks/{id}", s.pgDeleteTask)
	// Agents
	// conversations (chat page)
	mux.HandleFunc("GET /api/conversations", s.pgListConversations)
	mux.HandleFunc("POST /api/conversations", s.pgCreateConversation)
	mux.HandleFunc("POST /api/conversations/delete/batch", s.pgDeleteConversationsBatch)
	mux.HandleFunc("PATCH /api/conversations/{id}", s.pgRenameConversation)
	mux.HandleFunc("PATCH /api/conversations/{id}/profile", s.pgUpdateConversation)
	mux.HandleFunc("DELETE /api/conversations/{id}", s.pgDeleteConversation)
	mux.HandleFunc("GET /api/conversations/{id}/messages", s.pgConversationMessages)
	mux.HandleFunc("POST /api/conversations/{id}/messages", s.pgSendConversationMessage)
	mux.HandleFunc("POST /api/conversations/{id}/stop", s.pgStopConversation)
	mux.HandleFunc("GET /api/conversations/{id}/messages/{seq}", s.pgConversationMsgDetail)

	mux.HandleFunc("GET /api/agents", s.pgListAgents)
	mux.HandleFunc("POST /api/agents", s.pgCreateAgent)
	mux.HandleFunc("GET /api/agents/{key}", s.pgGetAgent)
	mux.HandleFunc("PATCH /api/agents/{key}", s.pgUpdateAgent)
	mux.HandleFunc("DELETE /api/agents/{key}", s.pgDeleteAgent)
	mux.HandleFunc("PUT /api/agents/{key}/config", s.pgSaveAgentConfig)
	mux.HandleFunc("PUT /api/agents/{key}/prompt", s.pgSavePrompt)
	mux.HandleFunc("POST /api/agents/{key}/prompt/reset", s.pgResetPrompt)
	mux.HandleFunc("PUT /api/agents/{key}/wrapup", s.pgSaveWrapup)
	mux.HandleFunc("POST /api/agents/{key}/wrapup/reset", s.pgResetWrapup)
	mux.HandleFunc("PUT /api/agents/{key}/wrapup/task-timeout", s.pgSaveTaskTimeoutWrapup)
	mux.HandleFunc("POST /api/agents/{key}/wrapup/task-timeout/reset", s.pgResetTaskTimeoutWrapup)
	mux.HandleFunc("GET /api/agents/{key}/triggers", s.pgListTriggers)
	mux.HandleFunc("POST /api/agents/{key}/triggers", s.pgCreateTrigger)
	mux.HandleFunc("PATCH /api/triggers/{id}", s.pgUpdateTrigger)
	mux.HandleFunc("DELETE /api/triggers/{id}", s.pgDeleteTrigger)
	mux.HandleFunc("GET /api/agents/{key}/prompts", s.pgListPromptVersions)
	mux.HandleFunc("GET /api/agents/{key}/variables", s.pgPromptVars)
	mux.HandleFunc("POST /api/agents/{key}/prompt/preview", s.pgPreviewPrompt)
	mux.HandleFunc("GET /api/agents/{key}/visibility", s.pgGetAgentVisibility)
	mux.HandleFunc("PUT /api/agents/{key}/visibility", s.pgSetAgentVisibility)
	// Built-in tool catalog: editable descriptions/default arguments and agent bindings; keys/handlers stay in code.
	mux.HandleFunc("GET /api/tools", s.pgListTools)
	mux.HandleFunc("PUT /api/tools/{key}", s.pgUpdateTool)
	mux.HandleFunc("POST /api/tools/custom", s.pgCreateCustomTool)
	mux.HandleFunc("POST /api/tools/custom/test", s.pgTestCustomTool)
	mux.HandleFunc("PUT /api/tools/custom/{key}", s.pgUpdateCustomTool)
	mux.HandleFunc("DELETE /api/tools/custom/{key}", s.pgDeleteCustomTool)
	mux.HandleFunc("POST /api/settings/python/detect", s.pgDetectPython)
	mux.HandleFunc("POST /api/tools/{key}/reset", s.pgResetTool)
	// MCP CRUD
	mux.HandleFunc("GET /api/mcp", s.pgListMCP)
	mux.HandleFunc("POST /api/mcp", s.pgSaveMCP)
	mux.HandleFunc("DELETE /api/mcp/{id}", s.pgDeleteMCP)
	mux.HandleFunc("GET /api/mcp/{id}/tools", s.pgMCPTools)
	mux.HandleFunc("POST /api/mcp/{id}/refresh", s.pgRefreshMCP)
	// Asset synchronization from ScopeSentry.
	mux.HandleFunc("GET /api/sync/scopesentry/status", s.syncSSStatus)
	mux.HandleFunc("POST /api/sync/scopesentry/datasource", s.syncSSDatasource)
	mux.HandleFunc("GET /api/sync/scopesentry/projects", s.syncSSProjects)
	mux.HandleFunc("GET /api/sync/scopesentry/tasks", s.syncSSTasks)
	mux.HandleFunc("POST /api/sync/scopesentry/sync", s.syncSSRun)
	// Filesystem-backed skill CRUD.
	mux.HandleFunc("GET /api/skills", s.fsListSkills)
	mux.HandleFunc("POST /api/skills", s.fsCreateSkill)
	mux.HandleFunc("POST /api/skills/upload", s.fsUploadSkill)
	mux.HandleFunc("DELETE /api/skills/{name}", s.fsDeleteSkill)
	mux.HandleFunc("GET /api/skills/missing", s.fsMissingSkills)   // Requested skill names that do not exist.
	mux.HandleFunc("GET /api/skills/{name}/usage", s.fsSkillUsage) // Recent calls for one skill.
	mux.HandleFunc("PUT /api/skills/{name}/meta", s.fsUpdateSkillMeta)
	mux.HandleFunc("POST /api/skills/{name}/dirs", s.fsCreateDir)
	mux.HandleFunc("GET /api/skills/{name}/files", s.fsListFiles)
	// {file...} captures path segments including slashes (e.g. scripts/extract.py)
	mux.HandleFunc("GET /api/skills/{name}/files/{file...}", s.fsReadFile)
	mux.HandleFunc("PUT /api/skills/{name}/files/{file...}", s.fsWriteFile)
	mux.HandleFunc("DELETE /api/skills/{name}/files/{file...}", s.fsDeletePath)
	// MCP resource visibility; more specific skill routes take precedence.
	mux.HandleFunc("GET /api/visibility/{kind}/{id}", s.pgResourceVisibility)
	mux.HandleFunc("POST /api/visibility/toggle", s.pgToggleVisibility)
	// Skill visibility by name takes precedence over the wildcard route above.
	mux.HandleFunc("GET /api/visibility/skill/{name}", s.pgSkillVisibility)
	mux.HandleFunc("POST /api/visibility/skill/toggle", s.pgToggleSkillVisibility)
	// Multiple LLM profiles.
	mux.HandleFunc("GET /api/llm/profiles", s.pgListProfiles)
	mux.HandleFunc("POST /api/llm/profiles", s.pgSaveProfile)
	mux.HandleFunc("DELETE /api/llm/profiles/{id}", s.pgDeleteProfile)
	mux.HandleFunc("POST /api/llm/profiles/active", s.pgActivateProfile)
	mux.HandleFunc("GET /api/llm/retry-policy", s.pgGetLLMRetryPolicy)
	mux.HandleFunc("POST /api/llm/retry-policy", s.pgSaveLLMRetryPolicy)
	mux.HandleFunc("GET /api/llm/pool", s.pgLLMPoolStatus)
	mux.HandleFunc("POST /api/llm/pool/reset", s.pgLLMPoolReset)
	mux.HandleFunc("POST /api/llm/models", s.pgListModels)

	// Interception-rule management.
	mux.HandleFunc("GET /api/intercept/rules", s.interceptListRules)
	mux.HandleFunc("POST /api/intercept/rules", s.interceptCreateRule)
	mux.HandleFunc("PUT /api/intercept/rules/{id}", s.interceptUpdateRule)
	mux.HandleFunc("DELETE /api/intercept/rules/{id}", s.interceptDeleteRule)
	mux.HandleFunc("POST /api/intercept/rules/{id}/toggle", s.interceptToggleRule)

	// Global asset interception rules for domains/IPs/URLs/CIDRs.
	mux.HandleFunc("GET /api/asset-intercept/rules", s.assetInterceptListRules)
	mux.HandleFunc("POST /api/asset-intercept/rules", s.assetInterceptCreateRule)
	mux.HandleFunc("PUT /api/asset-intercept/rules/{id}", s.assetInterceptUpdateRule)
	mux.HandleFunc("DELETE /api/asset-intercept/rules/{id}", s.assetInterceptDeleteRule)
	mux.HandleFunc("POST /api/asset-intercept/rules/{id}/toggle", s.assetInterceptToggleRule)

	mux.HandleFunc("GET /api/intercept/pending", s.interceptListPending)
	mux.HandleFunc("GET /api/intercept/pending/{id}", s.interceptGetOne)
	mux.HandleFunc("POST /api/intercept/pending/{id}/decide", s.interceptDecide)
	mux.HandleFunc("GET /api/intercept/history", s.interceptHistory)
	mux.HandleFunc("GET /api/intercept/history/{id}", s.interceptDetail)
	mux.HandleFunc("GET /api/intercept/history/{id}/execution", s.interceptExecution)
	mux.HandleFunc("GET /api/intercept/task/{taskID}", s.interceptListTaskItems)
	mux.HandleFunc("GET /api/intercept/tool-config", s.interceptGetToolConfig)
	mux.HandleFunc("PUT /api/intercept/tool-config", s.interceptSetToolConfig)
	mux.HandleFunc("GET /api/intercept/judge", s.interceptGetJudgeConfig)
	mux.HandleFunc("PUT /api/intercept/judge", s.interceptSetJudgeConfig)
	mux.HandleFunc("GET /api/intercept/judge/usage", s.interceptJudgeUsage)

	// /api/* goes through CORS + JWT; everything else is served by the embedded
	// frontend (public — auth is enforced client-side and on the API). With the
	// no-embed build the webui handler just 404s (run `next dev` separately).
	api := withLocale(cors(s.requireAuth(mux)))
	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/", s.webuiHandler())
	return root
}

// --- handlers ---

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "service": "artex", "version": BuildVersion})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	out["engine_mode"] = "idle"              // spec enum; overridden below when a task is active
	out["llm_configured"] = s.engine.Ready() // is an LLM provider installed at all
	if tr := s.m.Traffic(); tr != nil {
		c, _ := tr.Count()
		out["traffic"] = c
		out["traffic_enabled"] = true
	}
	if counts, err := s.m.Assets().CountsByType(); err == nil {
		total := 0
		for _, n := range counts {
			total += n
		}
		out["assets"] = total
		out["asset_counts"] = counts
	}

	// resolve the task: explicit ?task=<id> binds to that task (so a detail view
	// never silently follows a globally-changed active task); empty = active.
	taskParam := r.URL.Query().Get("task")
	t := s.m.ResolveTask(taskParam)
	if t == nil {
		if taskParam != "" && taskParam != "active" {
			writeErr(w, 404, "task not found")
			return
		}
		writeJSON(w, 200, out) // no task selected: only global fields
		return
	}
	out["llm_configured"] = s.engine.ReadyFor(t)

	st, _ := t.Store.Stats()
	out["exploration"] = st

	// per-task running state + heartbeat (distinct from "LLM configured").
	intents, _ := t.Store.ListByKind(db.KindIntent, 100000)
	inFlight := 0
	for _, in := range intents {
		if in.State == "running" {
			inFlight++
		}
	}
	goals, _ := t.Store.ListByKind(db.KindGoal, 10000)
	goalsMet := 0
	for _, g := range goals {
		if g.State == "met" {
			goalsMet++
		}
	}
	last := s.engine.LastActivity(t.ID)
	paused := s.engine.IsPaused(t.ID)
	activeCalls := s.engine.ActiveLLMCalls(t.ID)
	running := s.engine.ReadyFor(t) && s.engine.Started(t.ID) && !paused
	// A live event-driven engine with no work is healthy idle. Only a persisted
	// running intent without any corresponding LLM call can be considered stalled.
	stalled := running && activeCalls == 0 && inFlight > 0 && last > 0 && time.Now().Unix()-last > 120

	// engine_mode follows the spec enum (exploring|paused|stalled|idle).
	engineMode := "idle"
	switch {
	case paused:
		engineMode = "paused"
	case stalled:
		engineMode = "stalled"
	case running && activeCalls > 0:
		engineMode = "exploring"
	}
	out["engine_mode"] = engineMode

	out["active_task"] = map[string]any{
		"id": t.ID, "description": t.Description, "goal": t.Goal,
		"running":       running,
		"paused":        paused,
		"engine_mode":   engineMode,
		"in_flight":     inFlight,
		"llm_in_flight": activeCalls,
		"last_activity": last,
		"stalled":       stalled,
		"goals_total":   len(goals),
		"goals_met":     goalsMet,
	}
	writeJSON(w, 200, out)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	active := ""
	if t := s.m.ActiveTask(); t != nil {
		active = t.ID
	}
	list := s.m.List()
	metrics, _ := s.m.PG().TaskListMetricsAll()
	archiveBlockers, _ := s.m.PG().TaskArchiveBlockers()
	dtos := make([]TaskDTO, 0, len(list))
	for _, t := range list {
		dto := taskDTO(t, s.resolvedTaskStatus(t))
		applyTaskArchiveBlocker(&dto, archiveBlockers)
		metric := metrics[t.ExpID]
		dto.Tokens = tokenTotalDTO(metric.Tokens)
		// prefer the live in-memory heartbeat (fresher) and fall back to the
		// persisted max activity time (survives restarts) for run-duration display.
		dto.LastActivity = metric.LastActivity
		if live := s.engine.LastActivity(t.ID); live > dto.LastActivity {
			dto.LastActivity = live
		}
		dto.GoalsTotal = metric.Goals.Total
		dto.GoalsMet = metric.Goals.Met
		dto.InFlight = metric.RunningIntents
		dto.Findings = FindingSeverityDTO{
			Critical: metric.Findings.Critical,
			High:     metric.Findings.High,
			Medium:   metric.Findings.Medium,
			Low:      metric.Findings.Low,
		}
		dtos = append(dtos, dto)
	}
	writeJSON(w, 200, map[string]any{"tasks": dtos, "active": active})
}

func (s *Server) resolvedTaskStatus(t *Task) string {
	if t == nil {
		return "created"
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Queued:
		return "queued"
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case t.llmStateSnapshot().FailoverState == "chain_exhausted" && s.engine.Started(t.ID):
		// LLM readiness is an execution dependency, not lifecycle state. Keeping
		// an exhausted task running lets the user edit/reset its chain directly.
		return "running"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	default:
		return "created"
	}
}

func (s *Server) setActive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, err)
		return
	}
	if !s.m.SetActive(req.ID) {
		writeErr(w, 404, "task not found")
		return
	}
	// resume the engine for the opened task (idempotent — no-op if already running).
	// Queued tasks may become active for viewing without starting the engine; reconciliation enforces admission limits.
	if t, ok := s.m.Task(req.ID); ok && !t.lifecycleSnapshot().Queued {
		s.engine.Run(s.ctx, t)
	}
	writeJSON(w, 200, map[string]any{"active": req.ID})
}

// control pauses/resumes a task's autonomous execution (planner + workers).
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, err)
		return
	}
	if req.Action != "pause" && req.Action != "resume" {
		writeErr(w, 400, "action must be pause|resume")
		return
	}
	result, err := s.applyTaskControl(t, req.Action)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, result)
}

// controlIntent pauses, resumes, or cancels one local worker intent. A running
// worker is always stopped before state mutation/cleanup, preventing tool output
// that arrives after the user action from recreating deleted blackboard records.
func (s *Server) controlIntent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, locale.Text(responseLanguage(w), "Task is being deleted; intents cannot be controlled"))
		return
	}
	defer s.engine.decInflight(t.ID)

	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, 400, "bad intent id")
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"` // Required deletion reason for cancel.
		Mode   string `json:"mode"`   // Cancel mode: soft (default), or hard deletion cascading through exclusively supported descendants.
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad json: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	if req.Action != "pause" && req.Action != "resume" && req.Action != "cancel" {
		writeErr(w, 400, "action must be pause|resume|cancel")
		return
	}

	result, err := s.applyIntentControl(r.Context(), t, iid, req.Action, req.Reason, req.Mode)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, result)
}

// rerunIntent reopens blocked/exhausted/stopped intents for workers to reclaim and execute from scratch.
// Persisted facts/findings/assets remain; terminal or paused tasks are revived as needed.
// Supports retrying failed work after network/LLM failures with one action.
func restoreRerunIntent(t *Task, before *db.Node) error {
	if t == nil || before == nil {
		return locale.Errorf("missing intent rollback snapshot")
	}
	if before.State == "blocked" && before.BlockedReason != "" {
		return t.Store.SetIntentBlockedReason(before.ID, before.BlockedReason)
	}
	return t.Store.SetIntentState(before.ID, before.State)
}

func (s *Server) rerunIntent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad intent id")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, locale.Text(responseLanguage(w), "Task is being deleted; intents cannot be rerun"))
		return
	}
	defer s.engine.decInflight(t.ID)
	before, err := t.Store.GetNode(iid)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	reopened, err := t.Store.ReopenIntent(iid)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !reopened {
		writeErr(w, 409, locale.Text(responseLanguage(w), "This intent cannot be rerun; only blocked/exhausted/stopped intents are eligible"))
		return
	}
	queued, err := s.admitTask(t, "resume")
	if err != nil {
		if rollbackErr := restoreRerunIntent(t, before); rollbackErr != nil {
			err = locale.Errorf("%w; restore intent %d after admission failure: %v", err, iid, rollbackErr)
		}
		writeError(w, 500, err)
		return
	}
	log.Printf(locale.Text(responseLanguage(w), "[task] #%s reopened intent #%d for rerun"), t.ID, iid)
	writeJSON(w, 200, map[string]any{"id": t.ID, "reopened": iid, "queued": queued})
}

// rerunBlocked reopens all blocked task intents after failures such as a shared network/LLM outage,
// revives the task, and returns the number reopened.
func (s *Server) rerunBlocked(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, locale.Text(responseLanguage(w), "Task is being deleted; intents cannot be rerun"))
		return
	}
	defer s.engine.decInflight(t.ID)
	intents, err := t.Store.ListByKind(db.KindIntent, 1000000)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	before := make([]*db.Node, 0)
	for _, intent := range intents {
		if intent.State == "blocked" {
			copy := *intent
			before = append(before, &copy)
		}
	}
	n, err := t.Store.ReopenBlockedIntents()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	queued := false
	if n > 0 {
		queued, err = s.admitTask(t, "resume")
		if err != nil {
			rollbackErrors := make([]string, 0)
			for _, intent := range before {
				if rollbackErr := restoreRerunIntent(t, intent); rollbackErr != nil {
					rollbackErrors = append(rollbackErrors, fmt.Sprintf("intent %d: %v", intent.ID, rollbackErr))
				}
			}
			if len(rollbackErrors) > 0 {
				err = locale.Errorf("%w; restore blocked intents after admission failure: %s", err, strings.Join(rollbackErrors, "; "))
			}
			writeError(w, 500, err)
			return
		}
		log.Printf(locale.Text(responseLanguage(w), "[task] #%s reopened %d blocked intents"), t.ID, n)
	}
	writeJSON(w, 200, map[string]any{"id": t.ID, "reopened": n, "queued": queued})
}

// getLLM returns the current LLM config (key never exposed).
func (s *Server) getLLM(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	writeJSON(w, 200, map[string]any{
		"configured":       s.llmOn,
		"provider":         s.llmCfg.Provider(),
		"model":            s.llmCfg.Model,
		"base_url":         s.llmCfg.BaseURL,
		"proxy":            s.llmCfg.Proxy,
		"key_set":          s.llmCfg.APIKey != "",
		"rate_per_second":  s.llmCfg.RatePerSecond,
		"rate_per_minute":  s.llmCfg.RatePerMinute,
		"context_window_k": s.llmCfg.ContextWindowK,
		"thinking_type":    s.llmCfg.ThinkingType,
		"reasoning_effort": s.llmCfg.ReasoningEffort,
	})
}

// setLLM configures the LLM at runtime. A blank api_key keeps the existing key.
func (s *Server) setLLM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepAlive       string  `json:"keep_alive"`
		MaxConcurrent   int     `json:"llm_max_concurrent"`
		Provider        string  `json:"provider"`
		Model           string  `json:"model"`
		BaseURL         string  `json:"base_url"`
		Proxy           string  `json:"proxy"`
		APIKey          string  `json:"api_key"`
		RatePerSecond   float64 `json:"rate_per_second"`
		RatePerMinute   float64 `json:"rate_per_minute"`
		ContextWindowK  int     `json:"context_window_k"`
		ThinkingType    string  `json:"thinking_type"`
		ReasoningEffort string  `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, err)
		return
	}
	cfg := agent.ConfigFrom(req.Provider, req.Model, req.BaseURL, req.APIKey, req.Proxy)
	cfg.KeepAlive, cfg.MaxConcurrent = req.KeepAlive, req.MaxConcurrent
	cfg = s.applyLLMPolicy(cfg)
	cfg.RatePerSecond, cfg.RatePerMinute = req.RatePerSecond, req.RatePerMinute
	cfg.ThinkingType = req.ThinkingType
	cfg.ReasoningEffort = req.ReasoningEffort
	if k := req.ContextWindowK; k > 0 { // 0 = keep default (200K); cap at 1M
		if k > 1000 {
			k = 1000
		}
		cfg.ContextWindowK = k
	}
	if cfg.APIKey == "" && !cfg.Ready() {
		s.cfgMu.Lock()
		cfg.APIKey = s.llmCfg.APIKey // keep existing key if not re-entered
		s.cfgMu.Unlock()
	}
	if !cfg.Ready() {
		writeErr(w, 400, "api_key required")
		return
	}
	// Validate provider construction before persisting it as the active profile.
	if _, err := cfg.NewProvider(); err != nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "provider init failed: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	if err := s.saveLLMConfig(cfg); err != nil {
		writeErr(w, 500, locale.Text(responseLanguage(w), "persist provider failed: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	s.invalidateProfileAgents()
	savedCfg, ok := s.loadLLMConfig()
	if !ok {
		writeErr(w, 500, "saved provider is unavailable")
		return
	}
	cfg = savedCfg
	if err := s.applyLLM(cfg); err != nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "provider init failed: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	log.Printf("[engine] LLM configured via UI: %s / %s", cfg.Provider(), cfg.Model)
	s.getLLM(w, r)
}

// testLLM makes a real minimal completion to verify the config works.
func (s *Server) testLLM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepAlive        string `json:"keep_alive"`
		MaxConcurrent    int    `json:"llm_max_concurrent"`
		ContextWindowK   int    `json:"context_window_k"`
		MaxTokens        int    `json:"max_tokens"`
		Provider         string `json:"provider"`
		Model            string `json:"model"`
		BaseURL          string `json:"base_url"`
		Proxy            string `json:"proxy"`
		APIKey           string `json:"api_key"`
		ThinkingType     string `json:"thinking_type"`
		ReasoningEffort  string `json:"reasoning_effort"`
		ProfileID        *int64 `json:"profile_id"`         // Existing profile under test; an empty api_key uses its stored key.
		Streaming        *bool  `json:"streaming"`          // Omitted means streaming, matching profile-save defaults.
		SessionHeaderKey string `json:"session_header_key"` // Optional custom session header, also sent during testing with a fresh random session ID.
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, err)
		return
	}
	cfg := agent.ConfigFrom(req.Provider, req.Model, req.BaseURL, req.APIKey, req.Proxy)
	cfg.KeepAlive, cfg.MaxConcurrent = req.KeepAlive, req.MaxConcurrent
	cfg = s.applyLLMPolicy(cfg)
	// mirror production: send the SAME thinking params so a provider that rejects the
	// reasoning_effort/thinking field fails the test too (no false "test ok, run 400").
	cfg.ThinkingType = req.ThinkingType
	cfg.ReasoningEffort = req.ReasoningEffort
	// Test the profile's chosen transport mode so endpoints supporting only one mode fail here,
	// rather than passing the test but failing in an actual conversation.
	if req.Streaming != nil {
		cfg.Stream = *req.Streaming
	}
	// Use the configured session header during testing, with a fresh random session ID from TestConnection.
	// Endpoints such as opencode zen require x-opencode-session and otherwise return 400,
	// so omitting the header would make successful conversations fail connection tests.
	providerSessionHeaderKey := cfg.SessionHeaderKey
	cfg.SessionHeaderKey = req.SessionHeaderKey
	// API-key precedence: submitted form, specified profile's stored key, then global configuration.
	// Stored keys never return to the browser; testing an existing profile may require reading its key from DB.
	// Likewise, fall back to the stored custom session-header name when omitted by the form.
	if req.ProfileID != nil {
		if p, err := s.m.pg.ProfileByID(*req.ProfileID); err == nil && p != nil {
			if cfg.APIKey == "" && (cfg.Provider() != "ollama" || p.Format == "ollama") {
				cfg.APIKey = p.APIKey
			}
			if cfg.SessionHeaderKey == "" {
				cfg.SessionHeaderKey = p.SessionHeaderKey
			}
			if req.KeepAlive == "" {
				cfg.KeepAlive = p.KeepAlive
			}
			if req.ContextWindowK == 0 {
				cfg.ContextWindowK = p.ContextWindowK
			}
			if req.MaxTokens == 0 {
				cfg.MaxTokens = p.MaxTokens
			}
		}
	}
	// Request and saved-profile headers take precedence over provider defaults.
	if cfg.SessionHeaderKey == "" {
		cfg.SessionHeaderKey = providerSessionHeaderKey
	}
	if cfg.APIKey == "" {
		s.cfgMu.Lock()
		active := s.llmCfg
		s.cfgMu.Unlock()
		active = agent.ConfigFrom(active.Provider(), active.Model, active.BaseURL, active.APIKey, active.Proxy)
		// Reuse local gateway credentials only for the same format and transport target.
		// Other local endpoints and native Ollama remain deliberately keyless.
		sameGateway := cfg.Provider() != "ollama" && cfg.Format == active.Format && cfg.BaseURL == active.BaseURL && cfg.Proxy == active.Proxy
		if !cfg.Ready() || sameGateway {
			cfg.APIKey = active.APIKey
		}
	}
	if !cfg.Ready() {
		writeJSON(w, 200, map[string]any{"ok": false, "error": locale.Text(responseLanguage(w), "API key was not provided")})
		return
	}
	if cfg.Provider() == "ollama" {
		if req.ContextWindowK > 0 {
			cfg.ContextWindowK = req.ContextWindowK
		}
		if req.MaxTokens > 0 {
			cfg.MaxTokens = req.MaxTokens
		}
		s.testOllama(w, r, cfg)
		return
	}
	// Do not apply configured retries to connection tests: their hard 30-second timeout combined with
	// long retry intervals can make a usable endpoint appear unavailable. Tests check connectivity;
	// retry timing applies during normal execution.
	lat, reply, err := agent.TestConnection(r.Context(), cfg)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": locale.ErrorMessage(responseLanguage(w), err)})
		return
	}
	// Return the model's actual reply as evidence of a successful exchange beyond HTTP 200.
	writeJSON(w, 200, map[string]any{
		"ok": true, "latency_ms": lat.Milliseconds(), "model": cfg.Model, "reply": truncateReply(reply),
	})
}

// truncateReply clips a connection-test reply for display. A model told to answer
// "OK" can still ramble (or think out loud); the UI only needs enough to show it
// really said something. Rune-based so multibyte text never splits mid-character.
func truncateReply(s string) string {
	const max = 200
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

type createTaskReq struct {
	Name                 string   `json:"name,omitempty"` // Optional task name; omitted/empty means unnamed.
	CategoryID           *int64   `json:"category_id,omitempty"`
	Description          string   `json:"description"`
	Goal                 string   `json:"goal"`
	LLMProfileID         *int64   `json:"llm_profile_id,omitempty"`    // Task LLM profile; omitted/null selects the active profile.
	LLMProfileIDs        []int64  `json:"llm_profile_ids,omitempty"`   // Ordered task-level profile chain; the first entry is initially active.
	SourceTaskIDs        []string `json:"source_task_ids,omitempty"`   // Direct, read-only inheritance source tasks only.
	CompanyIDs           []int64  `json:"company_ids,omitempty"`       // Associate company scope and snapshot current company assets, without copying assets or forcing intents.
	TimeoutSeconds       int      `json:"timeout_seconds"`             // Task timeout in seconds; zero/omitted means unlimited.
	PlanHeartbeatSeconds int      `json:"plan_heartbeat_seconds"`      // Planner heartbeat seconds; zero/omitted defaults to 600 (10 minutes), also the enforced minimum.
	SeedFirstIntent      *bool    `json:"seed_first_intent,omitempty"` // Explicit true seeds one intent from description/goals so a worker can start before planning. Omitted/null defaults false; useful for simple CTF tasks needing one work item.
	CoverageEnabled      *bool    `json:"coverage_enabled,omitempty"`  // Asset coverage defaults true. False disables calculation/display/automatic scope accumulation and hides add_task_scope/list_untested_assets; company associations remain available.
	// InterceptRules stores task-local asset interception/allow rules in task_intercept_rules, never the global table.
	InterceptRules []taskInterceptRuleReq `json:"intercept_rules,omitempty"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad json: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		req.Description = locale.Text(responseLanguage(w), "Untitled task")
	}
	if len(req.LLMProfileIDs) == 0 && req.LLMProfileID != nil {
		req.LLMProfileIDs = []int64{*req.LLMProfileID}
	}
	if err := s.validateTaskProfileIDs(req.LLMProfileIDs); err != nil {
		writeError(w, 400, err)
		return
	}
	if req.TimeoutSeconds < 0 {
		req.TimeoutSeconds = 0
	}
	if len(req.SourceTaskIDs) > db.MaxTaskSourceCount {
		writeErr(w, 400, fmt.Sprintf(locale.Text(responseLanguage(w), "At most %d source tasks may be selected"), db.MaxTaskSourceCount))
		return
	}
	sourceIDs := make([]int64, 0, len(req.SourceTaskIDs))
	seenSources := map[int64]bool{}
	for _, raw := range req.SourceTaskIDs {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 || seenSources[id] {
			writeErr(w, 400, locale.Text(responseLanguage(w), "Source task ID is invalid or duplicated"))
			return
		}
		if _, ok := s.m.Task(strconv.FormatInt(id, 10)); !ok {
			writeErr(w, 400, fmt.Sprintf(locale.Text(responseLanguage(w), "Source task #%d does not exist"), id))
			return
		}
		seenSources[id] = true
		sourceIDs = append(sourceIDs, id)
	}
	companyIDs, err := db.NormalizeTaskCompanyIDs(req.CompanyIDs)
	if err != nil {
		writeErr(w, 400, fmt.Sprintf(locale.Text(responseLanguage(w), "Invalid linked companies: select at most %d valid companies"), db.MaxTaskCompanyCount))
		return
	}
	req.CompanyIDs = companyIDs
	interceptRules, err := buildTaskInterceptRules(req.InterceptRules)
	if err != nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "Invalid task interception rules: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	t, err := s.m.CreateTaskWithOptions(req.Description, req.Goal, db.TaskCreateOptions{
		Language: locale.FromRequest(r),
		Name:     strings.TrimSpace(req.Name), CategoryID: req.CategoryID,
		SourceTaskIDs: sourceIDs, CompanyIDs: req.CompanyIDs, LLMProfileIDs: req.LLMProfileIDs,
		TimeoutSeconds: req.TimeoutSeconds, PlanHeartbeatSeconds: req.PlanHeartbeatSeconds,
		CoverageEnabled: req.CoverageEnabled,
		InterceptRules:  interceptRules,
	})
	if err != nil {
		if errors.Is(err, db.ErrTaskCategoryInvalid) || errors.Is(err, db.ErrTaskCategoryNotFound) {
			writeErr(w, 400, locale.Text(responseLanguage(w), "Task category does not exist or is invalid"))
			return
		}
		if errors.Is(err, db.ErrTaskCompanyIDsInvalid) || errors.Is(err, db.ErrTaskCompanyNotFound) {
			writeErr(w, 400, locale.Text(responseLanguage(w), "Linked company does not exist or is invalid"))
			return
		}
		writeError(w, 500, err)
		return
	}
	log.Printf(locale.Text(responseLanguage(w), "[task] Created task #%s «%s», objective: %s"), t.ID, req.Description, req.Goal)
	// Share post-creation seeding, optional seed intent, background goal decomposition, and engine.Run with spawn_task.
	// launchTask is asynchronous so visible background goal decomposition does not block the UI.
	s.launchTask(t, req.Description+" "+req.Goal, req.SeedFirstIntent != nil && *req.SeedFirstIntent)
	writeJSON(w, 201, taskDTO(t, s.resolvedTaskStatus(t)))
}

func (s *Server) validateTaskProfileIDs(ids []int64) error {
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return locale.Errorf("LLM profile ID is invalid or duplicated")
		}
		seen[id] = true
		if _, ok := s.loadProfileConfig(id); !ok {
			return locale.Errorf("LLM profile #%d does not exist or has no API key", id)
		}
	}
	return nil
}

func (s *Server) updateTaskLLMProfiles(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	// The chain may change in any lifecycle state, including terminal: main-agent conversations still use it
	// after task completion, so an unavailable model must not permanently lock out interaction.
	before := t.llmStateSnapshot()
	var req struct {
		LLMProfileIDs      []int64 `json:"llm_profile_ids"`
		ActiveLLMProfileID *int64  `json:"active_llm_profile_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad json: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	if err := s.validateTaskProfileIDs(req.LLMProfileIDs); err != nil {
		writeError(w, 400, err)
		return
	}
	active := int64(0)
	if req.ActiveLLMProfileID != nil {
		active = *req.ActiveLLMProfileID
	}
	reopened, err := s.m.ReplaceTaskLLMProfiles(t.ID, req.LLMProfileIDs, active)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	// Profile edits affect only subsequent LLM calls. Existing in-flight calls
	// retain their provider. Concurrency reconciliation treats ActiveLLMCalls as a
	// live slot and postpones an unavailable task's pause/queue transition until
	// the call returns; a nudge lets an idle running task resume promptly.
	t.Notify()
	go s.reconcileConcurrency()
	llmState := t.llmStateSnapshot()
	result := map[string]any{
		"id": t.ID, "llm_profile_ids": llmState.ProfileIDs,
		"active_llm_profile_id": llmState.ActiveID,
		"llm_failover_state":    llmState.FailoverState, "reopened_intents": reopened,
	}
	if !sameOptionalID(before.ActiveID, llmState.ActiveID) {
		event := s.emitManualTaskLLMSwitch(t, before.ActiveID, llmState.ActiveID)
		result["switch_event"] = activityDTO(event)
	}
	writeJSON(w, 200, result)
}

var (
	reURL    = regexp.MustCompile(`https?://[^\s'"]+`)
	reIPPort = regexp.MustCompile(`\b((?:\d{1,3}\.){3}\d{1,3})(?::(\d{1,5}))?`)
	reDomain = regexp.MustCompile(`\b((?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,})(?::(\d{1,5}))?`)
)

// parseTarget extracts a target (scheme, host, port) from free text, supporting
// full URLs, IP[:port] and domain[:port]. ok=false when nothing parseable.
func parseTarget(text string) (scheme, host string, port int, ok bool) {
	text = strings.TrimSpace(text)
	if m := reURL.FindString(text); m != "" {
		if u, err := url.Parse(m); err == nil && u.Hostname() != "" {
			scheme = strings.ToLower(u.Scheme)
			host = strings.ToLower(u.Hostname())
			port = portOr(u.Port(), defaultPort(scheme))
			return scheme, host, port, true
		}
	}
	if m := reIPPort.FindStringSubmatch(text); m != nil {
		host = m[1]
		port = portOr(m[2], 80)
		return schemeForPort(port), host, port, true
	}
	if m := reDomain.FindStringSubmatch(text); m != nil {
		host = strings.ToLower(m[1])
		if m[2] == "" {
			return "https", host, 443, true
		}
		port = portOr(m[2], 443)
		return schemeForPort(port), host, port, true
	}
	return "", "", 0, false
}

func portOr(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return d
}
func defaultPort(scheme string) int {
	if scheme == "http" {
		return 80
	}
	return 443
}
func schemeForPort(p int) string {
	if p == 443 || p == 8443 {
		return "https"
	}
	return "http"
}

// llmHost returns the host of the configured LLM endpoint (to keep it out of scope).
func (s *Server) llmHost() string {
	s.cfgMu.Lock()
	base := s.llmCfg.BaseURL
	s.cfgMu.Unlock()
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}

func (s *Server) seed(t *Task, text string) {
	scheme, host, port, ok := parseTarget(text)
	if !ok {
		log.Printf(locale.Text(locale.ServerDefault(), "[seed] task %s: no target host/IP could be parsed from %q; no site created (configure scope manually)"), t.ID, text)
		return
	}
	// P0-1 guard: never treat the configured LLM gateway as a target.
	if gw := s.llmHost(); gw != "" && host == gw {
		log.Printf(locale.Text(locale.ServerDefault(), "[seed] task %s: target %q is an LLM gateway and cannot be used as a testing target"), t.ID, host)
		return
	}

	u := scheme + "://" + host
	if !(scheme == "https" && port == 443) && !(scheme == "http" && port == 80) {
		u += ":" + strconv.Itoa(port)
	}
	var rootID int64
	if as := s.m.Assets(); as != nil {
		taskID, _ := strconv.ParseInt(t.ID, 10, 64)
		if net.ParseIP(host) != nil {
			rootID, _ = as.UpsertIP(db.UpsertIPReq{IP: host, TaskID: taskID})
		} else if scheme == "https" || scheme == "http" {
			rootID, _ = as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: u, TaskID: taskID})
		} else {
			rootID, _ = as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: host, TaskID: taskID})
		}
		if rootID > 0 {
			_ = as.SetTaskAssetSource(taskID, rootID, "task", locale.Text(s.taskOutputLanguage(t.ID), "Initialized from the task description or objective"), nil)
		}
	}
	// anchor the seeded assets to this task's begin root as lineage/provenance
	// (the asset graph is global and shared; anchoring no longer gates reads).
	if rootID > 0 {
		if begin, _ := t.Store.OriginFactID(); begin > 0 {
			_ = t.Store.Anchor(begin, rootID)
		}
	}
	log.Printf(locale.Text(locale.ServerDefault(), "[seed] task %s: target site %s"), t.ID, u)
	// Do not Notify here: engine.Run uses HasActiveIntent to decide whether to start initial planning.
	// Seeds are created before Run, so an early buffered notification would bypass that guard when
	// plannerLoop starts and incorrectly trigger initial planning for seeded tasks.
}

// seedFirstIntent writes ONE open intent, summarized from description and goals, into the task's
// frontier at creation, so a worker can claim and run it immediately without first
// waiting a planner round. Mirrors a planner top-level intent: it links from the
// origin fact (RelDerivedFrom) so it still traces back to a fact node. Best-effort —
// a failure just falls back to the normal planner-driven flow.
func (s *Server) seedFirstIntent(t *Task) {
	summary := fmt.Sprintf(locale.Text(s.taskOutputLanguage(t.ID), "Complete the task objective: %s (task: %s)"), t.Goal, t.Description)
	id, err := t.Store.AddIntent(map[string]any{"summary": summary}, 8, nil, "seed")
	if err != nil {
		log.Printf(locale.Text(locale.ServerDefault(), "[seed] task %s: could not create seed intent: %v"), t.ID, err)
		return
	}
	if origin, _ := t.Store.OriginFactID(); origin > 0 {
		_ = t.Store.Link(origin, db.RelDerivedFrom, id)
	}
	log.Printf(locale.Text(locale.ServerDefault(), "[seed] task %s: created seed intent #%d"), t.ID, id)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	dto := taskDTO(t, s.resolvedTaskStatus(t))
	archiveBlockers, _ := s.m.PG().TaskArchiveBlockers()
	applyTaskArchiveBlocker(&dto, archiveBlockers)
	writeJSON(w, 200, dto)
}

// taskCoverage returns a task's rough asset test coverage (denominator/tested/backlog).
func (s *Server) taskCoverage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, locale.Text(responseLanguage(w), "Asset store is unavailable"))
		return
	}
	// Disabled asset coverage returns {enabled:false}; the frontend hides its coverage card/progress.
	if !t.CoverageEnabled {
		writeJSON(w, 200, &db.Coverage{Enabled: false, ByType: []db.CoverageByType{}})
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	cov, err := as.TaskCoverageWithSources(taskID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	cov.Enabled = true
	writeJSON(w, 200, cov)
}

// taskCoverageGraph returns the force-directed asset coverage graph for a task:
// all in-scope asset types plus connecting root-domain/company nodes, each carrying tested/in_scope.
func (s *Server) taskCoverageGraph(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, locale.Text(responseLanguage(w), "Asset store is unavailable"))
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	g, err := as.BuildCoverageGraph(taskID, t.ExpID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, g)
}

// taskAssetRefs returns the intents / facts / findings in this task anchored to a
// given asset ID, powering the coverage-graph drawer's related intents/facts.
func (s *Server) taskAssetRefs(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	assetID, _ := strconv.ParseInt(r.URL.Query().Get("asset_id"), 10, 64)
	if assetID <= 0 {
		writeErr(w, 400, locale.Text(responseLanguage(w), "asset_id is required"))
		return
	}
	refs, err := t.Store.AssetRefsWithSources(assetID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	intents := []CoverageAssetRefDTO{}
	facts := []CoverageAssetRefDTO{}
	findings := []CoverageAssetRefDTO{}
	for _, ref := range refs {
		dto := coverageAssetRefDTO(ref)
		switch ref.Kind {
		case "intent":
			intents = append(intents, dto)
		case "fact":
			facts = append(facts, dto)
		case "finding":
			findings = append(findings, dto)
		}
	}
	writeJSON(w, 200, map[string]any{"intents": intents, "facts": facts, "findings": findings})
}

// taskScopeList returns a task's scope rows (coverage denominator sources).
func (s *Server) taskScopeList(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, locale.Text(responseLanguage(w), "Asset store is unavailable"))
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	rows, err := as.ListTaskScopeWithSources(taskID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"scope": rows})
}

func (s *Server) taskScopeAdd(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, locale.Text(responseLanguage(w), "Asset store is unavailable"))
		return
	}
	var body struct {
		Kind   string `json:"kind"`
		Value  string `json:"value"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	ts, err := as.AddAgentScope(taskID, body.Kind, body.Value, body.Reason, "manual")
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, ts)
}

func (s *Server) taskScopeDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, locale.Text(responseLanguage(w), "Asset store is unavailable"))
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	scopeID, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid scope id")
		return
	}
	deleted, err := as.DeleteTaskScope(taskID, scopeID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !deleted {
		writeErr(w, 404, "scope row not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) frontier(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, []any{})
		return
	}
	fr, _ := t.Store.Frontier(atoiDefault(r.URL.Query().Get("limit"), 100))
	writeJSON(w, 200, taskNodeDTOs(fr))
}

func (s *Server) findings(w http.ResponseWriter, r *http.Request) {
	// Without task, the global Findings page reads independent findings retained after task deletion.
	// With task, task overview/Findings tabs read exploration_nodes while the task exists.
	q := r.URL.Query()
	taskParam := q.Get("task")
	if taskParam == "" {
		// page/limit requests server pagination {items,total,...}; otherwise return a raw array for dashboard aggregation,
		// matching intents endpoint compatibility. Filtering/sorting is consistently delegated to SQL.
		if q.Get("page") == "" && q.Get("limit") == "" {
			fs, _ := s.m.pg.ListFindings(500)
			assets := s.resolveFindingAssets(fs)
			out := make([]FindingDTO, 0, len(fs))
			for _, f := range fs {
				out = append(out, findingFromDB(f, assets))
			}
			writeJSON(w, 200, out)
			return
		}
		page := findingPaginationParam(q.Get("page"), 1, 0)
		limit := findingPaginationParam(q.Get("limit"), 20, 200)
		fs, total, err := s.m.pg.ListFindingsPage(findingFilterFromQuery(q), page, limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		assets := s.resolveFindingAssets(fs)
		out := make([]FindingDTO, 0, len(fs))
		for _, f := range fs {
			out = append(out, findingFromDB(f, assets))
		}
		writeJSON(w, 200, map[string]any{"items": out, "total": total, "page": page, "page_size": limit})
		return
	}
	t := s.m.ResolveTask(taskParam)
	if t == nil {
		writeJSON(w, 200, []any{})
		return
	}
	f, err := t.Store.ListByKind(db.KindFinding, 200)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	tid, _ := strconv.ParseInt(t.ID, 10, 64)
	meta, err := s.m.pg.FindingMetaByNodeID(tid)
	if err != nil {
		log.Printf("[findings] task=%s meta: %v", t.ID, err)
	}
	var aidSet []int64
	for _, m := range meta {
		aidSet = append(aidSet, m.AssetIDs...)
	}
	assets := s.resolveAssetIDs(aidSet)
	out := findingDTOsForTask(t, f, meta, assets)

	// Related-task findings are a live, read-only view. Provenance metadata lets
	// the task UI suppress mutation affordances while retaining stable ids.
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for _, source := range sources {
		nodes, listErr := source.Store.ListByKind(db.KindFinding, 200)
		if listErr != nil {
			writeError(w, 500, listErr)
			return
		}
		for _, node := range nodes {
			node.SourceTaskID = source.Task.TaskID
			node.Inherited = true
		}
		sourceMeta, metaErr := s.m.pg.FindingMetaByNodeID(source.Task.TaskID)
		if metaErr != nil {
			log.Printf("[findings] source_task=%d meta: %v", source.Task.TaskID, metaErr)
		}
		var sourceAssetIDs []int64
		for _, item := range sourceMeta {
			sourceAssetIDs = append(sourceAssetIDs, item.AssetIDs...)
		}
		out = append(out, findingDTOsForOwner(
			i64s(source.Task.TaskID),
			source.Task.Description,
			nodes,
			sourceMeta,
			s.resolveAssetIDs(sourceAssetIDs),
		)...)
	}
	writeJSON(w, 200, out)
}

// normFilter maps the frontend's "all" sentinel (and empty) to "" so the DB layer
// treats it as no filter.
func normFilter(v string) string {
	if v == "all" {
		return ""
	}
	return v
}

// resolveFindingAssets loads every asset anchored by the given findings, keyed by
// id, so each finding DTO can render its assets' labels.
func (s *Server) resolveFindingAssets(fs []*db.DBFinding) map[int64]*db.Asset {
	var ids []int64
	for _, f := range fs {
		ids = append(ids, f.AssetIDs...)
	}
	return s.resolveAssetIDs(ids)
}

// resolveAssetIDs de-dupes ids and loads their asset rows into an id→asset map.
func (s *Server) resolveAssetIDs(ids []int64) map[int64]*db.Asset {
	assets := map[int64]*db.Asset{}
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return assets
	}
	list, err := s.m.pg.Assets().GetByIDs(uniq)
	if err != nil {
		log.Printf("[findings] resolve assets: %v", err)
	}
	for _, a := range list {
		assets[a.ID] = a
	}
	return assets
}

// findingStats serves the whole-table aggregates (stat cards + vuln-class filter)
// for the paginated Findings page.
func (s *Server) findingStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.m.pg.FindingStats()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, st)
}

// getFinding returns one finding by its standalone-table id (DTO finding_id),
// with anchored assets resolved for display.
func (s *Server) getFinding(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if f == nil {
		writeErr(w, 404, "finding not found")
		return
	}
	assets := s.resolveAssetIDs(f.AssetIDs)
	dto := findingFromDB(f, assets)
	if contextTaskID := strings.TrimSpace(r.URL.Query().Get("context_task")); contextTaskID != "" {
		contextTask := s.m.ResolveTask(contextTaskID)
		if contextTask == nil {
			writeErr(w, 404, "context task not found")
			return
		}
		sourceTaskID, inherited, allowed := findingProvenanceInTask(contextTask, f.TaskID)
		if !allowed {
			writeErr(w, 404, "finding not available in task context")
			return
		}
		dto.SourceTaskID = sourceTaskID
		dto.Inherited = inherited
	}
	writeJSON(w, 200, dto)
}

// findingsExport exports findings from the Findings page.
//
// scope = filtered (page filters), all, or selected (checked IDs).
// format = md-single (combined Markdown) or md-zip (one Markdown file per finding).
//
//	| csv | json
//
// ids = comma-separated finding IDs, required for selected scope.
// Filtered scope uses the list endpoint's severity/status/vulnclass/task_id/q/sort parameters.
func (s *Server) findingsExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := q.Get("scope")
	format := q.Get("format")

	var ids []int64
	var filter db.FindingFilter
	switch scope {
	case "selected":
		for _, part := range strings.Split(q.Get("ids"), ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				writeErr(w, 400, locale.Text(responseLanguage(w), "bad finding id: ")+part)
				return
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			writeErr(w, 400, "no findings selected")
			return
		}
	case "all":
		// An empty filter applies no conditions.
	case "filtered", "":
		filter = findingFilterFromQuery(q)
	default:
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad scope: ")+scope)
		return
	}

	fs, err := s.m.pg.ListFindingsForExport(filter, ids)
	if err != nil {
		writeError(w, 500, err)
		return
	}

	stage, err := os.MkdirTemp("", "artex-finding-export-")
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer os.RemoveAll(stage)
	if err = s.evidenceStore().StageFindingsExport(r.Context(), fs, stage, format == "md-zip"); err != nil {
		evidenceError(w, err)
		return
	}
	now := time.Now()
	stamp := now.Format("20060102-150405")
	setDownload := func(contentType, filename string) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}

	switch format {
	case "md-single":
		setDownload("text/markdown; charset=utf-8", "findings-"+stamp+".md")
		_, _ = w.Write([]byte(report.FindingsMarkdown(fs, now, locale.FromRequest(r))))
	case "md-zip":
		path := filepath.Join(stage, "findings.zip")
		if err := buildFindingsEvidenceZip(path, fs, stage, now, locale.FromRequest(r)); err != nil {
			writeError(w, 500, err)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		defer file.Close()
		setDownload("application/zip", "findings-"+stamp+".zip")
		http.ServeContent(w, r, "findings.zip", now, file)
	case "csv":
		setDownload("text/csv; charset=utf-8", "findings-"+stamp+".csv")
		_, _ = w.Write(report.FindingsCSV(fs, locale.FromRequest(r)))
	case "json":
		assets := s.resolveFindingAssets(fs)
		out := make([]FindingDTO, 0, len(fs))
		for _, f := range fs {
			for i := range f.TrafficBindings {
				f.TrafficBindings[i].Snapshot.ReqHead = ""
				f.TrafficBindings[i].Snapshot.RespHead = ""
			}
			out = append(out, findingFromDB(f, assets))
		}
		setDownload("application/json; charset=utf-8", "findings-"+stamp+".json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	default:
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad format: ")+format)
	}
}

func findingProvenanceInTask(contextTask *Task, findingTaskID *int64) (sourceTaskID string, inherited, allowed bool) {
	if contextTask == nil || findingTaskID == nil {
		return "", false, false
	}
	contextID, err := strconv.ParseInt(contextTask.ID, 10, 64)
	if err != nil {
		return "", false, false
	}
	if *findingTaskID == contextID {
		return "", false, true
	}
	for _, sourceID := range contextTask.lifecycleSnapshot().SourceTaskIDs {
		if sourceID == *findingTaskID {
			return i64s(sourceID), true, true
		}
	}
	return "", false, false
}

// findingLineage returns the exploration sub-DAG from the task root to this
// finding's node — the finding node + all its ancestors + edges among them — so
// the detail page can show "how this finding was reached". Empty {nodes,edges}
// when the finding has no node/task (e.g. the originating task was deleted).
func (s *Server) findingLineage(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if f == nil {
		writeErr(w, 404, "finding not found")
		return
	}
	empty := map[string]any{"nodes": []any{}, "edges": []any{}}
	if f.NodeID == nil || f.TaskID == nil {
		writeJSON(w, 200, empty)
		return
	}
	t := s.m.ResolveTask(i64s(*f.TaskID))
	if t == nil {
		writeJSON(w, 200, empty)
		return
	}
	nodes, edges, err := t.Store.FindingLineage(*f.NodeID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"nodes": taskNodeDTOs(nodes), "edges": edgeDTOs(edges)})
}

// patchFinding partially updates a finding: any subset of {status, severity}.
// The id is the standalone findings-table id (DTO finding_id). Severity edits are
// mirrored onto the originating exploration node so the per-task view stays in
// sync. Returns the updated finding DTO.
func (s *Server) patchFinding(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	var body struct {
		Status    *string `json:"status"`
		Severity  *string `json:"severity"`
		Name      *string `json:"name"`
		VulnClass *string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad json: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	if body.Status == nil && body.Severity == nil && body.Name == nil && body.VulnClass == nil {
		writeErr(w, 400, "nothing to update: provide status/severity/name/vulnclass")
		return
	}
	if body.Status != nil {
		if !db.ValidFindingStatus(*body.Status) {
			writeErr(w, 400, locale.Text(responseLanguage(w), "bad status: ")+*body.Status)
			return
		}
		// Use the notifying variant to persist status and its change-notification event in one transaction,
		// avoiding a gap where status changes but the event is lost. Event-registration failure does not
		// prevent the status update, so log it without returning an error to the caller.
		from, found, notified, err := s.m.pg.SetFindingStatusWithNotify(r.Context(), id, *body.Status)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !found {
			writeErr(w, 404, "finding not found")
			return
		}
		if !notified && from != *body.Status {
			log.Printf(locale.Text(responseLanguage(w), "[notify] Status-change event not recorded for finding=%d %s→%s (status was updated)"), id, from, *body.Status)
		}
	}
	if body.Severity != nil {
		if !db.ValidSeverity(*body.Severity) {
			writeErr(w, 400, locale.Text(responseLanguage(w), "bad severity: ")+*body.Severity)
			return
		}
		n, err := s.m.pg.SetFindingSeverity(id, *body.Severity)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if n == 0 {
			writeErr(w, 404, "finding not found")
			return
		}
	}
	if body.Name != nil {
		n, err := s.m.pg.SetFindingName(id, strings.TrimSpace(*body.Name))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if n == 0 {
			writeErr(w, 404, "finding not found")
			return
		}
	}
	if body.VulnClass != nil {
		n, err := s.m.pg.SetFindingVulnClass(id, strings.TrimSpace(*body.VulnClass))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if n == 0 {
			writeErr(w, 404, "finding not found")
			return
		}
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if f == nil {
		writeErr(w, 404, "finding not found")
		return
	}
	writeJSON(w, 200, findingFromDB(f, s.resolveAssetIDs(f.AssetIDs)))
}

// deleteFinding removes a finding (findings row + originating exploration node).
// id is the standalone findings-table id (DTO finding_id).
func (s *Server) deleteFinding(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	n, err := s.m.pg.DeleteFinding(id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if n == 0 {
		writeErr(w, 404, "finding not found")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true, "id": id})
}

func (s *Server) intents(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		// Back-compat: bare list shape when the task can't be resolved.
		writeJSON(w, 200, []any{})
		return
	}
	q := r.URL.Query()
	limit := min(atoiDefault(q.Get("limit"), 300), 500)
	// No paging params → preserve the legacy bare-array response so existing callers
	// (and the poll) keep working unchanged.
	if q.Get("before") == "" && q.Get("page") == "" {
		in, err := t.Store.ListByKind(db.KindIntent, limit)
		if err != nil {
			log.Printf("[intents] task=%s limit=%d: %v", t.ID, limit, err)
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, taskNodeDTOs(in))
		return
	}
	// Paged form: ?before=<id> (or ?page as a marker) → {items, has_more} so the
	// worker session list can reach past the old fixed 300 boundary on scroll. It
	// also includes immutable intent results from directly related tasks; those
	// rows never participate in this task's frontier or claim path.
	before := int64(atoiDefault(q.Get("before"), 0))
	in, hasMore, err := taskIntentHistoryPage(t.Store, before, limit, false, 0)
	if err != nil {
		log.Printf("[intents] task=%s before=%d limit=%d: %v", t.ID, before, limit, err)
		writeError(w, 500, err)
		return
	}
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for _, source := range sources {
		items, sourceMore, sourceErr := taskIntentHistoryPage(source.Store, before, limit, true, source.Task.TaskID)
		if sourceErr != nil {
			writeError(w, 500, sourceErr)
			return
		}
		in = append(in, items...)
		hasMore = hasMore || sourceMore
	}
	sort.Slice(in, func(i, j int) bool { return in[i].ID > in[j].ID })
	if len(in) > limit {
		in = in[:limit]
		hasMore = true
	}
	writeJSON(w, 200, map[string]any{"items": taskNodeDTOs(in), "has_more": hasMore})
}

func inheritedIntentResult(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// inheritedGraphSnapshot exposes immutable source-task history without leaking
// work that is still open or running. Edges are filtered with the nodes so the
// response cannot contain dangling ids that reveal hidden live intent topology.
func inheritedGraphSnapshot(nodes []*db.Node, edges []db.Edge, sourceTaskID int64) ([]*db.Node, []db.Edge) {
	visible := make(map[int64]struct{}, len(nodes))
	filteredNodes := make([]*db.Node, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || (node.Kind == db.KindIntent && !inheritedIntentResult(node.State)) {
			continue
		}
		node.SourceTaskID = sourceTaskID
		node.Inherited = true
		visible[node.ID] = struct{}{}
		filteredNodes = append(filteredNodes, node)
	}
	filteredEdges := make([]db.Edge, 0, len(edges))
	for _, edge := range edges {
		if _, ok := visible[edge.From]; !ok {
			continue
		}
		if _, ok := visible[edge.To]; !ok {
			continue
		}
		filteredEdges = append(filteredEdges, edge)
	}
	return filteredNodes, filteredEdges
}

// taskIntentHistoryPage returns one newest-first page for an exploration.
// A source may have many open/running intents, so keep paging until enough
// historical results are collected instead of leaking its frontier into the UI.
func taskIntentHistoryPage(store *db.ExplorationStore, before int64, limit int, inherited bool, sourceTaskID int64) ([]*db.Node, bool, error) {
	if !inherited {
		return store.ListByKindPage(db.KindIntent, before, limit)
	}
	batch := max(limit, 300)
	cursor := before
	out := make([]*db.Node, 0, limit+1)
	for {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil {
			return nil, false, err
		}
		for _, node := range page {
			if !inheritedIntentResult(node.State) {
				continue
			}
			node.SourceTaskID = sourceTaskID
			node.Inherited = true
			out = append(out, node)
			if len(out) > limit {
				return out[:limit], true, nil
			}
		}
		if !more || len(page) == 0 {
			return out, false, nil
		}
		cursor = page[len(page)-1].ID
	}
}

// explorationGraph returns the whole exploration chain (task graph) as nodes+edges.
func (s *Server) explorationGraph(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, map[string]any{"nodes": []any{}, "edges": []any{}})
		return
	}
	nodes, err := t.Store.Nodes(2000)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	edges, err := t.Store.Edges(5000)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for _, source := range sources {
		sourceNodes, nodeErr := source.Store.Nodes(2000)
		if nodeErr != nil {
			writeError(w, 500, nodeErr)
			return
		}
		sourceEdges, edgeErr := source.Store.Edges(5000)
		if edgeErr != nil {
			writeError(w, 500, edgeErr)
			return
		}
		sourceNodes, sourceEdges = inheritedGraphSnapshot(sourceNodes, sourceEdges, source.Task.TaskID)
		nodes = append(nodes, sourceNodes...)
		edges = append(edges, sourceEdges...)
	}
	writeJSON(w, 200, map[string]any{"nodes": taskNodeDTOs(nodes), "edges": edgeDTOs(edges)})
}

// explorationNodes serves the activity board: this task's own exploration nodes as a
// paged time series (newest first unless ?order=asc), filterable by kind/state
// and a payload substring. Inherited nodes are deliberately out of scope — the
// board reports what this task is doing right now, and paging across the source
// tasks' stores would make the cursor meaningless.
// Response also carries the edges touching the page plus the neighbour nodes
// they point at, so each row can state where it came from and what it produced.
func (s *Server) explorationNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	size := atoiDefault(q.Get("size"), 20)
	t := s.m.ResolveTask(q.Get("task"))
	if t == nil {
		writeJSON(w, 200, map[string]any{
			"items": []any{}, "total": 0, "page": page, "size": size,
			"edges": []any{}, "refs": map[string]any{},
		})
		return
	}
	filter := db.NodeFilter{
		Kinds:  csvValues(q.Get("kind")),
		States: csvValues(q.Get("state")),
		Query:  q.Get("q"),
		Asc:    q.Get("order") == "asc",
	}
	nodes, total, err := t.Store.NodesPage(filter, page, size)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	ids := make([]int64, 0, len(nodes))
	onPage := make(map[int64]bool, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
		onPage[n.ID] = true
	}
	edges, err := t.Store.EdgesTouching(ids)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	neighbourSet := map[int64]bool{}
	for _, e := range edges {
		if !onPage[e.From] {
			neighbourSet[e.From] = true
		}
		if !onPage[e.To] {
			neighbourSet[e.To] = true
		}
	}
	neighbourIDs := make([]int64, 0, len(neighbourSet))
	for id := range neighbourSet {
		neighbourIDs = append(neighbourIDs, id)
	}
	neighbours, err := t.Store.NodesByIDs(neighbourIDs)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	refs := make(map[string]TaskNodeDTO, len(neighbours))
	for _, n := range neighbours {
		refs[i64s(n.ID)] = taskNodeDTO(n)
	}
	writeJSON(w, 200, map[string]any{
		"items":  taskNodeDTOs(nodes),
		"total":  total,
		"page":   page,
		"size":   size,
		"edges":  edgeDTOs(edges),
		"refs":   refs,
		"assets": s.nodeAnchoredAssets(t, append(ids, neighbourIDs...)),
	})
}

// nodeAnchoredAssets resolves the exploration_anchors of the given nodes into
// display-ready asset labels, keyed by node id. Anchors are provenance decoration
// for the activity board; a failure must not cost the caller its page, so errors are
// logged and degrade to "no assets".
func (s *Server) nodeAnchoredAssets(t *Task, nodeIDs []int64) map[string][]FindingAssetDTO {
	out := map[string][]FindingAssetDTO{}
	anchors, err := t.Store.NodeAssets(nodeIDs)
	if err != nil {
		log.Printf("[broadcast] node assets: %v", err)
		return out
	}
	var flat []int64
	for _, ids := range anchors {
		flat = append(flat, ids...)
	}
	assets := s.resolveAssetIDs(flat)
	for nodeID, ids := range anchors {
		if dtos := findingAssetDTOs(ids, assets); len(dtos) > 0 {
			out[i64s(nodeID)] = dtos
		}
	}
	return out
}

// csvValues splits a comma-separated query parameter, dropping empty entries.
func csvValues(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// activity returns the worker execution step log (incremental via ?since=seq).
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, map[string]any{"items": []any{}, "cursor": 0})
		return
	}
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	limit := atoiDefault(r.URL.Query().Get("limit"), 300)
	var intentPtr *int64
	if iv := r.URL.Query().Get("intent"); iv != "" {
		if n, err := strconv.ParseInt(iv, 10, 64); err == nil {
			intentPtr = &n
		}
	}
	var (
		items  []db.Activity
		cursor int64
		err    error
	)
	if intentPtr != nil {
		items, cursor, err = t.Store.ActivityListWithSources(*intentPtr, since, limit)
	} else {
		items, cursor, err = t.Store.ActivityList(nil, since, limit)
	}
	if err != nil {
		log.Printf("[activity] task=%s since=%d limit=%d intent=%v: %v", t.ID, since, limit, intentPtr, err)
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": activityDTOs(items), "cursor": cursor})
}

// parseActivitySession maps a stable session key (main | plan | intent:<ID>) to a
// DB session filter. Goal Agent + Planner both live under worker="planner" (the
// single Plan session); a Worker session is one intent, keyed by its node id.
func parseActivitySession(sess string) (db.ActivitySessionFilter, bool) {
	switch {
	case sess == "" || sess == "main":
		// bare "main" = the current segment; caller resolves MainSeg via the store.
		return db.ActivitySessionFilter{Main: true}, true
	case strings.HasPrefix(sess, "main:"):
		seg, err := strconv.Atoi(strings.TrimPrefix(sess, "main:"))
		if err != nil || seg < 0 {
			return db.ActivitySessionFilter{}, false
		}
		return db.ActivitySessionFilter{Main: true, MainSeg: &seg}, true
	case sess == "plan":
		return db.ActivitySessionFilter{Worker: "planner"}, true
	case strings.HasPrefix(sess, "intent:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(sess, "intent:"), 10, 64)
		if err != nil {
			return db.ActivitySessionFilter{}, false
		}
		return db.ActivitySessionFilter{NodeID: &id}, true
	}
	return db.ActivitySessionFilter{}, false
}

// activitySessionStore resolves a worker session against the current task and
// its direct sources. Planner/main always stay local; inherited intent sessions
// are immutable history and use the source exploration only for reads.
func activitySessionStore(t *Task, filter db.ActivitySessionFilter) (*db.ExplorationStore, int64, error) {
	if filter.NodeID == nil {
		return t.Store, 0, nil
	}
	node, err := t.Store.GetNodeWithSources(*filter.NodeID)
	if err != nil {
		return nil, 0, err
	}
	if node == nil || node.Kind != db.KindIntent {
		return nil, 0, nil
	}
	if !node.Inherited {
		return t.Store, 0, nil
	}
	if !inheritedIntentResult(node.State) {
		return nil, 0, nil
	}
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		return nil, 0, err
	}
	for _, source := range sources {
		if source.Task.TaskID == node.SourceTaskID {
			return source.Store, source.Task.TaskID, nil
		}
	}
	return nil, 0, nil
}

// activityHistory serves one reverse-paginated page of a session's activity history.
// The latest page (no ?before) opens a session; ?before=<id> pulls the older page on
// scroll-up. snapshot_cursor is the TASK-level max id at query time — the client uses
// it to open the single task SSE at since=snapshot_cursor so history (id<=cursor) and
// the live tail (id>cursor) meet with no gap and no overlap.
func (s *Server) activityHistory(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	q := r.URL.Query()
	sess := q.Get("session")
	filter, ok := parseActivitySession(sess)
	if !ok {
		writeErr(w, 400, "bad session")
		return
	}
	if filter.Main && filter.MainSeg == nil { // bare "main" → the current segment
		seg, err := t.Store.CurrentMainSeg()
		if err != nil {
			writeError(w, 500, err)
			return
		}
		filter.MainSeg = &seg
	}
	before := int64(atoiDefault(q.Get("before"), 0))
	limit := min(atoiDefault(q.Get("limit"), 200), 500) // cap so one request can't pull an unbounded slice
	store, sourceTaskID, err := activitySessionStore(t, filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if store == nil {
		writeErr(w, 404, "session not found")
		return
	}
	// The browser tails only this task's broadcaster. Keep the SSE join cursor
	// local even when the opened worker transcript comes from a related task.
	snapshot, err := t.Store.ActivityMaxID()
	if err != nil {
		log.Printf("[activity/history] task=%s session=%s snapshot: %v", t.ID, sess, err)
		writeError(w, 500, err)
		return
	}
	var items []db.Activity
	var hasMore bool
	if sourceTaskID > 0 {
		// Source sessions are readable only while the intent remains terminal. The
		// DB query also removes model reasoning/accounting rows from inherited data.
		items, hasMore, err = store.ActivityPageForTerminalIntent(*filter.NodeID, before, limit)
	} else {
		items, hasMore, err = store.ActivityPage(filter, before, limit)
	}
	if err != nil {
		log.Printf("[activity/history] task=%s session=%s before=%d limit=%d: %v", t.ID, sess, before, limit, err)
		writeError(w, 500, err)
		return
	}
	if sourceTaskID > 0 {
		for i := range items {
			items[i].SourceTaskID = sourceTaskID
			items[i].Inherited = true
		}
	}
	earliest := before
	if len(items) > 0 {
		earliest = items[0].ID
	}
	writeJSON(w, 200, map[string]any{
		"items":           activityDTOs(items),
		"snapshot_cursor": snapshot,
		"earliest_cursor": earliest,
		"has_more":        hasMore,
	})
}

// tokenStats returns per-worker token usage (input/output/cache read/write) for a
// task — main agent, planner, and each work#N.
func (s *Server) tokenStats(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, map[string]any{
			"workers":  []db.TokenUsage{},
			"sessions": []db.SessionTokenUsage{},
			"total":    tokenTotalDTO(db.TokenUsage{}),
		})
		return
	}
	stats, err := t.Store.TokenStatsByWorker()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	sessions, err := t.Store.TokenStatsBySession()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	total, err := t.Store.TokenTotal() // whole-task total (all agents)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workers": stats, "sessions": sessions, "total": tokenTotalDTO(total)})
}

// tokenDailyStats returns global token consumption aggregated by calendar day
// (UTC) across all tasks for the past ?days=N days (default 30).
func (s *Server) tokenDailyStats(w http.ResponseWriter, r *http.Request) {
	days := atoiDefault(r.URL.Query().Get("days"), 30)
	if s.m.pg == nil {
		writeJSON(w, 200, []any{})
		return
	}
	buckets, err := s.m.pg.TokenDailyAll(days)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if buckets == nil {
		buckets = []db.DailyTokenBucket{}
	}
	writeJSON(w, 200, buckets)
}

// conversationTokens returns per-conversation token summaries so the dashboard can
// merge chat (conversation) usage into its per-profile / daily token stats — which
// otherwise count only task (exploration) usage.
func (s *Server) conversationTokens(w http.ResponseWriter, r *http.Request) {
	if s.m.pg == nil {
		writeJSON(w, 200, []any{})
		return
	}
	rows, err := s.m.pg.ConversationTokenSummaries()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// streamActivity is the live SSE tail: it replays history after ?since=<seq>, then
// pushes each newly appended activity for the task. The seq cursor makes history +
// live join gap-free; on reconnect the client passes its last seq to catch any
// dropped events. Optional ?intent=<id> scopes the stream to one worker session.
// getLogs returns recent backend log lines (Seq > since), newest-last.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	limit := atoiDefault(r.URL.Query().Get("limit"), 500)
	lines, cursor := logSink.recent(since, limit)
	writeJSON(w, 200, map[string]any{"items": lines, "cursor": cursor})
}

// getLogsHistory returns older log lines from the DB (before a given db_id).
// GET /api/logs/history?before=<db_id>&limit=200
// Returns {items:[LogLine], has_more: bool}.
func (s *Server) getLogsHistory(w http.ResponseWriter, r *http.Request) {
	if s.m.pg == nil {
		writeJSON(w, 200, map[string]any{"items": []any{}, "has_more": false})
		return
	}
	before := int64(atoiDefault(r.URL.Query().Get("before"), 0))
	limit := atoiDefault(r.URL.Query().Get("limit"), 200)
	if limit > 500 {
		limit = 500
	}
	// If no before given, return the most recent DB rows (mirrors ring restore).
	var (
		rows []*db.DBLog
		err  error
	)
	if before <= 0 {
		rows, err = s.m.pg.RecentLogs(limit)
	} else {
		rows, err = s.m.pg.ListLogsBefore(before, limit)
	}
	if err != nil {
		writeErr(w, 500, locale.Text(responseLanguage(w), "db: ")+locale.ErrorMessage(responseLanguage(w), err))
		return
	}
	items := make([]LogLine, 0, len(rows))
	for _, r := range rows {
		items = append(items, LogLine{
			DBID:  r.ID,
			TS:    r.CreatedAt.Format(time.RFC3339),
			Level: r.Level,
			Tag:   r.Tag,
			Text:  r.Text,
		})
	}
	writeJSON(w, 200, map[string]any{"items": items, "has_more": len(rows) == limit})
}

// streamLogs is the live SSE tail of the backend log: replays history after
// ?since=<seq>, then pushes each new line.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := logSink.subscribe()
	defer unsub()
	send := func(l LogLine) {
		b, _ := json.Marshal(l)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	lines, cursor := logSink.recent(since, 1000)
	for _, l := range lines {
		send(l)
	}
	if cursor > since {
		since = cursor
	}
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case l, ok := <-ch:
			if !ok {
				return
			}
			if l.Seq <= since {
				continue
			}
			since = l.Seq
			send(l)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) streamActivity(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	var intentPtr *int64
	if iv := r.URL.Query().Get("intent"); iv != "" {
		if n, err := strconv.ParseInt(iv, 10, 64); err == nil {
			intentPtr = &n
		}
	}
	// Cursor precedence: the browser's automatic reconnect sends Last-Event-ID (the
	// last id it received) — trust it over the query so an auto-reconnect resumes
	// exactly where it dropped. A fresh/manual connect has no header and passes
	// since=<snapshot_cursor> from the history page instead.
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		if n, err := strconv.ParseInt(le, 10, 64); err == nil {
			since = n
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering

	// Subscribe BEFORE replaying history so events in between aren't lost; dedup the
	// overlap by skipping channel events whose id was already replayed.
	ch, unsub := s.engine.Broadcaster().Subscribe(t.ID)
	defer unsub()

	// Emit a standard SSE id: line so the browser echoes it as Last-Event-ID on
	// auto-reconnect (see cursor precedence above).
	sendSSE := func(a db.Activity) {
		b, _ := json.Marshal(activityDTO(a))
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", a.ID, b)
		flusher.Flush()
	}

	// Compensate the DB backlog after `since` in batches until caught up. This is the
	// gap between the history snapshot and the live tail — NOT the first-page history
	// (that's the /activity/history endpoint). A long task can have far more than one
	// batch, so loop instead of a single fixed read; on a query error log it and close
	// so the client reconnects and retries from its last id (broadcast is lossy — the
	// DB is the source of truth). The Broadcaster keeps buffering live events meanwhile;
	// the id<=since skip below drops any that this replay already covered.
	const replayBatch = 500
	for {
		items, cursor, err := t.Store.ActivityList(intentPtr, since, replayBatch)
		if err != nil {
			log.Printf("[activity/stream] task=%s replay since=%d: %v", t.ID, since, err)
			return
		}
		for _, a := range items {
			sendSSE(a)
		}
		if cursor > since {
			since = cursor
		}
		if len(items) < replayBatch {
			break
		}
	}
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case a, ok := <-ch:
			if !ok {
				return
			}
			if a.ID <= since {
				continue // already replayed
			}
			if intentPtr != nil && (a.NodeID == nil || *a.NodeID != *intentPtr) {
				continue // scoped session: only this intent's steps
			}
			since = a.ID
			sendSSE(a)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// activityDetail lazily returns the full detail blob for one step.
func (s *Server) activityDetail(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no task")
		return
	}
	seq, _ := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	d, err := taskActivityDetail(t, seq)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"detail": d})
}

// taskActivityDetail keeps the legacy local-task behavior (including thinking
// rows), while inherited details are restricted to terminal worker intents.
// This prevents a guessed global activity id from exposing source planner/main
// or in-flight transcripts.
func taskActivityDetail(t *Task, seq int64) (string, error) {
	return t.Store.ActivityDetailWithSources(seq)
}

func (s *Server) getTraffic(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeJSON(w, 200, map[string]any{"enabled": false, "exchanges": []any{}})
		return
	}
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 0)
	size := atoiDefault(q.Get("size"), 100)
	ex, matched, _ := tr.Page(traffic.PageQuery{
		Host:    q.Get("host"),
		Method:  q.Get("method"),
		Query:   q.Get("q"),
		Body:    q.Get("body"),
		Path:    q.Get("path"),
		Status:  q.Get("status"),
		RespMin: int64(atoiDefault(q.Get("resp_min"), -1)),
		RespMax: int64(atoiDefault(q.Get("resp_max"), -1)),
		Sort:    q.Get("sort"),
		Order:   q.Get("order"),
	}, page, size)
	count, _ := tr.Count() // global total, for the stat card
	writeJSON(w, 200, map[string]any{
		"enabled":   s.m.TrafficEnabled(), // reflect the capture toggle
		"proxy":     s.m.ProxyAddr(),
		"count":     count,   // total recorded (unfiltered)
		"total":     matched, // rows matching the current filter (for pagination)
		"page":      page,
		"size":      size,
		"exchanges": trafficDTOs(ex),
	})
}

// getTrafficHosts returns distinct recorded hosts with counts, for the page's
// target picker (pick a host → filter the list, then delete it).
func (s *Server) getTrafficHosts(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeJSON(w, 200, map[string]any{"hosts": []any{}})
		return
	}
	hosts, err := tr.Hosts()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"hosts": hosts})
}

// deleteTraffic removes recorded traffic for every host containing the query's
// host substring (the page's host filter is substring-based, so what you
// filtered is what gets deleted): index rows + each host's file tree, then
// garbage-collects blobs no remaining exchange references. Empty host → 400.
// Returns the number of exchanges deleted.
func (s *Server) deleteTraffic(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		writeErr(w, 400, "missing host")
		return
	}
	n, err := tr.DeleteHost(host)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// deleteTrafficHosts removes traffic for a set of EXACT hosts (JSON body
// {"hosts": [...]}) — the batch path for the page's multi-select delete. Exact
// match, so picking "api.example.com" never sweeps "api.example.com.cn".
// Returns the number of exchanges deleted.
func (s *Server) deleteTrafficHosts(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	var req struct {
		Hosts []string `json:"hosts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	hosts := make([]string, 0, len(req.Hosts))
	for _, h := range req.Hosts {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		writeErr(w, 400, "missing hosts")
		return
	}
	n, err := tr.DeleteHostsExact(hosts)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// deleteAllTraffic purges every recorded exchange, then compacts the index so
// the space is actually returned to the filesystem — an emptied index is the one
// moment a full rewrite is cheap. Evidence already bound to a finding lives in
// the evidence store and is deliberately left alone. Returns the number of
// exchanges deleted and the bytes of index reclaimed.
func (s *Server) deleteAllTraffic(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	n, reclaimed, err := tr.DeleteAll()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n, "reclaimed": reclaimed})
}

// getTrafficExchange returns the full raw request/response of one exchange,
// read on demand from the traffic tree (bodies are not in the paged list).
func (s *Server) getTrafficExchange(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, 400, "missing id")
		return
	}
	req, resp, err := tr.Get(id)
	if err != nil {
		writeError(w, 404, err)
		return
	}
	writeJSON(w, 200, map[string]any{"req": req, "resp": resp})
}

// getTrafficBlob streams one oversized body by its sha256. Bodies past the inline
// threshold are not carried by the exchange endpoint — it returns a preview plus
// an "@blob sha256:<hash>" pointer — so this is how the UI fetches them whole.
// Streamed rather than buffered: these are the bodies too large to hold in memory.
func (s *Server) getTrafficBlob(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	hash := strings.TrimSpace(r.URL.Query().Get("hash"))
	if hash == "" {
		writeErr(w, 400, "missing hash")
		return
	}
	f, size, err := tr.Blob(hash)
	if err != nil {
		writeError(w, 404, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", hash+".bin"))
	if _, err := io.Copy(w, f); err != nil {
		log.Printf(locale.Text(responseLanguage(w), "[traffic] Blob download %s interrupted: %v"), hash, err)
	}
}

// getSettings returns the runtime app settings the UI toggles. The Brave API key
// is returned as a boolean presence flag (brave_key_set), never the value itself,
// so the UI can show "configured" without echoing the secret back.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.settingsPayload())
}

func (s *Server) settingsPayload() map[string]any {
	localOnly, effectiveCIDRs := s.effectiveLLMPolicy()
	on, backend, braveKey, tavilyKey, proxy := s.m.WebSearch()
	pyStored, _, _ := s.m.pg.GetSetting(settingPythonInterp)
	concOn, concLimit := s.m.ConcurrencyLimit()
	if concLimit == 0 {
		concLimit = defaultConcurrencyLimit // Return a reasonable default to the UI even when disabled.
	}
	return map[string]any{
		"language":                 string(locale.ServerDefault()),
		"traffic_capture":          s.m.TrafficEnabled(),
		"agent_traffic_binding":    s.m.pg.GetBool(settingAgentTrafficBinding, false),
		"llm_record":               s.m.LLMRecordEnabled(),
		"web_search_enabled":       on,
		"web_search_backend":       backend,
		"brave_key_set":            strings.TrimSpace(braveKey) != "",
		"tavily_key_set":           strings.TrimSpace(tavilyKey) != "",
		"web_search_proxy":         proxy,                       // Independent HTTP/HTTPS/SOCKS5 egress proxy; empty means direct.
		"global_proxy":             s.m.GlobalProxy(),           // Global HTTP/HTTPS/SOCKS5 egress proxy for all target traffic; empty means direct.
		"python_interpreter":       strings.TrimSpace(pyStored), // User/automatically configured value; empty uses runtime detection.
		"workers":                  s.m.Workers(),               // Concurrent worker agents, default 3; applies to subsequently started tasks.
		"task_concurrency_enabled": concOn,                      // Task concurrency-limit switch, disabled by default.
		"task_concurrency_limit":   concLimit,                   // Maximum simultaneous tasks, default 5 when enabled.
		// Per-profile in-flight LLM request cap; 0 = unlimited. Each profile's
		// provider gets its own semaphore, shared across its agents.
		"llm_max_concurrent":                s.m.LLMMaxConcurrent(),
		"local_llm_only":                    localOnly,
		"local_llm_allowed_cidrs":           s.savedLLMCIDRs(),
		"local_llm_effective_allowed_cidrs": effectiveCIDRs,
		// LLM failover is off by default. Enabled global-active agents switch profiles when unavailable.
		// bind_fallback matters only with failover enabled and is also off by default.
		"llm_pool_enabled":       s.m.LLMPoolEnabled(),
		"llm_pool_bind_fallback": s.m.LLMPoolBindFallback(),
		// Constraint injection defaults on for all roles, adding task allow/deny rules to their system prompts.
		"constraints_inject_planner": s.constraintInjectPlanner(),
		"constraints_inject_worker":  s.constraintInjectWorker(),
		// Experimental noa model-driven compaction is off by default. It replaces built-in compaction for
		// all four platform agent roles; read once per run, affecting subsequently started runs.
		"noa_compaction": s.m.NoaCompactionEnabled(),
		// Global finding-notification settings. Individual channels are managed separately through /api/notify/*;
		// only the three settings shared across all channels belong here.
		"notify_enabled":             s.m.pg.GetBool(settingNotifyEnabled, true),
		"notify_public_base_url":     notifyPublicBaseURL(s.m.pg),
		"notify_digest_interval_min": notifyDigestIntervalMin(s.m.pg),
	}
}

// notifyPublicBaseURL reads the external address used for notification links.
func notifyPublicBaseURL(pg *db.DB) string {
	v, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	return v
}

// notifyDigestIntervalMin reads digest minutes, falling back to the default if absent or invalid.
// Return the effective default instead of an empty string so the UI can display the active value.
func notifyDigestIntervalMin(pg *db.DB) int {
	v, ok, _ := pg.GetSetting(settingNotifyDigestMinutes)
	if !ok {
		return notifyDefaultDigestMinutes
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return notifyDefaultDigestMinutes
	}
	return n
}

// pgDetectPython re-runs interpreter detection, stores + returns it.
func (s *Server) pgDetectPython(w http.ResponseWriter, r *http.Request) {
	p := detectPython()
	if p == "" {
		writeErr(w, 404, locale.Text(responseLanguage(w), "Python was not detected (neither python3 nor python is on PATH)"))
		return
	}
	if err := s.m.pg.SetSetting(settingPythonInterp, p); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"python_interpreter": p})
}

// putSettings applies a settings change. Toggling traffic_capture rebuilds the
// agents (applyLLM) so the new proxy/traffic-tools/prompt state takes hold — when
// off, agents get no proxy config, no traffic tools, and no proxy prompt content.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language            *string `json:"language"`
		TrafficCapture      *bool   `json:"traffic_capture"`
		AgentTrafficBinding *bool   `json:"agent_traffic_binding"`
		LLMRecord           *bool   `json:"llm_record"` // LLM recording defaults off and changes immediately without rebuilding agents.
		// Web search. WebSearchEnabled/Backend toggle the tool + backend; BraveKey/TavilyKey
		// are optional — omit (null) to leave a stored key untouched, send "" to clear.
		WebSearchEnabled *bool   `json:"web_search_enabled"`
		WebSearchBackend *string `json:"web_search_backend"`
		BraveKey         *string `json:"brave_search_api_key"`
		TavilyKey        *string `json:"tavily_search_api_key"`
		WebSearchProxy   *string `json:"web_search_proxy"`   // Independent HTTP/HTTPS/SOCKS5 proxy; null preserves it and empty clears it.
		GlobalProxy      *string `json:"global_proxy"`       // Global HTTP/HTTPS/SOCKS5 proxy; null preserves it and empty selects direct connections.
		PythonInterp     *string `json:"python_interpreter"` // Python interpreter path for custom script tools.
		Workers          *int    `json:"workers"`            // Positive concurrent-worker count; applies to subsequently started tasks.
		// Task concurrency caps running tasks. Disabled means unlimited; enabled queues overflow and admits it when slots open.
		ConcurrencyEnabled *bool `json:"task_concurrency_enabled"`
		ConcurrencyLimit   *int  `json:"task_concurrency_limit"`
		// LLMMaxConcurrent caps simultaneous in-flight LLM requests per profile
		// (0 = unlimited). Changing it rebuilds providers so the new cap takes hold.
		LLMMaxConcurrent     *int      `json:"llm_max_concurrent"`
		LocalLLMOnly         *bool     `json:"local_llm_only"`
		LocalLLMAllowedCIDRs *[]string `json:"local_llm_allowed_cidrs"`
		// LLM failover and bound-profile fallback switches both require rebuilding provider chains;
		// use the changed -> applyLLM path below.
		LLMPoolEnabled      *bool `json:"llm_pool_enabled"`
		LLMPoolBindFallback *bool `json:"llm_pool_bind_fallback"`
		// Constraint-injection flags default on and are read each planner/worker round without rebuilding agents.
		ConstraintsInjectPlanner *bool `json:"constraints_inject_planner"`
		ConstraintsInjectWorker  *bool `json:"constraints_inject_worker"`
		// Experimental noa compaction defaults off and is read per run; subsequent runs update without rebuilding agents.
		NoaCompaction *bool `json:"noa_compaction"`
		// The notification engine reads these three global settings each cycle, applying changes immediately
		// without rebuilding agents or restarting the process.
		NotifyEnabled    *bool   `json:"notify_enabled"`
		NotifyBaseURL    *string `json:"notify_public_base_url"`
		NotifyDigestMins *int    `json:"notify_digest_interval_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, err)
		return
	}
	if req.Language != nil {
		lang := locale.Lang(*req.Language)
		if !locale.Supported(lang) {
			writeError(w, 400, locale.Errorf("language must be one of %s", locale.SupportedLanguageNames()))
			return
		}
		if err := s.m.pg.SetSetting(settingLanguage, string(lang)); err != nil {
			writeError(w, 500, err)
			return
		}
		locale.SetServerDefault(lang)
	}
	if req.ConstraintsInjectPlanner != nil {
		if err := s.m.pg.SetBool(settingConstraintsInjectPlanner, *req.ConstraintsInjectPlanner); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if req.ConstraintsInjectWorker != nil {
		if err := s.m.pg.SetBool(settingConstraintsInjectWorker, *req.ConstraintsInjectWorker); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if req.NoaCompaction != nil {
		// Per-run resolvers apply changes to subsequent runs without applyLLM reconstruction.
		if err := s.m.SetNoaCompaction(*req.NoaCompaction); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	// Notification globals are reread each delivery cycle and take effect without restart.
	if req.NotifyEnabled != nil {
		if err := s.m.pg.SetBool(settingNotifyEnabled, *req.NotifyEnabled); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if req.NotifyBaseURL != nil {
		// Trim trailing slashes because notification links use fmt.Sprintf("%s/function/...");
		// retaining a slash would generate a double-slash path such as //function/....
		base := trimTrailingSlash(strings.TrimSpace(*req.NotifyBaseURL))
		if base != "" && !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			writeErr(w, 400, locale.Text(responseLanguage(w), "Return-link URL must start with http:// or https://"))
			return
		}
		if err := s.m.pg.SetSetting(settingNotifyPublicBaseURL, base); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if req.NotifyDigestMins != nil {
		// Minimum digest interval is one minute; use realtime channel mode for faster delivery.
		if *req.NotifyDigestMins < 1 || *req.NotifyDigestMins > 24*60 {
			writeErr(w, 400, locale.Text(responseLanguage(w), "Digest interval must be between 1 and 1440 minutes"))
			return
		}
		if err := s.m.pg.SetSetting(settingNotifyDigestMinutes, strconv.Itoa(*req.NotifyDigestMins)); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if req.Workers != nil {
		if err := s.m.SetWorkers(*req.Workers); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	if req.ConcurrencyEnabled != nil || req.ConcurrencyLimit != nil {
		// Partial PUT preserves omitted fields so changing one cannot reset another.
		curOn, curLimit := s.m.ConcurrencyLimit()
		if curLimit == 0 {
			curLimit = defaultConcurrencyLimit
		}
		on, limit := curOn, curLimit
		if req.ConcurrencyEnabled != nil {
			on = *req.ConcurrencyEnabled
		}
		if req.ConcurrencyLimit != nil {
			limit = *req.ConcurrencyLimit
		}
		if err := s.m.SetConcurrency(on, limit); err != nil {
			writeError(w, 400, err)
			return
		}
		// Reconcile immediately: disabling admits all queued tasks; raising the limit fills slots without waiting for a tick.
		go s.reconcileConcurrency()
	}
	if req.PythonInterp != nil {
		if err := s.m.pg.SetSetting(settingPythonInterp, strings.TrimSpace(*req.PythonInterp)); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if req.LLMRecord != nil {
		// The recorder reads this flag on each call; changes need no applyLLM reconstruction.
		if err := s.m.SetLLMRecordEnabled(*req.LLMRecord); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	changed := false
	if req.LocalLLMOnly != nil || req.LocalLLMAllowedCIDRs != nil {
		if err := s.saveLLMPolicy(req.LocalLLMOnly, req.LocalLLMAllowedCIDRs); err != nil {
			writeError(w, 400, err)
			return
		}
		s.invalidateProfileAgents()
		changed = true
	}
	if req.LLMPoolEnabled != nil {
		if err := s.m.SetLLMPoolEnabled(*req.LLMPoolEnabled); err != nil {
			writeError(w, 500, err)
			return
		}
		changed = true // the provider chain itself changes shape → rebuild
	}
	if req.LLMPoolBindFallback != nil {
		if err := s.m.SetLLMPoolBindFallback(*req.LLMPoolBindFallback); err != nil {
			writeError(w, 500, err)
			return
		}
		changed = true
	}
	if req.LLMMaxConcurrent != nil {
		if err := s.m.SetLLMMaxConcurrent(*req.LLMMaxConcurrent); err != nil {
			writeError(w, 400, err)
			return
		}
		// Providers embed the semaphore at build time: drop the per-profile caches and
		// mark changed so applyLLM also rebuilds the global provider (llmProv/llmDirect).
		s.invalidateProfileAgents()
		changed = true
	}
	if req.LLMPoolEnabled != nil || req.LLMPoolBindFallback != nil {
		// Pinned tasks' planner/worker and per-profile chat agents hold providers
		// built under the OLD switch state — drop them so they pick up the new one.
		s.invalidateProfileAgents()
	}
	if req.AgentTrafficBinding != nil {
		if err := s.m.pg.SetBool(settingAgentTrafficBinding, *req.AgentTrafficBinding); err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if req.TrafficCapture != nil {
		if err := s.m.SetTrafficEnabled(*req.TrafficCapture); err != nil {
			writeError(w, 500, err)
			return
		}
		changed = true
	}
	if req.GlobalProxy != nil {
		// Validation failure (bad scheme/host) is a client error, not a 500.
		if err := s.m.SetGlobalProxy(*req.GlobalProxy); err != nil {
			writeError(w, 400, err)
			return
		}
		changed = true // capture-off egress is baked into agents at build time → rebuild
	}
	if req.WebSearchEnabled != nil || req.WebSearchBackend != nil || req.BraveKey != nil || req.TavilyKey != nil || req.WebSearchProxy != nil {
		// Fill unspecified fields from current state so a partial PUT doesn't reset them.
		on, backend, _, _, _ := s.m.WebSearch()
		if req.WebSearchEnabled != nil {
			on = *req.WebSearchEnabled
		}
		if req.WebSearchBackend != nil {
			backend = *req.WebSearchBackend
		}
		if err := s.m.SetWebSearch(on, backend, req.BraveKey, req.TavilyKey, req.WebSearchProxy); err != nil {
			writeError(w, 500, err)
			return
		}
		changed = true
	}
	if changed {
		// rebuild agents so the new proxy/tools/prompt/web-search take hold (only if LLM configured).
		s.cfgMu.Lock()
		cfg, on := s.llmCfg, s.llmOn
		s.cfgMu.Unlock()
		if on {
			if err := s.applyLLM(cfg); err != nil {
				writeError(w, 500, err)
				return
			}
		}
	}
	writeJSON(w, 200, s.settingsPayload())
}

// testWebSearch runs a real "test" search with the given (or currently saved)
// backend/proxy/key to verify the config can actually reach a search backend —
// mirroring testLLM. Backend/proxy come from the request (so the form's unsaved
// edits are tested); empty API keys fall back to stored values so the user need
// not retype them. Always 200 with {ok, error?, count?, backend?}.
func (s *Server) testWebSearch(w http.ResponseWriter, r *http.Request) {
	if strict, _ := s.effectiveLLMPolicy(); strict {
		writeJSON(w, 200, map[string]any{"ok": false, "error": locale.Text(responseLanguage(w), agent.WebSearchLocalOnlyMessage), "local_llm_only": true})
		return
	}

	var req struct {
		Backend   string `json:"web_search_backend"`
		Proxy     string `json:"web_search_proxy"`
		BraveKey  string `json:"brave_search_api_key"`
		TavilyKey string `json:"tavily_search_api_key"`
	}
	// Empty body is fine — fall back entirely to the saved config below.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, 400, err)
		return
	}
	_, backend, storedBraveKey, storedTavilyKey, _ := s.m.WebSearch()
	if strings.TrimSpace(req.Backend) != "" {
		backend = req.Backend
	}
	// Proxy is taken from the form as-is (empty = direct), so testing reflects exactly
	// what's shown — including an intentional "clear proxy to test direct" before saving.
	proxy := strings.TrimSpace(req.Proxy)
	// API keys are secrets the form omits when already saved, so fall back to stored.
	braveKey := storedBraveKey
	if strings.TrimSpace(req.BraveKey) != "" {
		braveKey = req.BraveKey
	}
	tavilyKey := storedTavilyKey
	if strings.TrimSpace(req.TavilyKey) != "" {
		tavilyKey = req.TavilyKey
	}
	cfg := actool.WebSearchConfig{Backend: backend, BraveAPIKey: braveKey, TavilyAPIKey: tavilyKey, Proxy: proxy}
	// Hard cap so a slow/blocked proxy can't hang the request.
	wall := 30 * time.Second
	// DeepSeek credentials come from the active LLM profile, not this form. Each search performs model inference,
	// so allow more than the generic 30 seconds. Do not prejudge configuration validity: this test lets the user
	// verify it, and actual failures below provide better diagnostics than a preliminary guess.
	probeQuery := "test"
	if strings.EqualFold(strings.TrimSpace(backend), deepSeekWebSearchBackend) {
		if strict, _ := s.m.effectiveLLMPolicy(); strict {
			writeJSON(w, 200, map[string]any{"ok": false, "error": locale.Text(responseLanguage(w), agent.DeepSeekLocalOnlyMessage), "backend": backend})
			return
		}
		cfg.DeepSeekBaseURL, cfg.DeepSeekAPIKey, cfg.DeepSeekModel = s.m.deepSeekSearchCreds()
		wall = 120 * time.Second
		// DeepSeek chooses its search terms; a vague "test" may cause it to answer without searching.
		probeQuery = "DeepSeek company official website"
	}
	ctx, cancel := context.WithTimeout(r.Context(), wall)
	defer cancel()
	if strict, _ := s.effectiveLLMPolicy(); strict {
		writeJSON(w, 200, map[string]any{"ok": false, "error": locale.Text(responseLanguage(w), agent.WebSearchLocalOnlyMessage), "backend": backend, "local_llm_only": true})
		return
	}
	var results []actool.SearchResult
	var err error
	if strings.EqualFold(strings.TrimSpace(backend), deepSeekWebSearchBackend) {
		meter := agent.DeepSeekSearchMeter{Store: s.m.PG()}
		if profile, profileErr := s.m.pg.ActiveProfile(); profileErr == nil && profile != nil {
			cfg.DeepSeekBaseURL, cfg.DeepSeekAPIKey, cfg.DeepSeekModel = profile.BaseURL, profile.APIKey, profile.Model
			meter.ProfileName = profile.Name
		}
		results, err = agent.DeepSeekWebSearchProbe(ctx, cfg, meter, probeQuery, 3)
	} else {
		results, err = actool.WebSearchProbe(ctx, cfg, probeQuery, 3)
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": locale.ErrorMessage(responseLanguage(w), err), "backend": backend})
		return
	}
	if len(results) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "error": locale.Text(responseLanguage(w), "Search returned no results (possibly rate-limited or proxy unavailable)"), "backend": backend})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(results), "backend": backend})
}

// mainSessions lists the task's main-agent conversation segments (newest-first) and
// the current one. The frontend renders these as switchable main-agent sessions.
func (s *Server) mainSessions(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	list, err := t.Store.ListMainSessions()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	current := 0
	if len(list) > 0 {
		current = list[0].Seq // newest-first
	}
	writeJSON(w, 200, map[string]any{"sessions": list, "current": current})
}

// newMainSession starts a fresh main-agent conversation segment. Only the segment
// counter advances — the task's exploration graph, assets and goal are untouched, so
// the main agent continues over the same task with a clean transcript/context.
func (s *Server) newMainSession(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, 409, locale.Text(responseLanguage(w), "Task is being deleted; a conversation cannot be created"))
		return
	}
	m, err := t.Store.NewMainSession()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"seq": m.Seq, "created_at": rfc3339(m.CreatedAt), "current": m.Seq})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no active task")
		return
	}
	var req struct {
		Message     string           `json:"message"`
		Attachments []chatAttachment `json:"attachments"`
		Seg         *int             `json:"seg"`
	}
	if !decodeConversationRequest(w, r, &req) {
		return
	}
	seg, err := t.Store.CurrentMainSeg()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if req.Seg != nil && *req.Seg >= 0 {
		seg = *req.Seg
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	s.admitCompatibilityChat(w, r, chatstate.SessionRef{TaskID: id, ExplorationID: t.ExpID, MainSeg: seg}, req.Message, req.Attachments)
}

func (s *Server) cancelTaskChat(taskID string, cause error) bool {
	s.chatMu.Lock()
	cancel := s.chatCancel[taskID]
	if cancel != nil {
		cancel(cause)
	}
	s.chatMu.Unlock()
	return cancel != nil
}

func (s *Server) finishTaskChat(taskID string, cancel context.CancelCauseFunc) {
	cancel(agent.AbortChatTurnFinished)
	s.chatMu.Lock()
	delete(s.chatBusy, taskID)
	delete(s.chatCancel, taskID)
	s.chatMu.Unlock()
}

// taskChatStatus reports the authoritative state of the task's main-agent turn.
// Activity timestamps are not a reliable proxy because a tool or LLM call may run
// for minutes without emitting an intermediate frame.
func (s *Server) taskChatStatus(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	s.chatMu.Lock()
	running := s.chatBusy[t.ID]
	s.chatMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"running": running})
}

// stopChat aborts the in-flight main-agent turn for a task (manual stop button).
// Mirrors pgStopConversation: cancels only the current turn; planner/workers are
// unaffected and continue running. The user can send a new message immediately.
func (s *Server) stopChat(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	if err := s.m.pg.PauseChatGate(r.Context(), chatstate.SessionRef{TaskID: id, ExplorationID: t.ExpID}, -1); err != nil {
		chatControlError(w, err)
		return
	}
	s.cancelTaskChat(t.ID, agent.AbortChatStoppedByUser)
	writeJSON(w, 200, map[string]any{"status": "stopping"})
}

// fallbackChat handles human steering without an LLM through simple commands and task-state summaries.
func (s *Server) fallbackChat(t *Task, msg string, langs ...locale.Lang) string {
	m := strings.TrimSpace(msg)
	intentText, intentCommand := steeringCommandText(m, "intent", "의도", "意图")
	hintText, hintCommand := steeringCommandText(m, "hint", "힌트", "提示")
	switch {
	case intentCommand:
		text := intentText
		_, _ = t.Store.AddIntent(map[string]any{"summary": text}, 9, nil, "human")
		return locale.Text(locale.First(langs), "Added a high-priority intent: ") + text
	case hintCommand:
		text := hintText
		_, _ = t.Store.AddNode(db.KindHint, map[string]any{"text": text}, 0, "active", "human", nil)
		return locale.Text(locale.First(langs), "Hint saved for the planner's next round: ") + text
	default:
		assetCounts, _ := s.m.Assets().CountsByType()
		assets := 0
		for _, c := range assetCounts {
			assets += c
		}
		fnd, _ := t.Store.ListByKind(db.KindFinding, 1000)
		fr, _ := t.Store.Frontier(1000)
		return fmt.Sprintf(locale.Text(locale.First(langs), "(Rule mode, no LLM configured) Assets: %d; queued intents: %d; confirmed findings: %d.\nCommands: \"intent ...\" adds an intent; \"hint ...\" adds a planner hint."), assets, len(fr), len(fnd))
	}
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no active task")
		return
	}
	findings, _ := t.Store.ListByKind(db.KindFinding, 1000) // Findings only; facts have separate KindFact nodes and do not enter the report.
	counts := map[string]int{}
	for _, ty := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
		ns, _ := s.m.Assets().QueryByType(ty, 100000, 0)
		if len(ns) > 0 {
			counts[ty] = len(ns)
		}
	}
	md := report.Markdown(report.Input{
		Title: t.Description, Goal: t.Goal, GeneratedAt: time.Now(),
		AssetCounts: counts, Findings: findings, Language: locale.FromRequest(r),
	})
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(md))
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, []any{})
		return
	}
	writeJSON(w, 200, map[string]any{"entries": t.Guard.Audit(), "attributions": t.Guard.Attributions()})
}

// gc is a no-op stub (GC not yet implemented in the new asset store).
func (s *Server) gc(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"removed": 0})
}

// --- utils ---

// corsOriginsEnv lists the browser origins allowed to call /api cross-origin
// (comma-separated, e.g. "https://ui.example.com"; "*" restores the old
// allow-everything behaviour). The embedded UI is same-origin and needs none of
// this; the default only covers `next dev` from dev.sh.
const corsOriginsEnv = "ARTEX_CORS_ORIGINS"

var defaultCORSOrigins = []string{"http://localhost:5173", "http://127.0.0.1:5173"}

// corsAllowedOrigins builds the origin allowlist (lower-cased) from the env.
func corsAllowedOrigins() map[string]bool {
	list := defaultCORSOrigins
	if v := strings.TrimSpace(os.Getenv(corsOriginsEnv)); v != "" {
		list = strings.Split(v, ",")
	}
	out := map[string]bool{}
	for _, o := range list {
		if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
			out[o] = true
		}
	}
	return out
}

// cors adds CORS headers only for allowlisted origins. Before this, every origin
// got "Access-Control-Allow-Origin: *" with a 204 preflight, so any web page the
// operator happened to visit could POST to /api/auth/init on a not-yet-initialised
// instance (even one bound to 127.0.0.1) and read the token from the response.
func cors(next http.Handler) http.Handler {
	return corsWith(corsAllowedOrigins(), next)
}

func corsWith(allowed map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			if allowed["*"] || allowed[strings.ToLower(origin)] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": locale.Text(responseLanguage(w), msg)})
}

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}

// steeringCommandText accepts English/Korean commands and legacy Chinese aliases.
// Only the command prefix is interpreted; the user's remaining content is preserved.
func steeringCommandText(message string, prefixes ...string) (string, bool) {
	for _, prefix := range prefixes {
		if strings.HasPrefix(strings.ToLower(message), strings.ToLower(prefix)) {
			return strings.TrimSpace(message[len(prefix):]), true
		}
	}
	return "", false
}
