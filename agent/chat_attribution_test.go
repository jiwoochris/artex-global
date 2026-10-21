package agent

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// chatProbeProvider answers with plain text and no tool calls, so a Chat run
// ends after one turn.
type chatProbeProvider struct{}

func (chatProbeProvider) Stream(_ context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "ok"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop, StopReason: "end_turn"}, nil)
	}
}

func (chatProbeProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("ok")}}, "", llm.Usage{}, nil
}

// TestChatCarriesSourceAttributionIntoToolAssembly drives the real Chat entry
// point and inspects the RunInfo its tool assembly sees. This is the P3
// regression: Chat used to build a fresh RunInfo{SessionID}, so a reporter run
// triggered by a task finding produced tool_usage rows with task_id and
// exploration_id = NULL.
//
// ToolResolve is the observation point: it is called with the very RunInfo the
// assembled tools are metered with (the server's meterTool reads it from there).
func TestChatCarriesSourceAttributionIntoToolAssembly(t *testing.T) {
	oldResolve := ToolResolve
	t.Cleanup(func() { ToolResolve = oldResolve })

	var seen RunInfo
	ToolResolve = func(ctx context.Context, _ string, tools []actool.CoreTool) []actool.CoreTool {
		seen = RunInfoFrom(ctx)
		return tools
	}

	ca := NewChatAgent(chatProbeProvider{}, "fixture-model", t.TempDir(), nil, 0)

	// The attribution the dispatcher attached for the reporter run.
	attributed := RunInfo{TaskID: 42, ExplorationID: 7}
	ctx := WithRunInfo(context.Background(), attributed)

	if _, err := ca.Chat(ctx, "reporter", "conv-706", "write the report", 2, 0, false, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if seen.TaskID != 42 {
		t.Errorf("assembled tools saw TaskID=%d, want 42 (P3: attribution dropped by Chat)", seen.TaskID)
	}
	if seen.ExplorationID != 7 {
		t.Errorf("assembled tools saw ExplorationID=%d, want 7 (P3: attribution dropped by Chat)", seen.ExplorationID)
	}
	if seen.SessionID != "conv-706" {
		t.Errorf("assembled tools saw SessionID=%q, want conv-706", seen.SessionID)
	}
}

// TestChatWithoutAttributionStaysUnattributed guards the other direction: a
// plain user chat has no source task and must not inherit one.
func TestChatWithoutAttributionStaysUnattributed(t *testing.T) {
	oldResolve := ToolResolve
	t.Cleanup(func() { ToolResolve = oldResolve })

	var seen RunInfo
	ToolResolve = func(ctx context.Context, _ string, tools []actool.CoreTool) []actool.CoreTool {
		seen = RunInfoFrom(ctx)
		return tools
	}

	ca := NewChatAgent(chatProbeProvider{}, "fixture-model", t.TempDir(), nil, 0)
	if _, err := ca.Chat(context.Background(), "reporter", "conv-707", "hello", 2, 0, false, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if seen.TaskID != 0 || seen.ExplorationID != 0 {
		t.Errorf("plain chat picked up attribution: %+v", seen)
	}
	if seen.SessionID != "conv-707" {
		t.Errorf("SessionID = %q, want conv-707", seen.SessionID)
	}
}

var _ = json.RawMessage{}
