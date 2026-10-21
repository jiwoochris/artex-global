package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/locale"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in editable body (section [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = "You decompose authorized penetration-testing objectives. Identify the final outcomes the user wants, rather than planning attack steps.\n\nFIRST: extract operating constraints before decomposing objectives.\nIdentify explicit rules about permitted and prohibited operations in the task objective and description. Register each with set_constraints when present:\n- type=deny: prohibited operations, such as port scanning, production writes/deletes, brute force, or accessing a particular subdomain.\n- type=allow: explicitly allowed or restricted scope, such as passive reconnaissance only or a specific domain.\n- Constraints define operating boundaries; they are neither objectives nor attack steps.\n- Each constraint MUST be self-contained and name concrete targets. Replace references such as \"current target/port/IP/domain/site\" with the exact values from the task. Constraints are injected into execution prompts separately, where contextual references would be ambiguous. For https://abc.example.net, write \"Only test abc.example.net\"; for port 443, write \"Only test target port 443; do not scan other ports.\"\n- Register ONLY constraints explicitly stated or emphasized by the user. Never invent restrictions. If a stated constraint's type is uncertain, use deny conservatively.\n- If no operating constraints are present, do not call set_constraints.\nAfter registering any constraints, decompose objectives.\n\nAn objective is a final, deliverable, verifiable outcome.\nDo NOT list reconnaissance, endpoint scanning, vulnerability analysis/validation processes, attack techniques, or result-verification steps as sub-objectives.\n- One final outcome means one objective.\n- Split only independent final deliverables.\n- Set vulnclass where a specific vulnerability class applies; leave it empty for information-gathering or business-logic outcomes.\n- Never invent objectives the user did not request.\nSubmit the results using set_goals."

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = "\n\nAdditional responsibility: register the authorized asset scope.\nExtract explicitly stated testing assets from the objective/description and register them with add_task_scope. This defines the task's authorization boundary and coverage denominator. Use the minimum scope: only the exact target the user named; NEVER expand it on your own.\n- URL or hostname-bearing address (https://xxx.example.com/path, app.example.com): use the COMPLETE hostname, kind=subdomain, value=the complete hostname. For https://a1b2c3.lab.example.net/path, use a1b2c3.lab.example.net, NOT example.net. Never shorten a subdomain to its root domain.\n- Only a bare root domain with no subdomain (example.com), or an explicit request for the entire site/all subdomains/domain, permits kind=root_domain and value=example.com.\n- IP address/network: kind=ip / cidr, value=the IP or CIDR.\n- Do not register company scope: a new task's company generally does not yet exist in the asset store. Leave company-level scope to later planning.\nRegister only scope explicitly written in the objective/description; never invent or infer unmentioned domains/IPs. In reason, identify the statement supporting the scope for audit. If no explicit asset scope exists, do not call add_task_scope.\nRegister any scope with add_task_scope before submitting objectives with set_goals."

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text background: target scope, flag count, engagement instructions, etc.
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity), policies ...RuntimePolicy) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Goal decomposition is one-shot without a transcript store, so agentcore does
	// not attach a session ID (Prompt only does so when a writer exists). Gateways
	// using that header for prompt caching/sticky routing, such as opencode zen,
	// otherwise return 400 MissingSessionID even though ordinary chat works.
	// Attach a stable exploration-specific ID for cache reuse, distinct from
	// planner/worker IDs and parseable by llmrec.parseSession for attribution.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals", language: locale.FromContext(ctx)}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()}, locale.FromContext(ctx))
	// set_constraints is always available without an asset store. The editable body
	// already instructs constraint extraction before goal decomposition; wire the tool here.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += locale.Text(locale.FromContext(ctx), goalsScopeTail)
	}
	// These tools are constructed directly instead of going through ToolResolve, so
	// without this pass their invocations never reach the usage ledger (set_goals and
	// set_constraints were both missing from tool_usage). RunInfo carries the task and
	// exploration the goals belong to; agentKey is "goals" so the rows are filterable
	// the same way planner/worker rows are.
	tools = MeterTools(tools, "goals", RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	userMsg := locale.Text(locale.FromContext(ctx), "Task objective:\n") + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += locale.Text(locale.FromContext(ctx), "\n\nTask description (background that may include target scope, flag count, or engagement instructions; do not invent unmentioned details):\n") + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	opts := agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys, outputLanguageInstruction(locale.FromContext(ctx))},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// Allow enough turns for constraints, scope, and goals so set_goals is not missed before settlement.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // Non-streaming profiles use Provider.Complete.
		MaxTokens:    maxTokens,    // 0 omits the limit and uses the server default.
	}
	if len(policies) > 0 {
		opts.ToolOutputDir = cmdOutDir(ensureRunDir(dataDir, taskID, 0))
		applyRuntimePolicy(&opts, policies[0])
	}
	captureRun(ctx, opts, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
