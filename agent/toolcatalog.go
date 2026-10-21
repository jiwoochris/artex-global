package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// This file exposes built-in tools as an enumerable, database-overridable catalog:
// BuiltinToolSeeds expands execution agents' tools into key, description, schema,
// and default-agent bindings for idempotent startup seeding into tools.
// ToolResolve filters by agent, overrides description/schema, and injects defaults
// from database rows. Tool identity and handlers remain code-owned.
// The database can edit model-facing prose and defaults, never Call behavior.

// ToolSeed snapshots a built-in: Key is CoreTool.Name(), bound to its handler and
// read-only in UI; Desc/Schema come from code, Agents lists default bindings.
type ToolSeed struct {
	Key    string         // Immutable primary key, equal to CoreTool.Name().
	Desc   string         // Top-level description, overridable in UI.
	Schema map[string]any // Parameter JSON Schema: structure is read-only; description/default are editable.
	Agents []string       // Default agent keys: worker/planner/mainagent.
}

// builtinToolsByAgent uses a read-only shell ToolSet with nil stores to construct
// each agent's domain tools. Constructors capture closures without dereferencing
// stores; only Name/Description/InputSchema are read here, never Call.
//
// Exclude SDK actool.DefaultTools (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash):
// every agent always owns them, and much of their guidance lives in Prompt,
// which this catalog does not override. Seeding only Description would mislead.
// Without a DB row, ToolResolve leaves them unchanged; only local domain tools are managed.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals binds set_goals and set_constraints to persist decomposed goals and
		// extracted boundaries. It shares mainagent's managed tools and UI metadata/bindings.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto defaults to finding reporting and asset management; other tools are opt-in.
		// New databases use these seeds; existing databases use seedAutoDefaultBindings.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// Solo pentest defaults to asset lookup/insertion, finding reporting/lookup, and company lookup.
		// New databases use these seeds; existing databases use seedPentestDefaultBindings.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound tools appear in the catalog for manual assignment but have no
// default agents. ToolResolve drops empty-bound tools for every agent until opt-in.
// They remain in base sets such as PlannerTools so seeds can read metadata and
// runtime resolution can retain them when operators explicitly bind them.
//
// goal_met bypasses per-goal proof and can wrongly complete the whole task. It
// duplicates prove_goal's final-goal completion, so it requires explicit opt-in.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds deduplicates each agent's tools by name, merging agent bindings.
// Tools in defaultUnbound are forced to an empty binding list.
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // Cataloged for manual binding, but default to [] rather than null, matching other tools.
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// Parameter defaults are injected. Tools without matching rows (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// ToolMeter, if set, wraps each tool so one usage-ledger row is recorded per real
// invocation, attributed to ri. The server wires it to the PG tool_usage ledger.
//
// ToolResolve cannot cover every path: goal decomposition (agent/goals.go) builds its
// tool list directly instead of going through the resolver, so set_goals and
// set_constraints never reached the ledger. ToolMeter exists for those
// resolver-bypassing call sites; it meters whatever list it is handed and leaves the
// tools themselves untouched otherwise.
//
// nil = no metering (the default outside the server). Wired in server/assembly.go.
var ToolMeter func(tools []actool.CoreTool, agentKey string, ri RunInfo) []actool.CoreTool

// MeterTools routes tools through ToolMeter, if wired. Safe to call unconditionally.
func MeterTools(tools []actool.CoreTool, agentKey string, ri RunInfo) []actool.CoreTool {
	if ToolMeter == nil {
		return tools
	}
	return ToolMeter(tools, agentKey, ri)
}

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) remains untouched; only defaults are merged.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
