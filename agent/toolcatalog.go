package agent

import (
	"context"
	"encoding/json"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

// This file turns built-in tools from plain code into a catalogue that can be enumerated
// and overridden from the DB:
//   - BuiltinToolSeeds(): flattens the built-in toolsets of the three executor agents into
//     seed records (key + description + parameter schema + default agent bindings), which the
//     server idempotently seeds into the tools table at start-up.
//   - the ToolResolve hook: at runtime it applies the tools rows to the assembled tool list
//     (per-agent filtering + description/schema overrides + injection of parameter defaults).
// The key and handler stay in code; the DB can only change the prose and the defaults.
// The handler (Call behaviour) always comes from code — the DB cannot change it, only the
// description and omitted-argument defaults the model sees.

// ToolSeed is the seedable snapshot of one built-in tool: key is CoreTool.Name() (hard-bound
// to the handler and read-only in the UI), Desc/Schema come from the tool definition in code,
// and Agents lists the agents the code binds it to by default.
type ToolSeed struct {
	Key    string         // = CoreTool.Name(), the primary key; immutable
	Desc   string         // top-level description (overridable in the UI)
	Schema map[string]any // parameter JSON-Schema (structure read-only; description/default editable in the UI)
	Agents []string       // agent keys bound by default (worker/planner/mainagent)
}

// builtinToolsByAgent builds each executor agent's domain toolset from a read-only empty
// ToolSet (nil stores). The tool constructors only put closures into a Spec and never
// dereference a store at construction time, so nil is safe — these tools are used only to
// read Name()/Description()/InputSchema() here, never to Call.
//
// The SDK's generic tools actool.DefaultTools() (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
// are deliberately NOT included: every agent always has them, there is no "bound to whom"
// choice, and most of their description lives in Prompt() (this table covers only
// Description(), so a partial override would mislead). Not seeded → no DB row → ToolResolve
// passes them through untouched, so behaviour is unchanged. Only artex's own domain tools are
// in the table and therefore manageable.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals (the goal decomposer) binds set_goals + set_constraints by default: they persist
		// the decomposed goals and the extracted operating constraints. It shares these managed
		// tools with mainagent; the web UI can edit their description/schema and tick agents.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto binds finding reporting + asset management tools by default; other domain tools
		// can be ticked on demand in the UI. Fresh DBs seed from here; old DBs are migrated by
		// seedAutoDefaultBindings.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest (the standalone pentest agent) binds by default: list assets / insert assets /
		// report a finding / list findings / list companies. Fresh DBs seed from here; old DBs are
		// migrated by seedPentestDefaultBindings.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound lists system tools that still enter the catalogue (visible in the web UI and
// manually tickable per agent) but are bound to NO agent by default — ToolResolve drops an
// empty binding for every agent, so an explicit opt-in is required. They stay in some agent's
// base toolset (e.g. goal_met in PlannerTools) for two reasons: the seed can construct them to
// read their desc/schema, and once a user binds them back by hand the runtime base still has
// them, which is what lets ToolResolve keep them.
//
// goal_met: it bypasses per-goal prove_goal and declares the WHOLE task complete globally, so it
// is powerful and can misjudge, and it duplicates "prove_goal marks the last goal → automatic
// wrap-up". It is therefore bound to no agent by default; bind it by hand when needed.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds merges the agents' built-in toolsets into a de-duplicated seed list: tools
// with the same name (e.g. list_assets, held by several agents) collapse into one entry whose
// Agents is the union; tools in defaultUnbound are forced to an empty binding.
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
			agents = []string{} // in the catalogue and manually bindable, but bound to no agent by default (stored as [] not null, consistent with other tools)
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
// omitted-argument defaults get injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

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
// null. Structure (names/types/required) is untouched — only the default values are merged in.
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

// aliasTool is a CoreTool decorator that overrides only Name(): it lets a
// CamelCase built-in (e.g. Bash) also answer to the lowercase/snake_case spelling
// a model drifts into (bash, web_fetch). Handler, description, schema, prompt and
// permission all delegate to the wrapped tool, so an alias is behaviourally
// identical — it only repairs exact-match dispatch lookup (norma's execOne does
// reg.Get(use.Name), no case folding). See issue #19.
type aliasTool struct {
	actool.CoreTool
	name string
}

func (a *aliasTool) Name() string { return a.name }

// toolAliasNames returns the lowercase / snake_case / UPPER spellings a CamelCase
// tool name is commonly confused with, dropping the original and duplicates. A
// name already in lower/snake form yields nothing (no drift to defend against).
func toolAliasNames(name string) []string {
	if name == strings.ToLower(name) {
		return nil
	}
	candidates := []string{strings.ToLower(name), camelToSnake(name), strings.ToUpper(name)}
	out := make([]string, 0, len(candidates))
	seen := map[string]bool{name: true}
	for _, v := range candidates {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// camelToSnake converts CamelCase to snake_case (WebFetch → web_fetch,
// TodoWrite → todo_write) while leaving an all-caps run intact (LS → ls).
func camelToSnake(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				prev := runes[i-1]
				var next rune
				if i+1 < len(runes) {
					next = runes[i+1]
				}
				if (prev >= 'a' && prev <= 'z') || (next >= 'a' && next <= 'z') {
					b.WriteByte('_')
				}
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// addToolAliases registers drift-tolerant aliases for every CamelCase tool. Each
// alias is added to the returned tool list (so exact-match dispatch finds it) and
// its name is appended to deferred (so the schema is withheld and the model keeps
// seeing only the official CamelCase names — no duplicate exposure, no token
// waste). Names already taken by a real tool are skipped, so nothing is shadowed.
func addToolAliases(tools []actool.CoreTool, deferred []string) ([]actool.CoreTool, []string) {
	taken := map[string]bool{}
	for _, t := range tools {
		taken[t.Name()] = true
	}
	for _, n := range deferred {
		taken[n] = true
	}
	out := tools
	extra := deferred
	for _, t := range tools {
		for _, alias := range toolAliasNames(t.Name()) {
			if taken[alias] {
				continue
			}
			taken[alias] = true
			out = append(out, &aliasTool{CoreTool: t, name: alias})
			extra = append(extra, alias)
		}
	}
	return out, extra
}
