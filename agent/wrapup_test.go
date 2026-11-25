package agent

import (
	"strings"
	"testing"
	"unicode"
)

// A2: Korean localization of the wrap-up / settlement prompts.
//
// These constants are injected in the settlement phase when a run or task is terminated by hitting its step/time
// budget, and directly instruct the final summary that is shown to the user as-is. Therefore they (1) must be written
// in Korean, (2) must contain no leftover Chinese (CJK Han characters), and (3) must preserve the tool names and
// the meaning of instructions such as "one sentence of plain text".
//
// The DB seed leaves wrapup_prompt / task_timeout_wrapup_prompt as empty strings (the builtin agent INSERT in db/db.go
// doesn't fill these columns), and when empty they fall back to these constants.
// In other words, these constants are the only source of the wrap-up wording.
func TestWrapupPromptsLocalizedToKorean(t *testing.T) {
	all := map[string]string{
		"settleWrapUpPrompt":        settleWrapUpPrompt,
		"plannerWrapUpDefault":      plannerWrapUpDefault,
		"mainAgentWrapUpDefault":    mainAgentWrapUpDefault,
		"genericWrapUpDefault":      genericWrapUpDefault,
		"workerTaskTimeoutDefault":  workerTaskTimeoutDefault,
		"plannerTaskTimeoutDefault": plannerTaskTimeoutDefault,
	}

	hasScript := func(s string, table *unicode.RangeTable) bool {
		for _, r := range s {
			if unicode.Is(table, r) {
				return true
			}
		}
		return false
	}

	for name, p := range all {
		if !hasScript(p, unicode.Hangul) {
			t.Errorf("%s: 한글이 전혀 없어 한국어화되지 않았다", name)
		}
		// Tool names are ASCII and Hangul is in the Hangul block, so once translation is done there must not be a single CJK Han character.
		if hasScript(p, unicode.Han) {
			t.Errorf("%s: CJK 한자 잔재가 남아 번역이 미완이다: %q", name, p)
		}
	}

	// Tool names are identifiers, so they must not be translated and must be preserved as-is.
	// The worker family (per-run · task-timeout) keeps the instructions to write conclusions with record_fact,
	// vulnerabilities with report_finding, and finally summarize in one sentence of plain text.
	mustContain := func(name, p string, subs ...string) {
		for _, s := range subs {
			if !strings.Contains(p, s) {
				t.Errorf("%s: 지시 의미 %q 가 보존되어야 하는데 없다", name, s)
			}
		}
	}
	mustContain("settleWrapUpPrompt", settleWrapUpPrompt,
		"insert_assets", "record_fact", "report_finding", "한 문장", "순수 텍스트")
	mustContain("workerTaskTimeoutDefault", workerTaskTimeoutDefault,
		"insert_assets", "record_fact", "report_finding", "한 문장", "순수 텍스트")
	mustContain("genericWrapUpDefault", genericWrapUpDefault, "한 문장", "순수 텍스트")
	mustContain("mainAgentWrapUpDefault", mainAgentWrapUpDefault, "한 문장", "순수 텍스트")
	// The planner produces no summary sentence (it only judges and exits) and keeps the intent/goal/todo tools.
	mustContain("plannerWrapUpDefault", plannerWrapUpDefault, "add_intent", "prove_goal", "TodoWrite")
	mustContain("plannerTaskTimeoutDefault", plannerTaskTimeoutDefault, "prove_goal")
}

// per-run and task-timeout must differ in meaning (especially for the planner): per-run is "only this round
// ends" and task-timeout is "the whole task ends". The constant mapping must not get swapped or mixed up.
func TestWrapupDefaultsRouting(t *testing.T) {
	if WrapupDefault("worker") != settleWrapUpPrompt {
		t.Error("worker per-run 기본값이 settleWrapUpPrompt 가 아니다")
	}
	if WrapupDefault("planner") != plannerWrapUpDefault {
		t.Error("planner per-run 기본값이 plannerWrapUpDefault 가 아니다")
	}
	if WrapupDefault("mainagent") != mainAgentWrapUpDefault {
		t.Error("mainagent per-run 기본값이 mainAgentWrapUpDefault 가 아니다")
	}
	// Unregistered keys (custom agents) fall back to generic.
	if WrapupDefault("unknown-agent") != genericWrapUpDefault {
		t.Error("미등록 키가 genericWrapUpDefault 로 떨어지지 않는다")
	}
	// task-timeout exists only for worker/planner; for the rest it is an empty string (the caller falls back to per-run).
	if TaskTimeoutWrapupDefault("worker") != workerTaskTimeoutDefault {
		t.Error("worker task-timeout 기본값이 workerTaskTimeoutDefault 가 아니다")
	}
	if TaskTimeoutWrapupDefault("planner") != plannerTaskTimeoutDefault {
		t.Error("planner task-timeout 기본값이 plannerTaskTimeoutDefault 가 아니다")
	}
	if TaskTimeoutWrapupDefault("mainagent") != "" {
		t.Error("mainagent 은 task-timeout 문구가 없어야 한다(빈 문자열)")
	}
	// If the per-run and task-timeout wording were identical, the distinction in meaning would be lost.
	if workerTaskTimeoutDefault == settleWrapUpPrompt {
		t.Error("worker 의 per-run 과 task-timeout 문구가 동일하다")
	}
	if plannerTaskTimeoutDefault == plannerWrapUpDefault {
		t.Error("planner 의 per-run 과 task-timeout 문구가 동일하다")
	}
}
