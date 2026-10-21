package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

// TestWireToolsInstallsToolMeterHook checks the P2 wiring itself. Goal decomposition
// builds set_goals/set_constraints directly and never passes through ToolResolve, so
// without this hook their invocations are invisible to the usage ledger.
func TestWireToolsInstallsToolMeterHook(t *testing.T) {
	pg := openTestDB(t)
	wireTools(pg, nil)
	t.Cleanup(func() {
		agent.ToolResolve = nil
		agent.ToolMeter = nil
	})

	if agent.ToolMeter == nil {
		t.Fatal("agent.ToolMeter not wired by wireTools (P2: goals tools would be unmetered)")
	}

	const key = "set_goals"
	t.Cleanup(func() { dropUsage(t, pg, key) })
	dropUsage(t, pg, key)

	ri := agent.RunInfo{TaskID: 4242, ExplorationID: 77}
	base := []actool.CoreTool{hostStub(key)}
	metered := agent.MeterTools(base, "goals", ri)
	if len(metered) != 1 {
		t.Fatalf("len=%d, want 1", len(metered))
	}
	if _, isMetered := metered[0].(*meteredTool); !isMetered {
		t.Fatalf("%s is %T, want *meteredTool", key, metered[0])
	}
	if _, err := metered[0].Call(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pg.QueryRow(`SELECT COUNT(*) FROM tool_usage WHERE tool_key=$1`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%s: 1 call produced %d usage rows, want 1", key, n)
	}
	// Attribution must carry the goals agent key and the task/exploration.
	var agentKey string
	var taskID, explorationID int64
	if err := pg.QueryRow(`SELECT agent_key, task_id, exploration_id
FROM tool_usage WHERE tool_key=$1 ORDER BY id DESC LIMIT 1`, key).
		Scan(&agentKey, &taskID, &explorationID); err != nil {
		t.Fatal(err)
	}
	if agentKey != "goals" {
		t.Errorf("agent_key = %q, want %q", agentKey, "goals")
	}
	if taskID != ri.TaskID || explorationID != ri.ExplorationID {
		t.Errorf("attribution task=%d exp=%d, want task=%d exp=%d",
			taskID, explorationID, ri.TaskID, ri.ExplorationID)
	}
}
