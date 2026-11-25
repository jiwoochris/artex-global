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
