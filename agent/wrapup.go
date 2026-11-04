package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// Wrap-up (settlement) prompt: when an agent is terminated because it [ran out of
// steps (MaxTurns)] or [timed out (run_seconds/MaxDuration)], the SDK's settlement
// phase injects this prompt so the agent first persists what it identified but had
// not written back, then outputs a one-sentence summary — no unfinished work.
//
// Each agent's wrap-up prompt can be overridden per agent in the admin UI (stored in
// agents.wrapup_prompt); when empty, the built-in default here is used. Only the
// [prompt body] is editable; which tools are disabled and the wrap-up's own turn
// budget are code-owned policy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default wrap-up prompts, indexed by agent key. worker reuses the
// historically hard-coded settleWrapUpPrompt (defined in worker.go), planner and
// mainagent each have a version; an unmatched key (a custom agent) falls through to
// the generic fallback.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: the built-in per-agent turn budget for the wrap-up phase
// [itself] (overridable from the admin UI when >0). All get 10 turns so the wrap-up
// phase has enough steps to persist results. Unmatched keys use genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "This planning round's step budget is about to run out. Note that only [this round] ends — the system will keep waking you as the situation changes so you can continue planning. The work itself is not finishing, so there is no need to conclude the whole plan here. Do reflect the conclusions you have already clearly reached this round so the round is not wasted, but **do not invent intents just to have something to wrap up** (having 0 intents this round is a completely normal outcome). (1) If you have already judged an exploration direction that [must be dispatched right now], submit it in one add_intent call (do not sit on a decision you already made; send it now). (2) Mark goals already proven met by some finding or fact with prove_goal (do not skip this). (3) If you identified a serial exploit chain that has to be split into steps, record it with TodoWrite so you can continue dispatching it on the next wake-up. If everything is done, just end this round and output no summary text."

const mainAgentWrapUpDefault = "The step budget is about to run out and this interaction is ending. Do not start any new exploration or action. **In one plain-text sentence only**, summarize the current progress, the key conclusion, and the recommended next step to the user in English."

const genericWrapUpDefault = "This will end soon because the budget is exhausted. First record the results you completed but have not saved yet, then **in one plain-text sentence only** summarize what you did and which key conclusion you reached, in English (this one sentence is shown as the result of this run)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        withWrapupLang(resolveWrapup(agentKey)),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// withWrapupLang appends the output-language directive (langDirective) to the
// wrap-up prompt. Unlike the system prompt, the wrap-up prompt does not get the
// langDirective tail automatically, so it must be appended here for the last
// one-sentence summary the user sees to follow the selected display language. The
// wrap-up prose itself is an operating instruction the model reads (it stays in the
// source language), and this tail enforces the language of user-visible text with
// the highest priority. It is harmless for the planner family, which emits no
// summary. For the source language "en" the tail is skipped: the prompt is already
// English, so the default path keeps the model input identical to the previous
// version (guarding against benchmark drift).
func withWrapupLang(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return prompt
	}
	lang := resolveOutputLang()
	if lang == "en" { // source language: the prompt itself is already English
		return prompt
	}
	return prompt + langDirective(lang)
}

// ---------- Task-level timeout wrap-up text ----------
//
// This is a [separate set] from the per-run wrap-up text: per-run means "the budget
// for this one run is used up"; task timeout means "the whole task is due and about
// to end". The semantics are often opposite (especially for the planner: per-run says
// "do not stop, keep planning", task timeout says "stop planning at the deadline and
// make the final judgment"). Configured only for worker/planner.

// WrapupTaskTimeoutOverride / …TurnsOverride: DB overrides for the task-timeout
// wrap-up text and turn budget (wired to agents.task_timeout_wrapup_prompt /
// _max_turns, worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The whole task has reached its time limit and will end soon** (this is not this run's budget — the entire exploration is ending). This is the last chance. (1) Save [everything] you have already identified but not yet recorded. Record new assets with insert_assets, exploration conclusions and facts with record_fact, and confirmed vulnerabilities with report_finding. (2) Do not start any new command or probe. (3) **At the very end, in one plain-text sentence only**, summarize the key conclusion obtained from this intent, in English."

const plannerTaskTimeoutDefault = "**The whole task has reached its time limit and will end soon** (not this round — the entire task is ending). Perform the final goal judgment based on [all] facts and findings gathered so far. Mark goals proven met by evidence with prove_goal (do not skip this). **Do not generate any new intent** (even if you dispatch one now, it will not run). When the judgment is done, end immediately and output no summary text."

// TaskTimeoutWrapupDefault returns an agent's built-in task-timeout wrap-up text
// (for the admin placeholder / restore-default). Empty when unconfigured
// (mainagent/chat).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey]
}

// resolveTaskTimeoutWrapup: DB override (non-empty) > built-in default. An empty
// string means that agent has no task-timeout text (not worker/planner), in which
// case the caller should fall back to the per-run text.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // falls back to the per-run turn budget
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → this run is squeezed by the task deadline: settling on
//     Timeout means the task is due → task-timeout text; settling on MaxTurns means
//     the steps ran out inside the clamp window while the task still has a few
//     minutes → fall back to the per-run text.
//   - clamped=false → the task still has time: both reasons use the per-run text
//     (i.e. this degrades to wrapupSettlement).
//
// The PromptByReason handed to the harness picks the text per the [actual] reason at
// wrap-up time, so there is no build-time mismatch.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := withWrapupLang(resolveWrapup(agentKey))
	st := &harness.Settlement{
		Prompt:        perRun, // fallback (also the value for both reasons when not clamped)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  withWrapupLang(tt), // the task is due
				harness.ReasonMaxTurns: perRun,             // steps ran out first, the task still has time
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
