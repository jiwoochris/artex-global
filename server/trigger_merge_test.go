package server

import (
	"strings"
	"testing"
)

// A long task goal repeated per event was the dominant bloat. These tests pin the
// fix: the task-context header (description + goal) is rendered ONCE per task, no
// matter how many same-task fires are merged.

const longGoal = "拿到题目 f2-05 的受保护 flag 并通过 submit_flag 提交；本题密文已高度收敛，flag 只能由二进制内嵌数据派生……" // stands in for that several-thousand-character inherited fact

func sameTaskFires(n int) []triggeredRun {
	items := make([]triggeredRun, n)
	for i := range items {
		items[i] = triggeredRun{
			agentKey: "tec_benchmark", taskID: 72, taskDesc: "f2-05 逆向", taskGoal: longGoal,
			message: "【本次由工具调用触发】\n工具: submit_flag\n入参: {...}\n返回: {correct:false}", mergeable: true,
		}
	}
	return items
}

func TestMergeAllRunsWritesTaskGoalOnce(t *testing.T) {
	out := mergeAllRuns(sameTaskFires(39))
	if got := strings.Count(out.message, longGoal); got != 1 {
		t.Fatalf("same-task goal should appear exactly once in a merged-all run, got %d", got)
	}
	if strings.Count(out.message, "── 触发 ") < 1 || !strings.Contains(out.message, "触发 39") {
		t.Fatalf("all 39 event bodies should be present: %q", out.message)
	}
	// A merged run embeds its header inline, so finalTriggerMessage must not re-add it.
	if out.taskDesc != "" || out.taskGoal != "" {
		t.Fatalf("merged run must clear taskDesc/taskGoal to avoid a duplicate header")
	}
	if finalTriggerMessage(out) != out.message {
		t.Fatalf("finalTriggerMessage must not prepend another header for a merged run")
	}
}

func TestMergeAllRunsGroupsInterleavedTasks(t *testing.T) {
	// Fires from two tasks arriving interleaved (A,B,A,B) must still carry each
	// task's context exactly once — grouping, not per-event repetition.
	mk := func(id int64, goal string) triggeredRun {
		return triggeredRun{agentKey: "a", taskID: id, taskDesc: "d", taskGoal: goal, message: "body", mergeable: true}
	}
	out := mergeAllRuns([]triggeredRun{mk(1, "GOAL_A"), mk(2, "GOAL_B"), mk(1, "GOAL_A"), mk(2, "GOAL_B")})
	if got := strings.Count(out.message, "GOAL_A"); got != 1 {
		t.Fatalf("task #1 goal should appear once despite interleaving, got %d", got)
	}
	if got := strings.Count(out.message, "GOAL_B"); got != 1 {
		t.Fatalf("task #2 goal should appear once despite interleaving, got %d", got)
	}
	if !strings.Contains(out.message, "共 2 个任务") {
		t.Fatalf("header should report 2 tasks: %q", out.message)
	}
	if got := strings.Count(out.message, "── 触发 "); got != 4 {
		t.Fatalf("all 4 event bodies should be present, got %d", got)
	}
}

func TestMergeTriggeredRunsWritesTaskGoalOnce(t *testing.T) {
	out := mergeTriggeredRuns(sameTaskFires(5))
	if got := strings.Count(out.message, longGoal); got != 1 {
		t.Fatalf("same-task goal should appear exactly once in a by-task merge, got %d", got)
	}
}

func TestFinalTriggerMessageSingleFirePrependsHeaderOnce(t *testing.T) {
	item := sameTaskFires(1)[0]
	msg := finalTriggerMessage(item)
	if got := strings.Count(msg, longGoal); got != 1 {
		t.Fatalf("single fire should carry the task goal exactly once, got %d", got)
	}
	if !strings.HasPrefix(msg, "【任务 #72") {
		t.Fatalf("single fire should be prefixed with the task-context header: %q", msg)
	}
}

func TestTaskContextHeaderEmptyForIntervalFire(t *testing.T) {
	if h := taskContextHeader(0, "", ""); h != "" {
		t.Fatalf("interval/none trigger (no task) must produce no header, got %q", h)
	}
	// An interval fire's message must pass through untouched.
	item := triggeredRun{message: "定时触发正文"}
	if finalTriggerMessage(item) != "定时触发正文" {
		t.Fatalf("interval fire message must pass through unchanged")
	}
}

func TestTaskContextHeaderTruncatesLongGoal(t *testing.T) {
	huge := strings.Repeat("很", 5000)
	h := taskContextHeader(72, "d", huge)
	if len([]rune(h)) > 800 { // 200 desc + 500 goal + truncation marker/decoration, far below 5000
		t.Fatalf("header should be bounded even for a huge goal, got %d runes", len([]rune(h)))
	}
}

// TestTriggerSynthesisChineseFramingPreserved explicitly pins the Chinese framing produced by the P3 trigger
// message synthesis as something to preserve (agent brain input) (F10/F19 boundary decision). These phrases are
// dual-use: agent input that goes in as the user message via finalTriggerMessage → runTriggeredRun → ca.Chat,
// and also shown in the transcript, so per BRIEF boundary #1 (the brain is not translated — preserving
// benchmarked behavior) they must not be translated. The merge/header behavior tests above already pin
// `【任务 #`, `── 触发 ` and `共 N 个任务`, but the goal framing of the task context header and the by-task merge
// notice were not pinned yet, so they are added here (guard against accidental Korean localization regressions;
// paired with the preservation comment in conversations.go).
func TestTriggerSynthesisChineseFramingPreserved(t *testing.T) {
	// The goal framing of the task context header (（目标：…）) is preserved — no CJK in the input, so only the framing is checked.
	if h := taskContextHeader(7, "d", "g"); !strings.Contains(h, "（目标：") {
		t.Fatalf("작업 컨텍스트 헤더의 목표 프레이밍이 바뀌었습니다(두뇌 입력 번역 금지): %q", h)
	}
	// The by-task merge notice framing is preserved.
	if out := mergeTriggeredRuns(sameTaskFires(3)); !strings.Contains(out.message, "【本会话合并了") {
		t.Fatalf("by-task 머지 안내 프레이밍이 바뀌었습니다(두뇌 입력 번역 금지): %q", out.message)
	}
}
