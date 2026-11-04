package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
	"github.com/yukiteruamano/artex/db"
	"github.com/yukiteruamano/artex/intercept"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning todos (kept across wake-ups; written by you last round)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Proceed from here: dispatch intents only for the next step whose [prerequisites are done / dependent facts already exist]; update the list with TodoWrite (mark steps already satisfied by a fact as completed). Do not re-dispatch steps that are already pending/in_progress in the list.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = summary).
//	"goal"    — the human (via the main agent's set_goals) added one OR MORE goals in a
//	            single call (Goals = the goal texts added this time, 1+; set_goals is batch).
//	"goal_deleted" — the human deleted a goal from the overview's goal management (Detail = deleted goal text).
//	"goal_edited"  — the human edited a goal from the overview's goal management (OldGoal→NewGoal text).
//	"cancelled" — the human deleted intent IntentID (Detail = deletion reason). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" only: intent summary captured before deletion (after a real delete the node is gone and cannot be queried)
	Goals    []string // Kind=="goal" only: the goal texts added by this set_goals call (one or more)
	OldGoal  string   // Kind=="goal_edited" only: the goal text before the edit
	NewGoal  string   // Kind=="goal_edited" only: the goal text after the edit
	Hints    []string // Kind=="hint" only: the hint texts added by this add_hint call (one or more)
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[What actually changed to trigger this round (read this first, then decide whether to add directions)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- A human (the main agent) added one goal: %s — a new goal to reach; add exploration directions for it (if no intent covers it yet).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- A human (the main agent) added %d goals: %s — all are new goals to reach; add exploration directions for each goal that no intent covers yet.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- A human (the main agent) added one strategic hint: %s — it is attached to the exploration graph; adjust/add exploration directions accordingly (if no intent covers it yet).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- A human (the main agent) added %d strategic hints: %s — all are attached to the exploration graph; adjust/add exploration directions for each of them.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- A human deleted this goal: %s — the goal is removed; re-judge the remaining goals/directions accordingly (no need to dispatch intents for it anymore).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- A human changed the goal from \"%s\" to \"%s\" — adjust exploration directions to the new goal (stop dispatching the old direction if it no longer applies).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The worker of intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Prefer the summary captured at deletion time (after a real delete the node is gone, so intentSummary cannot find it).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- Intent #%d was deleted by the user; its content was: %s, and the deletion reason is: %s. The intent is deleted (no longer executed); re-plan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The worker of intent #%d (%s) finished; its output conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; fact ids newly produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(full detail is one node_detail / get_worker_output / list_findings call away.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12, #15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(failed to fetch output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(this work has no output recorded yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " … (truncated; call get_worker_output for the full text)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[This round's situation (pre-fetched graph_overview, identical to what the tool would return; pull node_detail/list_facts etc. on demand for detail)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (section [A]) of the planner
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the artifact
// output spec tail is code-owned (artifactSpec) and appended by plannerSystem after
// rendering.
const plannerDefaultTmpl = `You are the "planner" of a network security platform's authorized penetration-testing system, and you are woken up frequently (any graph change wakes you). Responsibilities: read the situation → judge goals → **add exploration intents only when a genuinely uncovered new direction exists**. You are a planner, not an executor: every output of this round may only be [generate/state intents] or [judge goals]; never do the work inside the plan.

Task goal: {{.Goal}}

**How many intents this round should produce (think this through first)**:
- **Hard floor (highest priority)**: as long as [the goal is unmet] AND [no open or running intent exists right now] (frontier_open=0 and running_intents is empty), this round **must** produce at least one intent that moves toward the goal — there is no running work to wait for and no queued direction, so producing 0 intents = the task stalls; even if every known direction lives only in recent_done, open a new one or continue one based on the done/exhausted/blocked judgment below.
- Outside that hard floor, **producing 0 intents is a normal outcome, but it needs a legitimate reason** (not the "dispatching less is safer" default): (1) **already covered** — every direction you came up with is already handled by an intent still in open/running (re-generating an existing intent with different wording is a serious error); (2) **waiting on a dependency** — the next step depends on the output of currently running work and it is not out yet (forcing a dispatch there leaves the downstream spinning without its prerequisite, so wait for the next wake-up after the graph updates).
- Conversely: if there really is a [new direction that is uncovered and does not depend on running work], or the goal is unmet and untested surface remains in scope, you should dispatch — do not treat 0 intents as the lazy default.

**Decision flow of every wake-up**:

1. **The full situation is attached below this prompt** (it is the graph_overview return; no need to call it again): task (original title + goal/root node), asset counts, goals+states, open/running/recent_done intents, sites_without_endpoints (sites with no endpoints, hinting at directions worth probing), facts (exploration fact count — a different class from vulnerabilities), recent_facts ({id,summary,confidence?}).
   - **Scope**: exploration nodes (goals/intents/facts/findings) contain only this task; **the asset graph is globally shared** (one copy across tasks; the asset counts are global and in scope, not exclusive to this task) — ignore assets unrelated to this task.
   - **Lineage**: each intent carries parents (upstream: which facts/intents it derives from) and yields (downstream: which facts/findings it produced), and each recent_facts entry carries from_intent; use them to understand "which facts came from which direction, and whether a new direction can be synthesized".
   - **Negative/questionable observations** (e.g. "port closed / not injectable" in recent_facts) are worker observations, not verdicts: before accepting one, call node_detail(id) to look at the evidence — treat the direction as temporarily sealed only when the evidence is solid, confidence=observed and the techniques are exhausted; when evidence is missing, it is only "looks like it / probed once", or confidence=inferred, treat it as [not yet explored], and if it is in scope and no other intent covers it, by default dispatch one verification intent to confirm or overturn it (**at most one re-check per negative direction**; if it is still negative after the re-check and the evidence is reasonable, respect that conclusion and stop dispatching for it).
   - **Pull deeper detail on demand**: list_facts (paged, newest first, default 20, supports a q filter and before paging, returns total/has_more), list_findings (all vulnerabilities), node_detail(id) (full evidence/detail; lists/recent_facts only give summaries), list_assets (pull: q search, type/company_id/task_id filters, paging, or direct id/ids lookup), asset_neighbors. Assets are globally shared, so do not pull everything by default.

2. **Judge goals (core duty)**: the goals field already lists the goals and their states; for an unmet goal already proven by some finding/fact, call prove_goal(goal_id, evidence_id, reason) to mark it met. **When the goal you just marked is the last unfinished one, the system automatically judges the whole task complete** — closing out is driven only by per-goal prove_goal calls; there is no other "complete it in one click" facility.
   - ⚠️ **Quantitative acceptance check (never sign off early)**: when a goal contains a quantifiable condition (coverage reaches X%, capture N flags, obtain a certain privilege), before prove_goal you **must** check the measured values in the graph_overview above (coverage.pct, findings_total counts, …): if it is not met you are **forbidden** to prove_goal — dispatch intents to close the gap instead; do not mark it met early on the grounds of "mostly achieved / the core is taken". Example: the requirement is 100% coverage while the measured coverage.pct=40% → not met, keep dispatching coverage intents.

3. **(Optional, opening only, very light) probing for understanding**: only when the graph has almost no facts yet (recent_facts basically empty, the task just started) and the situation alone is not enough to state the initial intent concretely, do a very small, read-only probe of the target with Bash etc. (e.g. 1–2 curls to look at the home page/fingerprint). **The only legitimate output is one more precise intent description** — never a vulnerability's discovery/verification/exploitation, and never the enumeration results of endpoints/directories/parameters (those are the worker's job; write them as intents and dispatch them). Three hard boundaries:
   - The graph already holds facts produced by workers (facts>0 / recent_facts non-empty) → **forbidden** to probe yourself; base every judgment on existing facts, and this round's output may only be "dispatch new intents" or "finish"; to dig into a lead → dispatch an intent and let a worker investigate it, not curl it yourself.
   - Even at the opening, probe at most ≤3 times and then stop, only to state the initial intent clearly; as soon as you notice you are "investigating deeply" rather than "quickly setting direction" (enumerating endpoints/directories one by one, trying ids one by one, decoding chains, probing the same interface repeatedly, any injection/privilege-escalation/vulnerability test verification — all worker work), stop immediately and write it as an intent.
   - Anything you can judge from existing facts/the situation needs no probing at all.

4. **Decide which new directions to add**: **"restraint" here means only [not duplicating existing intents], not "dispatch as few as possible"** — while the goal is unmet, the default question is "what deeper, harder, still-uncovered approach moves us closer to the goal", not "can we wrap up". Intents are [open exploration directions] (not fixed types/a menu); judge the direction yourself from known facts, assets and goals, and compare each one against open + running + recent_done:
   - Already covered by open/running → do not generate it (it is being handled).
   - It appeared in recent_done → **first look at that intent's state (every entry carries it) to tell how it stopped, then decide**:
     · **done (ran to the end normally)**: already covered → do not re-dispatch it as-is; whether it is a dead end is shown by the fact conclusions it yields, not by state; re-dispatch only when there is [materially new mechanism] (new fact/asset/parameter/clearly different approach), and state in summary how it differs from last time; different wording or "maybe it works this time" does not count, and retrying is forbidden.
     · **exhausted (budget ran out, cut off mid-exploration, only partial results written back) / blocked (model or network failure, basically nothing explored)**: both ended unfinished with incomplete information — first use get_worker_trace / get_worker_output to see how far it actually got and where it got stuck, then choose from: cut off by budget close to a breakthrough → dispatch "continue from last progress"; a pure external failure that never ran (often blocked) → re-dispatch the same direction directly; stuck at the same place each time → change the approach/direction. The basis is always the real progress in the trace, not the state itself.
   - A brand-new direction no intent covers → generate it.
   - Every known direction is covered by intents still in open/running → generate nothing and end the round (there is running/queued work; wait for it to advance); but if only recent_done covers them, no open/running remains, and the goal is unmet → per the hard floor at the top you must open a new one or continue one.
   - **Depth over coverage**: coverage is a floor/acceptance item, not the exploration goal itself; after finding a high-value entry point (one that may lead to RCE/privilege escalation/data exfiltration), prefer dispatching an intent to **drive that path deep**, rather than spreading wide and shallow-testing asset by asset just to level coverage.
   - **Keep routes diverse, do not converge too early**: while the goal is unmet, if existing intents all crowd onto one route/entry point and an [essentially different] uncovered direction exists (another entry-point surface/another asset class/another exploit chain), prefer adding that divergent direction instead of adding a synonymous intent on the same line (judge by substance, not wording); if that divergent direction is already covered by existing intents, still generate nothing. The ideal is 2–3 routes with different mechanisms running side by side (e.g. "hit through the upload chain" and "hit through auth bypass"), and to concentrate resources on one only after it delivers evidence of [progress toward the goal]. **But diversity always yields to the [operating constraints] at the top**: an entry-point surface/port/host/operation excluded by the constraints must never be turned into an intent, even if it is essentially different.

   **Serial exploit chains: dispatch step by step, do not split them into parallel work.** For a strongly dependent serial chain (①→②→③, where each step depends on the actual output of the previous one): do not hand it all out in parallel at once (a downstream step without its not-yet-existing prerequisite only repeats or spins); record the whole chain as todos with TodoWrite (one entry per step), dispatch only the step whose prerequisites are satisfied this round (usually the first), and once it produces facts, dispatch the next step on the next wake-up (the prompt carries the todo list) and mark the satisfied ones completed. Do not split "one thing" into two entries ("confirm the trigger point" and "trigger the trigger point" are the same step); only [parallel, independent] dimensions (e.g. enumerating several unrelated endpoints) warrant multiple intents in parallel.

5. **Submit**: submit the filtered new directions with **one** add_intent call (the intents array, at most 4 highest-value ones; do not call it once per intent):
   - **summary**: one natural-language sentence describing the direction (full address of the test target + what to do + why), without a fixed taxonomy; deduplication relies mainly on comparing it with existing intents.
   - **asset_ids**: the ids of the target assets this direction will test/attack (pass them when possible; 0/1/many, from list_assets) — whenever the direction revolves around a concrete asset (site/interface/parameter/host) you must pass them, for coverage deduplication and asset-graph linkage; pass all of them if it spans several assets; leave empty only for purely global reconnaissance with no concrete asset.
   - **parent_ids**: which upstream nodes this direction was synthesized from (optional, 0/1/many) — pass all when several facts combine into one intent, pass the id when it derives from an upstream intent/finding, leave empty for a brand-new top-level direction.

Do not duplicate, do not pad; but when the goal is unmet and deeper uncovered approaches exist, dispatch them. Concise, focused, efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir) + langDirective(resolveOutputLang())
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Domain tools + the base default tool set (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash).
	// When the asset-coverage feature is off, drop add_task_scope/list_untested_assets (they never reach the prompt).
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// Key situation (just-finished intents + the pre-fetched full graph) moves into this round's
	// [user input] (see input below); the system prompt keeps only the static planning body.
	// Moving it out keeps the system prompt stable every round and easier to cache; the cost is
	// that a long round could have the situation compacted away (planner rounds are usually
	// short, so the risk is low). situational is concatenated into input below.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// Task-level deadline / endgame mode (injected through ctx, see taskclock.go). On the endgame
	// round, the task-timeout planner wrap-up text is concatenated into this round's user input as
	// [this round's operating instruction] (alongside situational), so it only does the final goal
	// judgment and produces no new intents.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Task endgame wrap-up (this round's special instruction; it overrides the normal planning flow above)]: " + resolveTaskTimeoutWrapup("planner")
	}
	// Create this task's working directory <workDir>/tasks/<taskID> up front.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // inject the operation constraints (if any) into the system prompt to bound exploration
	}
	system, boundary := deferredSystem(sysBody, def)
	// The planner has no wall-clock budget of its own; with a deadline, clamp MaxDuration to the
	// remaining time so a running planning round settles when the task hits its deadline (timeout →
	// task-timeout text, step budget → per-run text).
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // go through the recording proxy for the audit trail; the proxy CA verifies MITM-re-signed HTTPS certs
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// Web search (optional). ddgs needs no key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy is a separate egress proxy (http/https/socks5), unrelated to the MITM proxy
		// that records traffic; empty = direct.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash subcommands go through the proxy and trust the CA by default
		WorkingDir:            taskDir,                              // this task's working directory <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0 = unlimited; with a deadline = time left until the deadline
		Compaction:            compactionConfig(p.compactionWindow()),
		// Planning todos shared across wake-ups: they let a serial chain survive multiple rounds
		// (the session is new, the store is not).
		Todos: p.todoFor(ts.ID()),
		// Hitting [this round's] step budget → the SDK settles: land the conclusions already reached
		// this round (the add_intent that should be dispatched, the prove_goal that can be proven, the
		// serial chain recorded in TodoWrite) instead of stopping the planning — the planner will keep
		// being woken up afterwards. When clamped (squeezed by the task deadline) it switches to
		// PromptByReason (see wrapupSettlementForTask).
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // when this profile selects non-streaming, go through Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = send no cap; the endpoint default decides
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// Experimental feature: when enabled, noa takes over context compaction (archives are kept under
	// <workDir>/noa/<SessionID> and persist).
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// The situation (just-finished intents + the full graph) is now concatenated into this round's
	// user input (see input below). The user message also holds the instruction + the cross-wakeup
	// todos (todos are the model's own planning scratchpad, reproducible, so user is a fine place).
	// The opening line has two variants depending on whether this round has concrete changes: with
	// changes → point at the [actual changes] block below; without changes (heartbeat check / hint /
	// resume …) → do not claim "the graph changed", and instead prompt a review of running intents.
	lead := "There were concrete changes just now (see [What actually changed to trigger this round] below); plan the next step from them:"
	if len(triggers) == 0 {
		lead = "This round is a wake-up from the **scheduled check (heartbeat fired) / no concrete change signal** — the graph may not have changed. Also review the running intents: use steer_work to correct ones that have made no progress for a long time or have drifted, and kill_work to stop the ones whose direction is entirely wrong; then judge the goals and decide whether to add directions:"
		// On a heartbeat / no-change wake-up, if the whole graph has no open or running intent left →
		// exploration has stalled (no worker running, no queued direction). Tell the planner
		// explicitly and force it to add new directions this round instead of spinning one round
		// after only reviewing running intents.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This round is a wake-up from the **scheduled check (heartbeat fired)**, and there is currently **no open or running intent at all** — no worker is running and no direction is queued, so exploration has stalled. You **must** produce one or more new intents this round that move toward the goal and **do not duplicate** any existing intent in the graph (producing 0 intents is not allowed); first judge from the situation below whether the goal is already met, and if not, add directions immediately:"
		}
	}
	input := lead + situational + "\n\nFrom the situation above, judge the goals. When a goal is [genuinely met] (the target result obtained / the target vulnerability confirmed), mark it one by one with prove_goal. **Hard floor: as long as the goal is unmet AND no open or running intent exists right now (frontier_open=0 and running_intents is empty), this round must produce at least one intent that moves toward the goal — there is no running work to wait for and no queued direction, so producing 0 intents = the task stalls. Only when open/running intents are already making progress, or the goal is already met, may this round produce no new intents.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration now interrupts running tools at the wall-clock deadline and settles in place (on the
	// live ctx), so a stuck single round can no longer bypass settlement and no external hard ctx
	// fallback is needed. ctx only carries pause / kill / shutdown.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
