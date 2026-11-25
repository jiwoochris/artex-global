package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// Wrap-up prompt (wrap-up / settlement prompt): when an agent is terminated because of [step exhaustion (MaxTurns)] or
// [timeout (run_seconds/MaxDuration)], the SDK's settlement phase injects this prompt
// so the agent first persists what it has identified but not yet written back, then outputs a one-sentence summary, avoiding unfinished work.
//
// Each agent's wrap-up prompt can be overridden in the admin backend as needed (stored in agents.wrapup_prompt); if empty, the built-in
// default here is used. Only the [prompt body] is editable; which tools are disabled and how many rounds the wrap-up itself gets are fixed code policy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default wrap-up prompts, indexed by agent key. worker reuses the historically hard-coded settleWrapUpPrompt
// (defined in worker.go); planner/mainagent each have their own version; keys with no match (custom agents) use the generic fallback.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: the built-in default round budget for each agent's wrap-up phase [itself] (can be overridden with >0 in the admin backend).
// All are 10 rounds, ensuring the wrap-up phase has enough steps to persist to the DB. Keys with no match use genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "이번 계획 라운드의 단계 예산이 곧 소진됩니다. 다만 【이번 라운드】만 끝나는 것이며, 시스템은 이후에도 상황 변화에 따라 당신을 다시 깨워 계획을 이어가게 합니다. 작업 자체가 끝나는 것이 아니므로 여기서 전체 계획을 마무리 지을 필요는 없습니다. 이번 라운드에서 이미 명확히 판단한 결론은 실제로 반영하여 이번 라운드가 헛되지 않게 하되, 【마무리를 위해 억지로 의도를 지어내지는】 마십시오(이번 라운드에 의도가 0개인 것도 완전히 정상적인 결과입니다). (1) 【지금 바로 파견해야 할】 탐색 방향을 이미 판단했다면 add_intent 한 번으로 묶어서 제출합니다(이미 정한 것은 묵히지 말고 바로 보냅니다). (2) 어떤 발견이나 사실로 이미 달성이 증명된 목표는 prove_goal 로 met 표시합니다(빠뜨리지 마십시오). (3) 단계를 나눠야 하는 직렬 익스플로잇 체인을 식별했다면 TodoWrite 로 기록하여 다음에 깨어났을 때 이어서 파견할 수 있게 합니다. 다 끝냈으면 이번 라운드를 바로 종료하며, 요약 텍스트는 출력하지 않습니다."

const mainAgentWrapUpDefault = "단계 예산이 곧 소진되어 이번 상호작용이 끝나려 합니다. 더 이상 새로운 탐색이나 조작을 시작하지 마십시오. **한 문장짜리 순수 텍스트로만** 현재 진행 상황, 핵심 결론, 그리고 권장하는 다음 단계를 사용자에게 한국어로 요약하십시오."

const genericWrapUpDefault = "예산 소진으로 곧 종료됩니다. 먼저 완료했지만 아직 저장하지 않은 결과를 기록한 뒤, **한 문장짜리 순수 텍스트로만** 무엇을 했고 어떤 핵심 결론을 얻었는지 한국어로 요약하십시오(이 한 문장이 이번 실행의 결과로 표시됩니다)."

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

// withWrapupLang appends the output-language directive (langDirective) after the wrap-up prompt.
// Unlike the system prompt, the wrap-up prompt doesn't get the langDirective tail attached automatically,
// so it has to be attached directly here for the final one-sentence summary shown to the user to follow the selected display language.
// The wrap-up prose itself is an operating instruction read by the model (kept in Korean), and this tail, at top priority,
// forces only the language of user-facing text. It is harmless for the planner family, which produces no summary.
// With the default language ko, no tail is attached. The system prompt's langDirective already
// forces Korean output, so the default path's model input stays the same as in previous versions (to prevent benchmark drift).
func withWrapupLang(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return prompt
	}
	lang := resolveOutputLang()
	if lang == "ko" {
		return prompt
	}
	return prompt + langDirective(lang)
}

// ---------- Task-level timeout wrap-up texts (see docs/任务级超时与收尾设计.md) ----------
//
// These are a SEPARATE SET from the per-run wrap-up texts: per-run means "the budget for this one run of yours is used up"; task timeout means
// "the whole task's time is up and it is about to end". The semantics are often opposite (especially for the planner: per-run says "don't stop, keep planning";
// task timeout says "time's up, stop planning and make the final judgement"). Configured only for worker/planner.

// WrapupTaskTimeoutOverride / …TurnsOverride: DB overrides of the task-timeout wrap-up text and its round count
// (wired to agents.task_timeout_wrapup_prompt / _max_turns; worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**전체 작업이 시간 제한 상한에 도달하여 곧 종료됩니다**(이번 run 의 예산이 아니라 탐색 전체가 끝나는 시점입니다). 이것이 마지막 기회입니다. (1) 이미 식별했지만 아직 기록하지 않은 내용을 【전부】 저장합니다. 새 자산은 insert_assets, 탐색 결론과 사실은 record_fact, 확인된 취약점은 report_finding 으로 기록합니다. (2) 더 이상 어떤 새 명령이나 탐지도 시작하지 마십시오. (3) **맨 마지막에 한 문장짜리 순수 텍스트로만** 이번 의도에서 얻은 핵심 결론을 한국어로 요약하십시오."

const plannerTaskTimeoutDefault = "**전체 작업이 시간 제한 상한에 도달하여 곧 종료됩니다**(이번 라운드가 아니라 작업 전체가 종료됩니다). 현재까지의 【모든】 사실과 발견을 근거로 마지막 목표 판정을 수행하십시오. 증거로 달성이 증명된 목표는 prove_goal 로 met 표시합니다(빠뜨리지 마십시오). **더 이상 어떤 새 의도도 생성하지 마십시오**(지금 의도를 파견해도 더 이상 실행되지 않습니다). 판정을 마치면 바로 종료하며, 요약 텍스트는 출력하지 않습니다."

// TaskTimeoutWrapupDefault returns an agent's built-in default task-timeout wrap-up text (for admin-backend placeholders / restoring the default).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // not configured (mainagent/chat) returns an empty string
}

// resolveTaskTimeoutWrapup: DB override (non-empty) > built-in default. An empty string means the agent has no task-timeout text
// (not worker/planner); in that case the caller should fall back to the per-run text.
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
	return resolveWrapupTurns(agentKey) // defaults to the per-run round count
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → this run was clamped by the task deadline: wrap-up due to Timeout = the task's time is up → the task-timeout text;
//     wrap-up due to MaxTurns = the steps within the clamped window ran out first while the task still has minutes left → fall back to the per-run text.
//   - clamped=false → the task still has plenty of time: both reasons use the per-run text (i.e. it degenerates to wrapupSettlement).
//
// This is handed to the harness's PromptByReason, which picks on the spot at wrap-up time according to the ACTUAL reason, so there is no build-time mismatch.
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
				harness.ReasonTimeout:  withWrapupLang(tt), // the task's time is up
				harness.ReasonMaxTurns: perRun,             // steps ran out first while the task still has time left
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
