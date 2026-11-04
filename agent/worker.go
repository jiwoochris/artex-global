package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
	"github.com/yukiteruamano/artex-global/db"
	"github.com/yukiteruamano/artex-global/intercept"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// Note that the deepseek backend differs in nature from the other three: DeepSeek has no
// directly callable search interface; search exists only inside its Anthropic-compatible
// messages API (the web_search_20250305 server tool), so each search consumes one model
// call and the search request is issued by DeepSeek's servers — it does not go through the
// local Proxy and is not captured in the traffic trail.
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
	// DeepSeek* come from the active LLM configuration (only the anthropic-format official
	// DeepSeek endpoint); they are not configured separately and change with the LLM profile.
	DeepSeekBaseURL string
	DeepSeekAPIKey  string
	DeepSeekModel   string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string            // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch       WebSearchOpts     // web_search tool backend selection (off by default)
	tx              *transcript.Store // raw LLM conversation persistence (nil = off)
	window          int               // context window in tokens (for compaction)
	windowFn        func() int        // optional dynamic task-chain minimum
	maxTurns        int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "This run is about to end because its budget is exhausted. Do not run any further command or probe. Handle the following in order. (1) Record, one at a time, the items you already identified above but have not recorded yet. Record new assets with insert_assets, exploration conclusions and facts with record_fact, and confirmed vulnerabilities with report_finding. (2) **At the very end, in one plain-text sentence only**, summarize what you did and which key conclusion you reached, in English. This one sentence is shown to the user as the result of this run, so you must output it."

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (section [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the artifact output
// spec are NOT here — they are code-owned and appended by workerSystem after
// rendering (sections [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `You are the "executor" (work agent) of a network security platform's authorized penetration-testing system. You are given **[one intent]** (a one-sentence exploration direction); your only job: **complete that one intent, write the findings back into the knowledge graph, then stop and return.**

**Boundaries (red lines)**:
1. **Do only the one intent you were given**. **If, while exploring this intent, you glimpse a lead outside it that deserves deeper digging** (a path revealed by an error, a point that may link to other assets, an entry point that looks like another exploit chain), **mention it in one line in the fact's summary and hand it to the planner**.
2. An initial setback (payload filtered / 404 / injection with no output) does not mean the intent is exhausted — work through every bypass for this intent before writing the conclusion;
3. Act only within the authorized scope. If the top of the system prompt carries [operating constraints], that is the highest-priority red line: self-check before running every command/probe, and skip it if it violates them (even if it falls inside the intent you were given).

**Write back as you discover** (only what reaches the graph counts; what stays in your head or in prose does not; write each result immediately instead of saving it for the end, where the step budget will lose it). Three kinds of write-back — do not mix up the graphs:
- **New asset/resource → insert_assets (the asset graph)**: subdomain / service / endpoint / fingerprint / credentials and everything about the asset **itself**. **Register assets only here; exploration conclusions/judgments do not go here, use record_fact.**
- **Exploration conclusion/fact → record_fact (the exploration graph, pass intent_id)**: use it for all of these. **Aggregate several observations into [one] fact** (a one-sentence summary + a detail that expands on the summary and rests on the real execution); do not write one entry per attribute — usually one per intent; fragmenting makes the graph explode — **by default write one and merge whatever fits into detail**; use the facts array to split only when there truly are [fully independent, non-mergeable] conclusions, which is a rare exception, not the norm. **Increments only**: record only what you [newly obtained] this time, and do not re-record existing facts in other words (if it only confirms something existing with nothing new, there is no need to record it). **Only what you actually saw**: give evidence (one line: the command plus the one or two lines of output that prove it best, kept short; detail lives in detail) and mark confidence (observed = directly seen / inferred = inferred from symptoms).
- **Vulnerability confirmed → report_finding (the exploration graph, with PoC, pass intent_id)**: **use it only if you actually triggered it in this run and have reproducible evidence (request/response or command output)**. It is strictly forbidden to treat "version/fingerprint matches a CVE", "the parameter looks injectable" or "inferred from an external vulnerability database/changelog/code diff" as confirmed, and do not substitute querying a CVE database or comparing patch versions for actual triggering. If it cannot be triggered but is suspicious → write one inferred fact with record_fact (the suspicion plus why it was not triggered) for the planner; do not force it into a finding.


When the intent is done, summarize in one sentence what you did and which facts you wrote back.`

// workerTrafficBlock is section [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**Traffic tools**:\n- traffic_search / traffic_get / traffic_blob: look back at responses and find resources already visited; **check the traffic first, do not curl the same URL again**. traffic_search **requires a host** and by default returns only 3 very lightweight index entries (id/method/url/status/resp_len, no response body); pass a larger limit explicitly when you need more; use body_contains for a full-text search inside request/response bodies (at least 3 characters, substring matching supported, e.g. to find passwords/keys/errors/internal addresses); to read the original text of one entry use traffic_get(id), where an oversized body is shown as @blob sha256:<hash> — fetch the full text in segments with traffic_blob(hash)."
}

// artifactSpec is section [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**Intermediate artifact output spec**: scripts, payloads, captured response bodies, temporary data and every other intermediate artifact **must be written inside this task's working directory " + dir + "** (a relative path lands there directly; the absolute path works too) — **do not write to /tmp, do not use other absolute paths**."
}

// workerArtifactSpec is the worker's section [C]: its per-intent run dir is
// pre-created by the engine (ensureRunDir), so it just writes relative paths there
// — no manual mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**Intermediate artifact output spec**: scripts, payloads, captured response bodies, temporary data and every other intermediate artifact **must be written inside this intent's dedicated working directory " + runDir + "** (already created automatically; just write relative paths there, no need to mkdir manually) — **do not write to /tmp, do not use other absolute paths**."
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert != "") + workerArtifactSpec(runDir) + langDirective(resolveOutputLang())
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n[The intent you were given (this run's only task: do just this one, produce only facts, stop when done)]:\n%s\nintent id: %d (pass it when writing back with record_fact / report_finding)", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage is the signal the planner uses to judge "which kind is undertested / should the scope
	// widen", which contradicts the worker's boundary of "do only the intent you were given, do not
	// chase uncovered points" → drop it from the worker view. data is a map exclusive to this worker,
	// so deleting a key does not affect the planner.
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n[Global exploration situation (read-only; helps you place your own intent in the big picture)]:\n" +
		"Below is the current exploration overview of the whole task. It has two uses: first, know what others have already found so you do not duplicate it; second, let you relate your own intent to the global picture while exploring it.\n" +
		"**Diverging is good**: while exploring this intent, think deeply and make associations freely. The only line is — do not actually go execute other intents (that is other workers' job, scheduled by the planner). Whenever you associate something valuable (cross-asset linkage, an entry point that looks like another exploit chain, something suspicious at the global level), **be sure to write it into a fact for the planner** — that is an important output of yours, not optional. Better to report one extra item for the planner to judge than to swallow it.\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // wake the planner on the spot when report_finding persists, carrying which intent + finding
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// The worker deliberately does not get MultiEdit/Glob/Grep: use Edit for precise file edits and
	// Bash (grep/find) for searching — this narrows the tool surface and cuts low-value calls. The
	// other SDK defaults (Read/Write/Edit/LS/Bash/Sleep) stay as they are.
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()

	// The intent is the worker's [single duty, an invariant spanning the whole run] → it goes into the
	// system prompt together with the launch instruction and the raw data of the target assets the
	// intent anchors: the system prompt is rebuilt every run and can never be compacted away, so the
	// intent is always present in long runs and a resumed run does not depend on the transcript still
	// holding that first message. The cost is that per-intent volatile data enters the system prompt
	// and cross-intent cache reuse is lost; that is a deliberate tradeoff (losing the intent is far
	// worse than spending tokens). Diverging from the planner (which keeps its situation block in the
	// user turn) is intentional: the planner is the one producing intents and has no single mandate,
	// while the worker has one. Only the [global situation overview] stays in the launch user message
	// — it can degrade and tolerate staleness, so compacting it away is harmless.
	// This intent's dedicated working directory <workDir>/tasks/<taskID>/i<intentID>, created by the engine first.
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := renderWorkerGraphOverview(tsx.graphOverviewData())
	sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // inject the operation constraints (if any) so the worker obeys them strictly while executing
	}
	// Intent block → intent-anchored asset block → launch instruction, appended to the end of the system
	// prompt in that order (the same append scheme as constraintBlock).
	sysBody += renderIntentTask(intent)
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					sysBody += "\n\nTarget assets corresponding to this intent's asset_ids:\n" + string(b)
				}
				// The assets this intent explicitly targets → automatically added to the task's test scope
				// (same conservative granularity as insertAssets). upsertTaskScope's ON CONFLICT DO NOTHING
				// plus the uq_task_scope unique index guarantee no duplicates; re-runs/retries are idempotent
				// no-ops too. When the asset coverage feature is off, the test scope (the denominator) is no
				// longer accumulated.
				if coverageEnabled {
					for _, a := range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	sysBody += "\n\nStart executing the intent above: do only it, produce only facts, assets and findings, and stop when done."
	system, boundary := deferredSystem(sysBody, def)
	// The task-level deadline (injected through ctx) clamps this run's wall-clock budget and decides
	// the wrap-up text (see taskclock.go).
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle := wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	opts := agentcore.Options{
		Provider:        w.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch goes through the recording proxy, so its HTTP is captured like curl's; loading the
		// proxy CA lets HTTPS certs re-signed by the MITM [verify normally] (instead of disabling
		// verification). Empty proxy = direct.
		EnableWebFetch: true,
		WebFetchProxy:  w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// Web search (optional). ddgs needs no key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy is a separate egress proxy (http/https/socks5), unrelated to the MITM proxy
		// that records traffic; empty = direct.
		EnableWebSearch:       w.webSearch.Enabled,
		WebSearchBackend:      w.webSearch.Backend,
		BraveSearchAPIKey:     w.webSearch.BraveKey,
		TavilySearchAPIKey:    w.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: w.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  w.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   w.webSearch.DeepSeekModel,
		WebSearchProxy:        w.webSearch.Proxy,
		// Bash subcommands' HTTP goes through the recording proxy and trusts its CA by default (tools
		// need no -x/-k).
		BashEnv:    proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: runDir,
		MaxTurns:   w.maxTurns, // 0 = unlimited (configurable in agent management)
		// Wall-clock budget, checked at turn boundaries, never interrupting mid-step; 0 = unlimited. With
		// a task-level deadline it is clamped to min(own budget, time left until the deadline), so this
		// run settles naturally when the task hits its deadline (see taskclock.go).
		MaxDuration: maxDur,
		// Hitting the budget (turns OR duration) → the SDK runs one settlement round (hiding Bash) so
		// what was identified gets written back instead of being left unfinished. When clamped (squeezed
		// by the task deadline) it uses PromptByReason: timeout = task deadline → task-timeout text, step
		// budget = steps ran out inside the clamp window → fall back to the per-run text. Not clamped
		// keeps the plain per-run text.
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. The truncation cap uses the SDK default (30000 characters).
		ToolOutputDir: cmdOutDir(runDir),
		Compaction:    compactionConfig(w.compactionWindow()), // long tool-heavy runs stay within the window
		Todos:         actool.NewTodoStore(),                  // session-scoped temporary todos (TodoWrite), pure planning, dropped on exit
		NonStreaming:  w.nonStreaming(),                       // when this profile selects non-streaming, go through Provider.Complete
		MaxTokens:     w.maxTokens(),                          // 0 = send no cap; the endpoint default decides
	}
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// The intent / launch instruction / intent-anchored assets are already delivered in the system
	// prompt (see the sysBody assembly above). This launch user message only carries the [global
	// situation overview] — degradeable big-picture information, harmless if compacted away. In the
	// rare case the overview fails to marshal and is empty, fall back to a launch sentence so the first
	// turn never has an empty user message.
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "Start executing the intent you were given in the system prompt: do only it, produce only facts, assets and findings, and stop when done."
	}

	// Experimental feature: when enabled, noa takes over context compaction (archives are kept under
	// <workDir>/noa/<SessionID> and persist).
	noaSession := WorkerSessionID(ts.ID(), intent.ID)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	if w.tx != nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "Continue."
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message != "" {
		if alreadyRecorded {
			input = "Continue with the new intent from the last human chat input. Do not repeat actions that are already done."
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n[New intent from human chat input]\n" + message +
				"\n\nExecute this human input now, and once it is done decide from the context whether the original task needs to continue."
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n[New intent from human chat input]\n" + message +
				"\n\nPrioritize this human input."
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err := captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
