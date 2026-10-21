package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatstate"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/transcript"
)

// TestChatRunAttributionReachesBothLedgers is the P3 guard for the TOKEN ledger.
//
// tool_usage attribution is not sufficient evidence that a reporter run is
// attributed: llm_usage is metered by a separate path (llmrec) that derives
// task/worker from the transcript session id via parseSession, and parseSession
// only understands "exp<N>-<lane>". A chat run's session is "conv-N", so before
// this fix every reporter LLM call was metered with task_id = NULL and
// worker = NULL even though its tool_usage rows were correctly attributed.
//
// The regression this pins: the "conv-N" session id the chat agent installs
// downstream must NOT clobber the attribution, since llmrec reads task/worker
// per call from these explicit context values.
func TestChatRunAttributionReachesBothLedgers(t *testing.T) {
	t.Run("reporter run carries the source task into both ledgers", func(t *testing.T) {
		run := chatstate.Run{SourceTaskID: 7, SourceExplorationID: 9}
		ctx := chatRunAttribution(context.Background(), run)

		ri := agent.RunInfoFrom(ctx)
		if ri.TaskID != 7 || ri.ExplorationID != 9 {
			t.Errorf("tool_usage path: RunInfo = task %d / exp %d, want task 7 / exp 9", ri.TaskID, ri.ExplorationID)
		}
		if got := llmrec.TaskIDFrom(ctx); got != "7" {
			t.Errorf("llm_usage path: task id = %q, want 7", got)
		}

		// The chat agent installs its own session id further down the chain.
		// parseSession cannot decode "conv-N", so this is exactly the case that
		// used to leave the token ledger unattributed.
		installed := transcript.WithSessionID(ctx, "conv-1")
		if got := llmrec.TaskIDFrom(installed); got != "7" {
			t.Errorf("task id after the chat session id is installed = %q, want 7 — "+
				"the conv-N session must not clobber the attribution", got)
		}
	})

	t.Run("plain user chat stays unattributed", func(t *testing.T) {
		ctx := chatRunAttribution(context.Background(), chatstate.Run{})
		if got := llmrec.TaskIDFrom(ctx); got != "" {
			t.Errorf("plain chat picked up task id %q, want none", got)
		}
		if ri := agent.RunInfoFrom(ctx); ri.TaskID != 0 || ri.ExplorationID != 0 {
			t.Errorf("plain chat picked up RunInfo %+v, want zero", ri)
		}
	})

	t.Run("a task without an exploration is not half-attributed", func(t *testing.T) {
		ctx := chatRunAttribution(context.Background(), chatstate.Run{SourceTaskID: 7})
		if got := llmrec.TaskIDFrom(ctx); got != "" {
			t.Errorf("half attribution reached the token ledger: task id = %q, want none", got)
		}
		if ri := agent.RunInfoFrom(ctx); ri.TaskID != 0 {
			t.Errorf("half attribution reached tool_usage: RunInfo = %+v, want zero", ri)
		}
	})
}
