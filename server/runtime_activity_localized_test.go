package server

import (
	"fmt"
	"strings"
	"testing"
)

// Regression guard keeping the "display-only" runtime activity summary constants of
// engine.go·engine_timeout.go·goals.go as their exact English source text. These summaries show up in the dashboard transcript
// (activity transcript) but carry no node_id (they are task-scoped), so they are never read back into any
// agent context — the feedback path (planner.workerOutput·get_worker_output tool) picks only 'result'/
// 'text' activities within intent scope (node_id). So translating these summaries does not touch
// the brain input (BRIEF boundary #1). Conversely the summaries fed back to the brain (the seed intent summary `Complete the task goal…`·the default description `unnamed task`
// etc.) keep the original text, so they are not covered by this test. Assertions use
// assertEnglishMessage (intercept_archive_localized_test.go). [[G132]]
func TestRuntimeActivitySummariesLocalized(t *testing.T) {
	// Fixed summaries without format arguments: they must be the English source text as-is.
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"goalless_task_done", goallessTaskDoneSummary,
			"All goals achieved and the intents launched directly finished, so the task is being closed"},
		{"goal_breakdown_r0", goalBreakdownRound0Summary, "Goal breakdown round 0"},
		{"queued_no_llm", queuedNoLLMSummary,
			"Queued: no runnable LLM configuration right now, so it starts automatically once one is restored"},
		{"queued_fifo", queuedFIFOSummary,
			"Queued: earlier work is still waiting, so it starts automatically in FIFO order"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}

	// %d format strings: check that the format argument is still there, that the formatted result
	// is the English source text and that the number is really reflected.
	fmtCases := []struct{ label, f, wantEnglish string }{
		{"planner_round", plannerRoundSummaryFmt, "Planning round 7"},
		{"timeout_final_round", timeoutFinalRoundSummaryFmt,
			"Task timed out — final wrap-up and judgment (round 7)"},
		{"queued_conc_limit", queuedConcurrencyLimitSummaryFmt,
			"Queued: the concurrency limit of 7 is reached, so it starts automatically as soon as a slot frees up"},
	}
	for _, c := range fmtCases {
		if !strings.Contains(c.f, "%d") {
			t.Fatalf("%s: the %%d format argument disappeared: %q", c.label, c.f)
		}
		got := fmt.Sprintf(c.f, 7)
		assertEnglishMessage(t, c.label, got, c.wantEnglish)
		if !strings.Contains(got, "7") {
			t.Fatalf("%s: the format argument is not reflected in the result: %q", c.label, got)
		}
	}
}
