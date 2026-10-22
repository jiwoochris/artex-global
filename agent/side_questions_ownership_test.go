package agent

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
)

// TestSideCaptureParentOwnershipIsExclusive verifies that the side-question
// capture parent never mixes conversation ownership with task ownership.
//
// side_question_sessions has a CHECK constraint that allows exactly two
// shapes: a conversation-owned row (conversation_id set, task/exploration/
// intent NULL) or a task-owned row (task_id + exploration_id set,
// conversation_id NULL). A run can legitimately carry BOTH a conversation id
// and a task id — a reporter attributed back to its source task keeps the
// task id while running inside the conversation it was triggered from — so
// building the parent from both made every checkpoint of such a run violate
// the constraint and get rejected. The task's checkpoints then never persisted.
//
// Ownership is exclusive: the conversation wins, and the task attribution is
// carried by the run rather than by the session.
func TestSideCaptureParentOwnershipIsExclusive(t *testing.T) {
	cases := []struct {
		name string
		ri   RunInfo
		want sidequestion.Parent
	}{
		{
			name: "conversation run keeps the conversation only",
			ri:   RunInfo{SessionID: "conv-706"},
			want: sidequestion.Parent{ConversationID: 706},
		},
		{
			name: "conversation run attributed to a task keeps the conversation only",
			ri:   RunInfo{TaskID: 2, ExplorationID: 2, SessionID: "conv-706"},
			want: sidequestion.Parent{ConversationID: 706},
		},
		{
			name: "task run keeps the task and exploration",
			ri:   RunInfo{TaskID: 3, ExplorationID: 3},
			want: sidequestion.Parent{TaskID: 3, ExplorationID: 3},
		},
		{
			name: "worker run keeps the intent",
			ri:   RunInfo{TaskID: 3, ExplorationID: 3, IntentID: 9},
			want: sidequestion.Parent{TaskID: 3, ExplorationID: 3, IntentID: 9},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured []sidequestion.Snapshot
			ctx := sidequestion.WithPublisher(t.Context(), func(s sidequestion.Snapshot) {
				captured = append(captured, s)
			})
			ctx = WithRunInfo(ctx, tc.ri)

			opts := &agentcore.Options{
				Provider: sidequestion.Bind(&sideAgentProvider{}, sidequestion.Model{Model: "fixture"}),
			}
			attachSideCapture(ctx, opts)

			// The publisher only receives snapshots once a model call runs, so
			// drive one through the attached deps.
			if opts.Deps.CallModel == nil {
				t.Fatal("attachSideCapture did not install the capture hooks")
			}
			for range opts.Deps.CallModel(ctx, llm.CompletionRequest{}) {
			}
			if len(captured) == 0 {
				t.Fatal("no side-question snapshot was captured")
			}
			parent := captured[0].Parent

			// The shape the table's CHECK constraint accepts.
			conversationOwned := parent.ConversationID > 0 &&
				parent.TaskID == 0 && parent.ExplorationID == 0 && parent.IntentID == 0
			taskOwned := parent.ConversationID == 0 &&
				parent.TaskID > 0 && parent.ExplorationID > 0
			if !conversationOwned && !taskOwned {
				t.Fatalf("parent %+v mixes conversation and task ownership; the table CHECK rejects it", parent)
			}
			if parent.ConversationID != tc.want.ConversationID ||
				parent.TaskID != tc.want.TaskID ||
				parent.ExplorationID != tc.want.ExplorationID ||
				parent.IntentID != tc.want.IntentID {
				t.Fatalf("parent = %+v, want %+v", parent, tc.want)
			}
		})
	}
}

// TestSideCaptureSkipsRunsWithoutOwnership verifies that a run with neither a
// conversation nor a task attaches no capture at all.
func TestSideCaptureSkipsRunsWithoutOwnership(t *testing.T) {
	var captured []sidequestion.Snapshot
	ctx := sidequestion.WithPublisher(t.Context(), func(s sidequestion.Snapshot) {
		captured = append(captured, s)
	})
	ctx = WithRunInfo(ctx, RunInfo{TaskID: 5}) // task without exploration

	opts := &agentcore.Options{
		Provider: sidequestion.Bind(&sideAgentProvider{}, sidequestion.Model{Model: "fixture"}),
	}
	attachSideCapture(ctx, opts)

	if opts.Deps.CallModel != nil {
		for range opts.Deps.CallModel(ctx, llm.CompletionRequest{}) {
		}
	}
	if len(captured) != 0 {
		t.Fatalf("captured %d snapshots for a run with no valid ownership", len(captured))
	}
}

var _ = context.Background
