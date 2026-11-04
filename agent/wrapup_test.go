package agent

import (
	"strings"
	"testing"
	"unicode"
)

// A2: language of the wrap-up / settlement prompts.
//
// These constants are injected by the settlement phase when a run or task is terminated
// because it hits the step/time budget, and they directly dictate the final summary
// shown to the user as-is. They must therefore (1) be the English source text — each one
// pinned by its opening sentence — (2) carry no stale Korean or Chinese translation, and
// (3) preserve instruction meanings such as "one plain-text sentence" along with the tool names.
//
// The DB seed leaves wrapup_prompt / task_timeout_wrapup_prompt as empty strings (db/db.go's
// builtin agent INSERT does not fill these columns); when they are empty it falls back to these constants.
// In other words, these constants are the only source of the wrap-up text.
func TestWrapupPromptsAreEnglish(t *testing.T) {
	all := map[string]struct{ prompt, opensWith string }{
		"settleWrapUpPrompt":        {settleWrapUpPrompt, "This run is about to end because its budget is exhausted."},
		"plannerWrapUpDefault":      {plannerWrapUpDefault, "This planning round's step budget is about to run out."},
		"mainAgentWrapUpDefault":    {mainAgentWrapUpDefault, "The step budget is about to run out and this interaction is ending."},
		"genericWrapUpDefault":      {genericWrapUpDefault, "This will end soon because the budget is exhausted."},
		"workerTaskTimeoutDefault":  {workerTaskTimeoutDefault, "**The whole task has reached its time limit and will end soon**"},
		"plannerTaskTimeoutDefault": {plannerTaskTimeoutDefault, "**The whole task has reached its time limit and will end soon**"},
	}

	hasScript := func(s string, table *unicode.RangeTable) bool {
		for _, r := range s {
			if unicode.Is(table, r) {
				return true
			}
		}
		return false
	}

	for name, c := range all {
		if !strings.HasPrefix(c.prompt, c.opensWith) {
			t.Errorf("%s: the prompt no longer opens with %q: %q", name, c.opensWith, c.prompt)
		}
		// Tool names are ASCII, so any Hangul or Han character left in the prompt is a stale translation.
		if hasScript(c.prompt, unicode.Hangul) || hasScript(c.prompt, unicode.Han) {
			t.Errorf("%s: a Korean or Chinese translation leaked back in: %q", name, c.prompt)
		}
	}

	// Tool names are identifiers, so they must be preserved as-is and not translated.
	// The worker family (per-run / task-timeout) writes conclusions with record_fact and vulnerabilities with
	// report_finding, and keeps the instruction to summarize in one plain-text sentence at the end.
	mustContain := func(name, p string, subs ...string) {
		for _, s := range subs {
			if !strings.Contains(p, s) {
				t.Errorf("%s: the instruction meaning %q must be preserved but is missing", name, s)
			}
		}
	}
	mustContain("settleWrapUpPrompt", settleWrapUpPrompt,
		"insert_assets", "record_fact", "report_finding", "one plain-text sentence", "plain-text")
	mustContain("workerTaskTimeoutDefault", workerTaskTimeoutDefault,
		"insert_assets", "record_fact", "report_finding", "one plain-text sentence", "plain-text")
	mustContain("genericWrapUpDefault", genericWrapUpDefault, "one plain-text sentence", "plain-text")
	mustContain("mainAgentWrapUpDefault", mainAgentWrapUpDefault, "one plain-text sentence", "plain-text")
	// The planner emits no summary sentence (it only judges and ends) and keeps the intent/goal/todo tools.
	mustContain("plannerWrapUpDefault", plannerWrapUpDefault, "add_intent", "prove_goal", "TodoWrite")
	mustContain("plannerTaskTimeoutDefault", plannerTaskTimeoutDefault, "prove_goal")
}

// per-run and task-timeout must mean different things (especially for the planner): per-run says "only this round
// ends", while task-timeout says "the whole task ends". The constant mapping must not change or get mixed up.
func TestWrapupDefaultsRouting(t *testing.T) {
	if WrapupDefault("worker") != settleWrapUpPrompt {
		t.Error("worker per-run default is not settleWrapUpPrompt")
	}
	if WrapupDefault("planner") != plannerWrapUpDefault {
		t.Error("planner per-run default is not plannerWrapUpDefault")
	}
	if WrapupDefault("mainagent") != mainAgentWrapUpDefault {
		t.Error("mainagent per-run default is not mainAgentWrapUpDefault")
	}
	// Unregistered keys (custom agents) fall back to generic.
	if WrapupDefault("unknown-agent") != genericWrapUpDefault {
		t.Error("unregistered key does not fall back to genericWrapUpDefault")
	}
	// task-timeout exists only for worker/planner; anything else gets an empty string (the caller falls back to per-run).
	if TaskTimeoutWrapupDefault("worker") != workerTaskTimeoutDefault {
		t.Error("worker task-timeout default is not workerTaskTimeoutDefault")
	}
	if TaskTimeoutWrapupDefault("planner") != plannerTaskTimeoutDefault {
		t.Error("planner task-timeout default is not plannerTaskTimeoutDefault")
	}
	if TaskTimeoutWrapupDefault("mainagent") != "" {
		t.Error("mainagent must have no task-timeout wording (empty string)")
	}
	// Identical per-run and task-timeout wording would erase the distinction.
	if workerTaskTimeoutDefault == settleWrapUpPrompt {
		t.Error("worker per-run and task-timeout wording are identical")
	}
	if plannerTaskTimeoutDefault == plannerWrapUpDefault {
		t.Error("planner per-run and task-timeout wording are identical")
	}
}
