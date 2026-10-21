package server

import (
	"context"
	"encoding/json"
	"log"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type toolUsageRecorder interface {
	InsertToolUsage(*db.ToolUsage) error
}

// meteredTool records one row immediately before a catalog tool is invoked. It
// embeds the resolved tool so schema overrides, defaults and permission behavior
// remain unchanged.
type meteredTool struct {
	actool.CoreTool
	recorder toolUsageRecorder
	toolKey  string
	agentKey string
	ri       agent.RunInfo
}

func meterTool(t actool.CoreTool, recorder toolUsageRecorder, toolKey, agentKey string, ri agent.RunInfo) actool.CoreTool {
	if recorder == nil {
		return t
	}
	return &meteredTool{CoreTool: t, recorder: recorder, toolKey: toolKey, agentKey: agentKey, ri: ri}
}

func (m *meteredTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if err := m.recorder.InsertToolUsage(&db.ToolUsage{
		ToolKey:       m.toolKey,
		AgentKey:      m.agentKey,
		TaskID:        m.ri.TaskID,
		ExplorationID: m.ri.ExplorationID,
		IntentID:      m.ri.IntentID,
		SessionID:     m.ri.SessionID,
	}); err != nil {
		log.Printf("[toolusage] insert %s: %v", m.toolKey, err)
	}
	return m.CoreTool.Call(ctx, input, tc)
}

// sdkDefaultToolNames is the set of SDK-provided default tool names
// (actool.DefaultTools: Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash/sleep).
//
// These tools intentionally have no row in the DB tools table — the catalog owns
// only DB-managed description/schema for domain tools, and the SDK's own prompt
// guidance is code-owned. The resolver's no-row branch therefore used to skip
// them, which silently dropped their invocations from the tool_usage ledger.
//
// The set is derived from the SDK itself instead of a hard-coded list, so a new
// SDK default tool is metered as soon as the dependency adds it. Only these
// names are metered; MCP/skill/host tools keep their own ledgers and stay
// untouched.
func sdkDefaultToolNames() map[string]bool {
	names := make(map[string]bool)
	for _, t := range actool.DefaultTools() {
		names[t.Name()] = true
	}
	return names
}
