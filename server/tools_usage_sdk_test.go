package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/dbtest"
	actool "github.com/Autumn-27/norma/tool"
)

// sdkDefaultTools is the exact set of names actool.DefaultTools() returns. Each one
// is also a real SDK tool, so the resolver test only asserts on the wrapper type for
// these (calling them needs a live ToolContext — see TestToolResolveMetersByName).
func sdkDefaultTools() []string {
	var names []string
	for _, tool := range actool.DefaultTools() {
		names = append(names, tool.Name())
	}
	return names
}

// sdkDefaultToolInstances returns actool.DefaultTools() as the concrete SDK tools.
func sdkDefaultToolInstances() []actool.CoreTool {
	return actool.DefaultTools()
}

// hostStub is a stand-in for an MCP/host/skill tool: no row in the tools table, so
// it must ride the same no-row branch as the SDK defaults but stay UNwrapped.
func hostStub(name string) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        name,
		Description: "host tool stub",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Result{}, nil
		},
	})
}

// openTestDB opens the throwaway ARTEX_TEST_DSN database. Closing is registered as
// the FIRST t.Cleanup: cleanups run in reverse order, so the row cleanups a test
// registers later still see an open connection.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	pg, err := db.Open(dbtest.DSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

// dropUsage removes rows these tests write, keeping the shared test DB clean.
func dropUsage(t *testing.T, pg *db.DB, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, err := pg.Exec(`DELETE FROM tool_usage WHERE tool_key=$1`, key); err != nil {
			t.Fatalf("drop usage %s: %v", key, err)
		}
	}
}

// lastUsage reads the most recent ledger row for key and asserts its attribution.
func lastUsage(t *testing.T, pg *db.DB, key, agentKey string, ri agent.RunInfo) {
	t.Helper()
	var taskID, explorationID, intentID int64
	var sessionID *string
	row := pg.QueryRow(`
SELECT task_id, exploration_id, intent_id, session_id
FROM tool_usage
WHERE tool_key=$1
ORDER BY id DESC
LIMIT 1`, key)
	if err := row.Scan(&taskID, &explorationID, &intentID, &sessionID); err != nil {
		t.Fatalf("read usage row for %s: %v", key, err)
	}
	wantTask, wantExp, wantIntent := ri.TaskID, ri.ExplorationID, ri.IntentID
	if taskID != wantTask || explorationID != wantExp || intentID != wantIntent {
		t.Errorf("%s attribution: task=%d exp=%d intent=%d, want task=%d exp=%d intent=%d",
			key, taskID, explorationID, intentID, wantTask, wantExp, wantIntent)
	}
	if (sessionID == nil) != (ri.SessionID == "") {
		t.Errorf("%s session_id: got %v, want %q", key, sessionID, ri.SessionID)
	}
	if sessionID != nil && *sessionID != ri.SessionID {
		t.Errorf("%s session_id: got %q, want %q", key, *sessionID, ri.SessionID)
	}
}

func byName(tools []actool.CoreTool) map[string]actool.CoreTool {
	out := make(map[string]actool.CoreTool, len(tools))
	for _, tool := range tools {
		out[tool.Name()] = tool
	}
	return out
}

// TestSdkDefaultToolNamesTracksTheSDK pins the allowlist contract: the set is derived
// from actool.DefaultTools() rather than hard-coded, so it must equal exactly the SDK's
// own default set. If a future SDK release adds or drops a tool, this test says so
// instead of the ledger silently going stale.
func TestSdkDefaultToolNamesTracksTheSDK(t *testing.T) {
	got := sdkDefaultToolNames()
	want := map[string]bool{}
	for _, name := range sdkDefaultTools() {
		want[name] = true
	}
	if len(got) != len(want) {
		t.Fatalf("sdkDefaultToolNames has %d names, actool.DefaultTools has %d", len(got), len(want))
	}
	for name := range want {
		if !got[name] {
			t.Errorf("sdkDefaultToolNames missing SDK default %q", name)
		}
	}
	// The P1 symptom specifically: without Bash the ledger keeps dropping every
	// worker shell call, which is exactly what the old E2E showed (Bash=0).
	if !got["Bash"] {
		t.Error(`sdkDefaultToolNames missing "Bash" — the P1 regression returns`)
	}
	// Catalog tools must not be swept in by the no-row branch.
	if got["record_fact"] {
		t.Error("sdkDefaultToolNames must not contain catalog tools (record_fact)")
	}
}

// TestToolResolveWrapsSDKDefaultsAndNotHosts is the P1 regression. SDK default tools
// have no row in the tools table on purpose, and the no-row branch used to pass them
// through untouched — silently dropping every Bash call from tool_usage. The resolver
// must wrap exactly those by name and leave MCP/host/skill tools (same branch, own
// ledgers) untouched.
//
// This asserts on the wrapper type only; invoking the real SDK tools needs a live
// ToolContext, so the ledger write itself is covered by TestToolResolveMetersByName.
func TestToolResolveWrapsSDKDefaultsAndNotHosts(t *testing.T) {
	pg := openTestDB(t)
	wireTools(pg, nil) // nil domainReg: only the filter/decorate/meter path matters
	t.Cleanup(func() { agent.ToolResolve = nil })
	t.Setenv("AGENT_CORE_DISABLE_INTERACTIVE_SHELL", "1")

	base := append(sdkDefaultToolInstances(),
		hostStub("mcp__demo__search"),
		hostStub("traffic_Search"),
	)
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: 42, ExplorationID: 8, IntentID: 9, SessionID: "s-1"})

	resolved := byName(agent.ToolResolve(ctx, "worker", base))
	for _, name := range sdkDefaultTools() {
		got, ok := resolved[name]
		if !ok {
			t.Fatalf("%s dropped by resolver", name)
		}
		if _, isMetered := got.(*meteredTool); !isMetered {
			t.Errorf("%s is %T, want *meteredTool (P1: SDK defaults were never metered)", name, got)
		}
	}
	// MCP/host tools ride the same no-row branch but keep their own ledgers.
	for _, name := range []string{"mcp__demo__search", "traffic_Search"} {
		got, ok := resolved[name]
		if !ok {
			t.Fatalf("%s dropped by resolver", name)
		}
		if _, isMetered := got.(*meteredTool); isMetered {
			t.Errorf("%s is *meteredTool, want untouched pass-through", name)
		}
	}
}

// TestToolResolveMetersByName drives the whole path with same-named SAFE stubs: the
// stub carries an SDK default tool's name, so it takes exactly the same no-row branch
// and must be metered, while a host tool in the same batch must not be. It then proves
// the ledger write itself: one row per call, correctly attributed, no duplicates.
func TestToolResolveMetersByName(t *testing.T) {
	pg := openTestDB(t)
	wireTools(pg, nil)
	t.Cleanup(func() { agent.ToolResolve = nil })
	t.Setenv("AGENT_CORE_DISABLE_INTERACTIVE_SHELL", "1")

	const sdkName = "Bash" // P1's actual symptom
	const hostName = "traffic_Search"
	t.Cleanup(func() { dropUsage(t, pg, sdkName, hostName) })
	dropUsage(t, pg, sdkName, hostName)

	ri := agent.RunInfo{TaskID: 4242, ExplorationID: 77, IntentID: 13, SessionID: "sess-abc"}
	ctx := agent.WithRunInfo(context.Background(), ri)

	base := []actool.CoreTool{hostStub(sdkName), hostStub(hostName)}
	resolved := byName(agent.ToolResolve(ctx, "worker", base))

	sdkTool, ok := resolved[sdkName]
	if !ok {
		t.Fatalf("%s dropped by resolver", sdkName)
	}
	if _, isMetered := sdkTool.(*meteredTool); !isMetered {
		t.Fatalf("%s is %T, want *meteredTool", sdkName, sdkTool)
	}
	if hostResolved, ok := resolved[hostName]; ok {
		if _, isMetered := hostResolved.(*meteredTool); isMetered {
			t.Fatalf("%s is *meteredTool, want untouched", hostName)
		}
	}

	for i := 0; i < 3; i++ {
		if _, err := sdkTool.Call(ctx, json.RawMessage(`{}`), nil); err != nil {
			t.Fatalf("%s call %d: %v", sdkName, i, err)
		}
	}
	// 3 calls → exactly 3 rows, all attributed to this run. A double wrap would
	// produce 6; a missing wrap would produce 0.
	var n int
	if err := pg.QueryRow(`SELECT COUNT(*) FROM tool_usage WHERE tool_key=$1`, sdkName).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("%s: 3 calls produced %d usage rows, want 3 (missing or duplicate metering)", sdkName, n)
	}
	lastUsage(t, pg, sdkName, "worker", ri)

	// The host tool in the same batch must have written nothing.
	var hostRows int
	if err := pg.QueryRow(`SELECT COUNT(*) FROM tool_usage WHERE tool_key=$1`, hostName).Scan(&hostRows); err != nil {
		t.Fatal(err)
	}
	if hostRows != 0 {
		t.Errorf("%s: host tool wrote %d usage rows, want 0", hostName, hostRows)
	}
}

// TestToolResolveDoesNotDoubleMeterCatalogTools guards the fix against the obvious
// regression: a tool that DOES have a tools-table row must still be metered exactly
// once, not once in the no-row branch and once in the row branch.
func TestToolResolveDoesNotDoubleMeterCatalogTools(t *testing.T) {
	pg := openTestDB(t)
	wireTools(pg, nil)
	t.Cleanup(func() { agent.ToolResolve = nil })
	t.Setenv("AGENT_CORE_DISABLE_INTERACTIVE_SHELL", "1")

	const key = "record_fact"
	t.Cleanup(func() { dropUsage(t, pg, key) })
	dropUsage(t, pg, key)

	ts := agent.NewToolSet(nil, "")
	base := ts.WorkerTools() // record_fact et al. all have catalog rows
	ri := agent.RunInfo{TaskID: 4242, ExplorationID: 77, IntentID: 13}
	ctx := agent.WithRunInfo(context.Background(), ri)

	rf, ok := byName(agent.ToolResolve(ctx, "worker", base))["record_fact"]
	if !ok {
		t.Fatal("worker lost record_fact")
	}
	if _, err := rf.Call(ctx, json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pg.QueryRow(`SELECT COUNT(*) FROM tool_usage WHERE tool_key=$1`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%s: 1 call produced %d usage rows, want 1 (double-metering?)", key, n)
	}
	lastUsage(t, pg, key, "worker", ri)
}
