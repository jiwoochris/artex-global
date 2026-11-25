package intercept

import (
	"regexp"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// A tool_input rule must see the decoded command, not its JSON serialization:
// rm -rf "/etc" is stored as rm -rf \"/etc\" and a naive regex misses both
// the opening quote and the closing boundary.
func TestRuleMatchesDecodedJSONString(t *testing.T) {
	rule := compiledRule{
		InterceptRule: db.InterceptRule{
			MatchTarget: "tool_input",
			MatchType:   "regex",
			Pattern:     `rm -rf "/etc"`,
		},
		re: regexp.MustCompile(`rm -rf "/etc"`),
	}
	input := []byte(`{"command":"rm -rf \"/etc\""}`)
	if !ruleMatches(rule, "Bash", input) {
		t.Fatal("escaped JSON command must match the decoded string")
	}
	if len(jsonStringValues(input)) != 1 || jsonStringValues(input)[0] != `rm -rf "/etc"` {
		t.Fatalf("unexpected decoded values: %v", jsonStringValues(input))
	}
}

func TestRuleMatchesStillChecksRawPayload(t *testing.T) {
	rule := compiledRule{
		InterceptRule: db.InterceptRule{
			MatchTarget: "tool_input",
			MatchType:   "regex",
			Pattern:     "whoami",
			ToolNames:   []string{"Bash"},
		},
		re: regexp.MustCompile("whoami"),
	}
	if !ruleMatches(rule, "Bash", []byte(`{"command":"whoami"}`)) {
		t.Fatal("raw payload match must keep working")
	}
	if ruleMatches(rule, "Write", []byte(`{"content":"whoami"}`)) {
		t.Fatal("tool scope must still be enforced")
	}
}

func TestNormalizeCommand(t *testing.T) {
	cases := map[string]string{
		"rm -rf /app/data/.":              "rm -rf /app/data",
		"rm -rf /app//data":               "rm -rf /app/data",
		"rm -rf .//data":                  "rm -rf data",
		"rm -rf /app/data//":              "rm -rf /app/data",
		"rm -rf /app/data/tasks/58/../..": "rm -rf /app/data",
		`r""m -rf /etc`:                   "rm -rf /etc",
		`r\m -rf /etc`:                    "rm -rf /etc",
		`rm -rf /et""c`:                   "rm -rf /etc",
		`rm -rf /app/da""ta`:              "rm -rf /app/data",
		"curl https://example.com//x":     "curl https://example.com//x",
	}
	for in, want := range cases {
		if got := normalizeCommand(in); got != want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRuleMatchesAliasAndMCP(t *testing.T) {
	prev := ToolNameCanonicalizer
	ToolNameCanonicalizer = func(n string) string {
		if n == "bash" {
			return "Bash"
		}
		return n
	}
	t.Cleanup(func() { ToolNameCanonicalizer = prev })
	rule := compiledRule{
		InterceptRule: db.InterceptRule{
			MatchTarget: "tool_input",
			MatchType:   "regex",
			Pattern:     "reboot",
			ToolNames:   []string{"Bash"},
		},
		re: regexp.MustCompile("reboot"),
	}
	if !ruleMatches(rule, "bash", []byte(`{"command":"reboot"}`)) {
		t.Fatal("alias must resolve to the canonical tool scope")
	}
	mcpRule := compiledRule{
		InterceptRule: db.InterceptRule{
			MatchTarget: "tool_input",
			MatchType:   "regex",
			Pattern:     "/delete",
			ToolNames:   []string{"ExecuteExtraTool"},
		},
		re: regexp.MustCompile("/delete"),
	}
	if !ruleMatches(mcpRule, "mcp__zap__request", []byte(`{"url":"/delete"}`)) {
		t.Fatal("MCP calls must inherit the ExecuteExtraTool scope")
	}
}

func TestIsToolEnabledCoversExecutionTools(t *testing.T) {
	i := &Interceptor{enabledTools: map[string]bool{}}
	for _, name := range defaultEnabledTools {
		i.enabledTools[name] = true
	}
	for _, name := range []string{"ExecuteExtraTool", "oob_generate", "oob_poll", "mcp__zap__request"} {
		if !i.IsToolEnabled(name) {
			t.Errorf("execution tool %q must enter the rule path", name)
		}
	}
}
