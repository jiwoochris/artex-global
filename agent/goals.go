package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
	"github.com/yukiteruamano/artex/db"
)

// goalsDefaultTmpl is the built-in EDITABLE body (section [A]) of the
// goals-decomposer prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You are a penetration-test goal decomposer. Your job is to identify from the user's input the **final result to be achieved**, not to plan attack steps.

**Step one (do this before splitting goals): extract operating constraints**
From the "task goal / task description", identify the operator's explicit rules about [which operations may and may not be performed], and register them one by one with set_constraints (if neither the description nor the goal involves operating constraints, you may skip extracting them):
- type=deny: forbidden operations (e.g. "do not scan ports", "no write/delete operations against production", "no brute force", "do not touch a certain subdomain").
- type=allow: an explicitly allowed/restricted scope of operations (e.g. "passive reconnaissance only", "only against a certain domain").
- A constraint is neither a goal nor an attack step: it prescribes the boundary of operating behavior.
- **Constraints must be [self-contained and hard-code concrete targets]**: replace referring expressions such as "the current target/current port/current IP/current domain/this site" with the **concrete values** from the task goal/description. Constraints are injected separately into the execution-phase prompt, where, without context, a referring expression cannot be resolved.
  Example: the target is https://abc.example.net → write "only abc.example.net may be tested", not "only the current target may be tested"; "test only the target port 443, do not scan other ports", not "test only the current port". If the source only says "the current target" but the target address is explicit, fill the address in.
- **Register only constraints [explicitly written or emphasized] in the goal/description; never invent them**; when unsure about the type, use deny (more conservative).
- If the goal/description truly contains no operating constraints, **do not** call set_constraints.
After registering the constraints (if any), do the goal decomposition below.

**A goal = a final deliverable/verifiable result**

**What is not a goal (never list these as sub-goals)**:
- information gathering, reconnaissance, endpoint scanning
- the process of analyzing and verifying vulnerabilities
- attack steps, exploitation techniques
- result verification steps

**Splitting principles**:
- The user describes a single final goal → output one
- There are multiple **mutually independent** final deliverables → list each of them
- Tag vulnclass when it maps to a clear vulnerability class; leave it empty for information-gathering/business-logic goals
- Never invent goals the user did not mention

Submit the result with set_goals.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Additional duty: register the test asset scope**
Besides decomposing goals, identify the **explicitly given test asset scope** in the "task goal / task description" and register it with add_task_scope (this task's authorization boundary, and the denominator of asset test coverage). **Minimum-scope principle: register only the one target the user explicitly names; never widen it on your own.**
- If the target is a URL or an address with a hostname (e.g. https://xxx.example.com/path, app.example.com) → take its **full hostname**, kind=subdomain, value=full hostname.
  Example: target https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**not** example.net).
  **Never** shorten a hostname that has a subdomain to the root domain — seeing xxx.example.com and registering all of example.com widens the scope beyond the user's target and violates the minimum-scope principle.
- Only when the user gave a **bare root domain with no subdomain at all** (e.g. they literally wrote example.com), or explicitly said "the whole site / all subdomains / the entire domain" → use kind=root_domain, value=example.com.
- A plain IP or network range → kind=ip / cidr, value=IP or CIDR.
- **Do not** register company scope (company) — the task has just been created and the asset system usually does not have this company yet, so the registration cannot happen; company-level scope is left to the later plan phase.
Other rules:
- Register only the scope **explicitly written** in the goal/description; never invent or infer domains/IPs that are not mentioned.
- In reason, briefly note which sentence the basis comes from, for auditability.
- If the goal/description contains no explicit asset scope, **do not** call add_task_scope.
Register the scope with add_task_scope first (if any), then submit the goals with set_goals.`

// goalsSystem assembles the goals-decomposer system prompt: the rendered body
// [A] (DB-overridable), the code-owned scope-extraction tail when add_task_scope
// is wired (withScope), and the code-owned output-language tail [C] last —
// mirroring chatSystem/plannerSystem so a DB-edited body can never drop the tail.
// DecomposeGoalsWithProvider and the localization test share this one assembly, so
// the langDirective tail can't drift between runtime and test. EngagementDescription
// is intentionally left empty: the task description rides in the user message, not
// the {{.EngagementDescription}} var (see DecomposeGoalsWithProvider).
func goalsSystem(dataDir string, withScope bool) string {
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	if withScope {
		sys += goalsScopeTail
	}
	return sys + langDirective(resolveOutputLang())
}

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
// desc is the task's free-text description (background: target scope/flag count/engagement notes, etc.).
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
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Goal decomposition is a one-shot call: no transcript store is attached, so agentcore does not
	// put a session id on the ctx (it only does so when there is a writer, see agentcore.Prompt). But
	// gateways that do prompt caching/sticky routing by the session-id header (opencode zen returns 400
	// MissingSessionID without x-opencode-session) read exactly that ctx value — without it, chat works
	// while decomposition fails with 400. Attach a stable id explicitly: all decomposition requests of
	// one exploration share it (good for cache hits), and it does not collide with the planner/worker
	// naming, so llmrec.parseSession attributes it correctly.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// goalsSystem appends the scope-extraction tail in lockstep (withScope) so the
	// prompt never asks for a tool that isn't present, and it owns the output-language
	// tail last so a DB-edited body can't drop it. Description rides in the user
	// message, NOT the {{.EngagementDescription}} var, so a prompt can't inject it twice.
	withScope := as != nil && taskID > 0
	sys := goalsSystem(dataDir, withScope)
	// set_constraints is always available (it does not depend on the asset store): the body already
	// contains the "extract operating constraints before splitting goals" step (its wording is
	// editable on the agent edit page); here it only needs the tool wired in.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	if withScope {
		tools = append(tools, tsx.addTaskScope())
	}
	userMsg := "Task goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask description (background information, possibly including target scope/flag count/engagement notes; for reference only — do not invent anything it does not mention):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 steps (extract constraints → register scope → split goals) each need one tool call; give
		// enough turns so set_goals is not missed before the wrap-up.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // when this profile selects non-streaming, go through Provider.Complete
		MaxTokens:    maxTokens,    // 0 = send no cap; the endpoint default decides
	}, userMsg, captureEmit)
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
