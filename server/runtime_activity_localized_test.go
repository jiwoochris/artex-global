package server

import (
	"fmt"
	"strings"
	"testing"
)

// Regression guard that keeps the "display-only" runtime activity summary constants in engine.go,
// engine_timeout.go and goals.go in Korean. These summaries appear in the dashboard activity transcript,
// but they carry no node_id (they are task-level), so they are never read back into any agent context —
// the feedback paths (planner.workerOutput, the get_worker_output tool) only pick 'result'/'text'
// activities within an intent's scope (node_id). So localizing them does not touch brain input (BRIEF
// boundary #1). Conversely, summaries that are fed back into the brain (the seed intent summary
// `完成任务目标…`, the default description `未命名任务`, etc.) keep the original text and are not covered
// by this test. The Korean check reuses assertKoreanError (Hangul present, zero Chinese Han characters,
// intercept_archive_localized_test.go). [[G132]]
func TestRuntimeActivitySummariesLocalized(t *testing.T) {
	// Fixed summaries without format arguments: must be Korean as-is.
	plain := map[string]string{
		"goalless_task_done": goallessTaskDoneSummary,
		"goal_breakdown_r0":  goalBreakdownRound0Summary,
		"queued_no_llm":      queuedNoLLMSummary,
		"queued_fifo":        queuedFIFOSummary,
	}
	for label, msg := range plain {
		assertKoreanError(t, label, msg)
	}

	// %d format strings: check that the format argument is still there, that the formatted result is also
	// Korean, and that the number is actually reflected.
	fmtCases := map[string]string{
		"planner_round":       plannerRoundSummaryFmt,
		"timeout_final_round": timeoutFinalRoundSummaryFmt,
		"queued_conc_limit":   queuedConcurrencyLimitSummaryFmt,
	}
	for label, f := range fmtCases {
		if !strings.Contains(f, "%d") {
			t.Fatalf("%s: 포맷 인자 %%d 가 사라졌습니다: %q", label, f)
		}
		got := fmt.Sprintf(f, 7)
		assertKoreanError(t, label, got)
		if !strings.Contains(got, "7") {
			t.Fatalf("%s: 포맷 인자가 결과에 반영되지 않았습니다: %q", label, got)
		}
	}
}
