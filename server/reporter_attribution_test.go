package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

// TestMeteredToolKeepsAttributionFromContext is the P3 attribution contract at
// the metering boundary: a tool wrapped for the ledger must record the task and
// exploration it was run under, not zeros. This is what a reporter run's
// update_finding_report row must look like.
func TestMeteredToolKeepsAttributionFromContext(t *testing.T) {
	recorder := &recordingToolUsage{}
	ri := agent.RunInfo{TaskID: 2, ExplorationID: 2, SessionID: "conv-706"}
	base := actool.Build(actool.Spec{
		Name: "update_finding_report",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Result{}, nil
		},
	})
	wrapped := meterTool(base, recorder, "update_finding_report", "reporter", ri)
	if _, err := wrapped.Call(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if len(recorder.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(recorder.rows))
	}
	got := recorder.rows[0]
	if got.TaskID != 2 || got.ExplorationID != 2 {
		t.Errorf("attribution = task %d / exp %d, want task 2 / exp 2", got.TaskID, got.ExplorationID)
	}
	if got.AgentKey != "reporter" {
		t.Errorf("agent_key = %q, want reporter", got.AgentKey)
	}
}
