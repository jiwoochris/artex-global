package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "The user paused the task",
		"The user paused the task through the task control API (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was cancelled deliberately; in-flight intents go back to the frontier (open) and are claimed again from scratch after the task resumes")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "The orchestrator agent paused the task",
		"The orchestrator agent called the pause_task tool to pause this task. This Planner/Worker run was cancelled deliberately; in-flight intents go back to the frontier (open) and re-run after resume")
	AbortTaskDeleted = cause("task_deleted", "The task was deleted",
		"The task is being deleted (DELETE /api/tasks/{id}); the delete barrier cancelled the Planner, Worker and main Agent running for this task; the results of this run will not be used again")
	AbortPausedOnReload = cause("paused_on_reload", "The backend restored the task's paused state",
		"On startup the backend restored the task pause from the state persisted in the database. This run was cancelled; normally no Agent is running during restoration")
	AbortGoalMet = cause("goal_met", "The planner determined the task goal was met",
		"The planner determined the task goal was met and set the task to done, then cancelled the Workers still running; those intents are marked stopped rather than failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "The drain wait while settling a timed-out task is exhausted",
		"After the task reached timeout the engine waited for the running Worker to settle gracefully, but the 90-second drain grace was still not enough, so a hard cancel was issued; intents are marked exhausted, and facts and assets already written during settlement are kept")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "The planner terminated this intent",
		"The planner called kill_work to terminate this intent deliberately, usually meaning the direction was off track or no longer worth pursuing; the intent is marked stopped and is not claimed again automatically")
	AbortWorkPausedByUser = cause("work_paused_by_user", "The user paused this Worker intent",
		"The user paused the running Worker. This call was cancelled and the intent moved to paused; intents, facts, findings and activity records already registered are all kept, and execution restarts from scratch after resume")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "The user deleted this Worker intent",
		"The user deleted the running Worker. This call was cancelled; after the Worker leaves the write region the server handles the intent with the deletion mode the user chose — a fake delete only marks it deleted and keeps all outputs, a real delete cascades to remove the intent and the downstream nodes supported only by it")
	AbortWorkFinished = cause("work_finished", "The Worker finished normally and released its context",
		"The Worker finished normally and the engine released its context resources in detachWork. This is not a run interruption; if it appears in an interruption message, the cancellation and the settlement event raced")
	AbortPausedRaceGuard = cause("paused_race_guard", "Refusing to start a new run while the task is paused",
		"While the task is paused the engine refuses to hand out a new execution context, preventing a race between claim and pause from starting a Worker anyway; intents already claimed go back to the frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "The user stopped this conversation turn",
		"The user clicked stop and deliberately aborted this run of the main Agent or conversation Agent. Activity records already produced are kept, and the next message can still be sent")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "The task was paused, aborting the main Agent conversation",
		"When the user paused the task, the running main Agent conversation was cancelled as well. Activity records already produced are kept; the message of this turn is not replayed automatically after the task resumes")
	AbortChatTurnFinished = cause("chat_turn_finished", "This conversation turn finished normally and released its context",
		"This conversation turn finished normally and the server is releasing the context resources of that turn. This is not a run interruption; if it appears in an interruption message, the cancellation and the settlement event raced")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "The backend process is shutting down",
		"The backend process received SIGINT or SIGTERM and is restarting, updating or shutting down. All running Agents are cancelled; intents left in running are reset to open after the restart and executed again")
	AbortRunHardTimeout = cause("run_hard_timeout", "The hard timeout backstop for a single run has fired",
		"A single run exceeded the soft wall-clock budget plus the extra grace, meaning a model request or a tool did not return for a long time, so normal end-of-turn settlement could not run. Check the last tool call that had not returned before the interruption first")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "upstream context reached its deadline",
			"upstream context reached its deadline, but the setter attached no named cause via WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "canceller attached no named cause",
			"upstream context was cancelled, but the canceller attached no named cause via context.WithCancelCause; register a cause in agent/cancelcause.go and wire it into that cancellation point", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
