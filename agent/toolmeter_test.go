package agent

import (
	"context"
	"encoding/json"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// countingTool is a no-op tool used to prove MeterTools hands back a usable tool
// without touching behavior: Name/Description/Call must delegate unchanged.
func countingTool(name string, calls *int) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        name,
		Description: "counting stub",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			*calls++
			return actool.Result{}, nil
		},
	})
}

// TestMeterToolsPassesThroughWhenUnwired keeps agent usable outside the server: with no
// ToolMeter wired the list must come back untouched, not nil or reordered.
func TestMeterToolsPassesThroughWhenUnwired(t *testing.T) {
	old := ToolMeter
	ToolMeter = nil
	t.Cleanup(func() { ToolMeter = old })

	calls := 0
	in := []actool.CoreTool{countingTool("a", &calls), countingTool("b", &calls)}
	out := MeterTools(in, "goals", RunInfo{TaskID: 1, ExplorationID: 2})
	if len(out) != len(in) {
		t.Fatalf("len=%d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Name() != in[i].Name() {
			t.Errorf("index %d: %s, want %s", i, out[i].Name(), in[i].Name())
		}
	}
	// An empty list must not become nil-vs-empty surprising callers either way.
	if got := MeterTools(nil, "goals", RunInfo{}); len(got) != 0 {
		t.Errorf("nil input: len=%d, want 0", len(got))
	}
}

// TestMeterToolsReceivesAgentKeyAndRunInfo pins the hook's contract: goals.go passes the
// "goals" agent key and the task/exploration the decomposed goals belong to, and the
// ledger rows must be attributable. If MeterTools dropped either, every goals-tool row
// would come back unattributed — the same failure P2 is fixing.
func TestMeterToolsReceivesAgentKeyAndRunInfo(t *testing.T) {
	old := ToolMeter
	t.Cleanup(func() { ToolMeter = old })

	var gotKey string
	var gotRI RunInfo
	ToolMeter = func(_ []actool.CoreTool, agentKey string, ri RunInfo) []actool.CoreTool {
		gotKey, gotRI = agentKey, ri
		return nil
	}

	want := RunInfo{TaskID: 4242, ExplorationID: 77}
	MeterTools(nil, "goals", want)
	if gotKey != "goals" {
		t.Errorf("agentKey = %q, want %q", gotKey, "goals")
	}
	if gotRI != want {
		t.Errorf("RunInfo = %+v, want %+v", gotRI, want)
	}
}
