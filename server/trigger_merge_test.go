package server

import (
	"strings"
	"testing"
)

// A long task goal repeated per event was the dominant bloat. These tests pin the
// fix: the task-context header (description + goal) is rendered ONCE per task, no
// matter how many same-task fires are merged.

const longGoal = "Retrieve the protected flag of challenge f2-05 and submit it via submit_flag; the ciphertext of this challenge has converged heavily, so the flag can only be derived from data embedded in the binary..." // stands in for those thousands of characters of inherited facts

func sameTaskFires(n int) []triggeredRun {
	items := make([]triggeredRun, n)
	for i := range items {
		items[i] = triggeredRun{
			agentKey: "tec_benchmark", taskID: 72, taskDesc: "f2-05 reverse engineering", taskGoal: longGoal,
			message: "[This run was triggered by a tool call]\nTool: submit_flag\nInput: {...}\nResult: {correct:false}", mergeable: true,
		}
	}
	return items
}

func TestMergeAllRunsWritesTaskGoalOnce(t *testing.T) {
	out := mergeAllRuns(sameTaskFires(39))
	if got := strings.Count(out.message, longGoal); got != 1 {
		t.Fatalf("same-task goal should appear exactly once in a merged-all run, got %d", got)
	}
	if strings.Count(out.message, "── Trigger ") < 1 || !strings.Contains(out.message, "Trigger 39") {
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
	if !strings.Contains(out.message, "2 tasks in total") {
		t.Fatalf("header should report 2 tasks: %q", out.message)
	}
	if got := strings.Count(out.message, "── Trigger "); got != 4 {
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
	if !strings.HasPrefix(msg, "[Task #72") {
		t.Fatalf("single fire should be prefixed with the task-context header: %q", msg)
	}
}

func TestTaskContextHeaderEmptyForIntervalFire(t *testing.T) {
	if h := taskContextHeader(0, "", ""); h != "" {
		t.Fatalf("interval/none trigger (no task) must produce no header, got %q", h)
	}
	// An interval fire's message must pass through untouched.
	item := triggeredRun{message: "interval trigger body"}
	if finalTriggerMessage(item) != "interval trigger body" {
		t.Fatalf("interval fire message must pass through unchanged")
	}
}

func TestTaskContextHeaderTruncatesLongGoal(t *testing.T) {
	// 很 is a 3-byte rune, so the truncated header proves the bound counts characters.
	huge := strings.Repeat("很", 5000)
	h := taskContextHeader(72, "d", huge)
	if len([]rune(h)) > 800 { // 200 desc + 500 goal + truncation markers/decoration, far below 5000
		t.Fatalf("header should be bounded even for a huge goal, got %d runes", len([]rune(h)))
	}
}

// TestTriggerSynthesisChineseFramingPreserved pins the Chinese framing the P3 trigger synthesis produces as
// a preservation target (agent brain input) — the F10/F19 boundary call. These strings are agent input
// that finalTriggerMessage → runTriggeredRun → ca.Chat pass as the user message and are dual-purpose
// (also exposed in the transcript), so under BRIEF boundary #1 (the brain is not localized — TSecBench
// benchmark behavior preservation) they must not be translated. Merge/header tests above pin
// `[Task #`·`── Trigger `·`N tasks in total`, but the task-context goal framing and by-task merge notice
// were unpinned, so this reinforces them (reverse-regression guard, paired with conversations.go).
func TestTriggerSynthesisChineseFramingPreserved(t *testing.T) {
	// The task-context header's goal framing ((goal: …)) is preserved — the input has no CJK, so only framing is checked.
	if h := taskContextHeader(7, "d", "g"); !strings.Contains(h, "(goal: ") {
		t.Fatalf("the task-context header goal framing changed (brain input must not be localized): %q", h)
	}
	// The by-task merge notice framing is preserved.
	if out := mergeTriggeredRuns(sameTaskFires(3)); !strings.Contains(out.message, "[This conversation merged") {
		t.Fatalf("the by-task merge notice framing changed (brain input must not be localized): %q", out.message)
	}
}
