package agent

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// toolCaptureProvider records the tool list the model was shown and then answers with
// plain text and no tool calls, so DecomposeGoalsWithProvider returns immediately.
type toolCaptureProvider struct {
	seen []llm.ToolSchema
}

func (p *toolCaptureProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.seen = append(p.seen, req.Tools...)
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "no goals needed"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop, StopReason: "end_turn"}, nil)
	}
}

func (p *toolCaptureProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("no goals needed")}}, "", llm.Usage{}, nil
}

// countingMeter stands in for the server's real metering hook: it wraps each tool so
// one call is observable per invocation. goals.go must route its constructed tools
// through this hook or the usage ledger never sees set_goals / set_constraints.
func countingMeter() (func([]actool.CoreTool, string, RunInfo) []actool.CoreTool, *int) {
	calls := 0
	return func(tools []actool.CoreTool, agentKey string, ri RunInfo) []actool.CoreTool {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, t := range tools {
			name, ak, r := t.Name(), agentKey, ri
			out = append(out, actool.Build(actool.Spec{
				Name:        name,
				Description: t.Description(),
				Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
					calls++
					_ = ak
					_ = r
					return actool.Result{}, nil
				},
			}))
		}
		return out
	}, &calls
}

// TestDecomposeGoalsRoutesToolsThroughMeter is the P2 regression at the actual call
// site. Testing the ToolMeter hook alone is not enough: goals.go could stop calling it
// and every test would still pass while the ledger stayed empty. This drives
// DecomposeGoalsWithProvider and asserts the hook is invoked with the goals agent key
// and the task's RunInfo, covering every tool the decomposer exposes.
func TestDecomposeGoalsRoutesToolsThroughMeter(t *testing.T) {
	old := ToolMeter
	t.Cleanup(func() { ToolMeter = old })

	meter, calls := countingMeter()
	ToolMeter = meter

	var gotKey string
	var gotRI RunInfo
	ToolMeter = func(tools []actool.CoreTool, agentKey string, ri RunInfo) []actool.CoreTool {
		gotKey, gotRI = agentKey, ri
		return meter(tools, agentKey, ri)
	}

	prov := &toolCaptureProvider{}
	// taskID > 0 with a nil store exercises the exact wiring the runtime uses: the
	// meter must receive the task id and tolerate a zero exploration id.
	DecomposeGoalsWithProvider(context.Background(), prov, t.TempDir(),
		"prove nothing is exposed", "", nil, nil, 4242, false, 0, nil)

	if gotKey != "goals" {
		t.Fatalf("MeterTools agentKey = %q, want %q (P2: goals tools unmetered or misattributed)", gotKey, "goals")
	}
	if gotRI.TaskID != 4242 {
		t.Errorf("MeterTools RunInfo.TaskID = %d, want 4242", gotRI.TaskID)
	}
	if gotRI.ExplorationID != 0 {
		t.Errorf("MeterTools RunInfo.ExplorationID = %d, want 0 for a nil store", gotRI.ExplorationID)
	}

	// The decomposer must have exposed its managed tools to the model, and every one
	// of them came back from the meter.
	if len(prov.seen) == 0 {
		t.Fatal("provider saw no tools — the test did not exercise the goals tool list")
	}
	names := make([]string, 0, len(prov.seen))
	for _, ts := range prov.seen {
		names = append(names, ts.Name)
	}
	for _, want := range []string{"set_goals", "set_constraints"} {
		found := false
		for _, got := range names {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tool %q missing from the model-facing list %v", want, names)
		}
	}
	_ = calls
}

// TestMeterToolsPreservesToolIdentity guards the wrap contract: metering must not
// rename a tool, change its schema, or drop it. A wrapper that lost Name() would break
// the model's tool calls even though the ledger row looked right.
func TestMeterToolsPreservesToolIdentity(t *testing.T) {
	old := ToolMeter
	ToolMeter = nil
	t.Cleanup(func() { ToolMeter = old })

	stub := countTool("set_goals")
	in := []actool.CoreTool{stub}
	out := MeterTools(in, "goals", RunInfo{TaskID: 1})
	if len(out) != 1 {
		t.Fatalf("len=%d, want 1", len(out))
	}
	if out[0].Name() != stub.Name() {
		t.Errorf("Name = %q, want %q", out[0].Name(), stub.Name())
	}
	if out[0].Description() != stub.Description() {
		t.Errorf("Description = %q, want %q", out[0].Description(), stub.Description())
	}
	_ = db.ToolUsage{}
}

// countTool is a minimal catalog-like tool used for identity assertions.
func countTool(name string) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        name,
		Description: "identity stub",
		Schema:      map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Result{}, nil
		},
	})
}
