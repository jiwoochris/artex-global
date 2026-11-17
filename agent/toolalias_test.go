package agent

import (
	"context"
	"encoding/json"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestCamelToSnake(t *testing.T) {
	cases := map[string]string{
		"Bash":            "bash",
		"WebFetch":        "web_fetch",
		"TodoWrite":       "todo_write",
		"TaskList":        "task_list",
		"MultiEdit":       "multi_edit",
		"AskUserQuestion": "ask_user_question",
		"LS":              "ls",
		"sleep":           "sleep",
	}
	for in, want := range cases {
		if got := camelToSnake(in); got != want {
			t.Errorf("camelToSnake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolAliasNames(t *testing.T) {
	bash := toolAliasNames("Bash")
	if !contains(bash, "bash") || !contains(bash, "BASH") || contains(bash, "Bash") {
		t.Errorf("Bash aliases = %v", bash)
	}
	wf := toolAliasNames("WebFetch")
	for _, want := range []string{"webfetch", "web_fetch", "WEBFETCH"} {
		if !contains(wf, want) {
			t.Errorf("WebFetch aliases %v missing %q", wf, want)
		}
	}
	if a := toolAliasNames("sleep"); len(a) != 0 {
		t.Errorf("lowercase name should have no aliases, got %v", a)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestAddToolAliasesDelegates proves an alias is dispatchable by its drifted name
// and that Call/Description/Schema delegate to the wrapped handler.
func TestAddToolAliasesDelegates(t *testing.T) {
	var called bool
	orig := actool.Build(actool.Spec{
		Name:        "Bash",
		Description: "run a shell command",
		Run: func(_ context.Context, _ json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			called = true
			return actool.Text("ok"), nil
		},
	})
	out, deferred := addToolAliases([]actool.CoreTool{orig}, nil)
	byName := map[string]actool.CoreTool{}
	for _, tl := range out {
		byName[tl.Name()] = tl
	}
	if _, ok := byName["Bash"]; !ok {
		t.Fatal("original Bash dropped")
	}
	alias, ok := byName["bash"]
	if !ok {
		t.Fatalf("bash alias not registered; have %v", keys(byName))
	}
	if !contains(deferred, "bash") {
		t.Errorf("bash alias not deferred: %v", deferred)
	}
	if alias.Description() != "run a shell command" {
		t.Errorf("alias description = %q", alias.Description())
	}
	if _, err := alias.Call(context.Background(), json.RawMessage(`{}`), nil); err != nil || !called {
		t.Fatalf("alias Call did not reach handler (called=%v err=%v)", called, err)
	}
}

// TestAddToolAliasesSkipsTaken ensures an alias never shadows an existing tool.
func TestAddToolAliasesSkipsTaken(t *testing.T) {
	camel := actool.Build(actool.Spec{Name: "Bash", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		return actool.Text(""), nil
	}})
	lower := actool.Build(actool.Spec{Name: "bash", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		return actool.Text(""), nil
	}})
	out, _ := addToolAliases([]actool.CoreTool{camel, lower}, nil)
	n := 0
	for _, tl := range out {
		if tl.Name() == "bash" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("bash registered %d times, want 1 (alias must not shadow)", n)
	}
}

// TestAugmentToolsRegistersAliases is the end-to-end check: assembling a real
// agent base (DefaultTools) yields lowercase aliases that are callable by name,
// while every alias schema is withheld via the deferred list.
func TestAugmentToolsRegistersAliases(t *testing.T) {
	oldPolicy, oldResolve, oldAugment := FindingTrafficBindingEnabled, ToolResolve, ToolAugment
	t.Cleanup(func() { FindingTrafficBindingEnabled, ToolResolve, ToolAugment = oldPolicy, oldResolve, oldAugment })
	ToolAugment = nil
	ToolResolve = nil
	FindingTrafficBindingEnabled = nil

	out, def, cleanup := AugmentTools(context.Background(), "worker", actool.DefaultTools())
	defer cleanup()

	byName := map[string]actool.CoreTool{}
	for _, tl := range out {
		byName[tl.Name()] = tl
	}
	for _, want := range []string{"Bash", "bash", "LS", "ls", "WebFetch"} {
		switch want {
		case "WebFetch":
			continue // not in DefaultTools
		}
		if _, ok := byName[want]; !ok {
			t.Errorf("assembled worker missing %q", want)
		}
	}
	deferredSet := map[string]bool{}
	for _, n := range def.Deferred {
		deferredSet[n] = true
	}
	if !deferredSet["bash"] || !deferredSet["read"] || !deferredSet["ls"] {
		t.Errorf("aliases not deferred (would duplicate schemas): %v", def.Deferred)
	}
	if deferredSet["Bash"] {
		t.Error("official Bash name must NOT be deferred")
	}
}

func keys(m map[string]actool.CoreTool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
