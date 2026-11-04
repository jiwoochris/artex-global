package agent

import (
	"strings"
	"testing"
)

func TestRenderSystemOverrideAndFallback(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil })

	// no override → built-in default
	PromptOverride = nil
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "g"}); got != "DEFAULT" {
		t.Fatalf("no override should give default, got %q", got)
	}

	// override → rendered with vars
	PromptOverride = func(k string) (string, bool) {
		if k == "planner" {
			return "Goal:{{.Goal}} Scope:{{.Scope}}", true
		}
		return "", false
	}
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "take down X", Scope: "*.x.com"}); got != "Goal:take down X Scope:*.x.com" {
		t.Fatalf("override render: %q", got)
	}

	// override referencing a non-catalog var → execution error → fallback to default
	PromptOverride = func(k string) (string, bool) { return "{{.NotInCatalog}}", true }
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "x"}); got != "DEFAULT" {
		t.Fatalf("bad var should fall back to default, got %q", got)
	}

	// full plannerSystem path: DB body [A] is honored, then the code-owned tail
	// [C] (intermediate artifact output spec) is ALWAYS appended — editing the body can't drop it.
	PromptOverride = func(k string) (string, bool) { return "PLANNER {{.Goal}}", true }
	got := plannerSystem("take down X", "/data", "/data")
	if !strings.HasPrefix(got, "PLANNER take down X") {
		t.Fatalf("plannerSystem body not honored: %q", got)
	}
	if !strings.Contains(got, "Intermediate artifact output spec") || !strings.Contains(got, "/data") {
		t.Fatalf("plannerSystem missing code-owned artifact tail: %q", got)
	}

	// worker dual-text via {{if .ProxyAddr}} in a user template, plus the code tail:
	// [B] trafficTool present only when RECORDING (caCert set — the MITM is on, so
	// the traffic_* tools exist), [C] artifact spec always present. The trafficTool
	// block is gated on the CA (arg 2), NOT on ProxyAddr — a global egress proxy
	// with capture off routes traffic but records nothing.
	PromptOverride = func(k string) (string, bool) {
		return "{{if .ProxyAddr}}via proxy {{.ProxyAddr}}{{else}}manual{{end}}", true
	}
	recording := workerSystem("127.0.0.1:8080", "/ca.pem", "/data", "/data")
	if !strings.HasPrefix(recording, "via proxy 127.0.0.1:8080") {
		t.Fatalf("worker proxy branch body: %q", recording)
	}
	if !strings.Contains(recording, "traffic_search") {
		t.Fatalf("worker while recording should inject trafficTool: %q", recording)
	}
	if strings.Contains(recording, "traffic_refs") {
		t.Fatalf("worker bypassed shared optional evidence policy: %q", recording)
	}
	if !strings.Contains(recording, "Intermediate artifact output spec") {
		t.Fatalf("worker missing artifact tail: %q", recording)
	}
	// Egress proxy set but capture OFF (no CA): the ProxyAddr template branch still
	// renders, but the trafficTool block must NOT — those tools are not registered.
	egressOnly := workerSystem("127.0.0.1:8080", "", "/data", "/data")
	if !strings.HasPrefix(egressOnly, "via proxy 127.0.0.1:8080") {
		t.Fatalf("worker egress-only branch body: %q", egressOnly)
	}
	if strings.Contains(egressOnly, "traffic_search") {
		t.Fatalf("worker without recording must NOT inject trafficTool: %q", egressOnly)
	}
	noProxy := workerSystem("", "", "/data", "/data")
	if !strings.HasPrefix(noProxy, "manual") {
		t.Fatalf("worker no-proxy branch body: %q", noProxy)
	}
	if strings.Contains(noProxy, "traffic_search") {
		t.Fatalf("worker without proxy must NOT inject trafficTool: %q", noProxy)
	}
}

// langMarker is the unambiguous substring that proves langDirective selected a
// given display language: the language's own name as the directive spells it. Used
// by the directive and role-append tests below.
var langMarker = map[string]string{
	"en": "English",
	"es": "Español",
}

// TestLangDirectiveLocalized pins the output-language tail across both
// supported display languages (English, Spanish): each language's directive names that
// language, preserves raw technical strings, forbids mirroring the target/material
// language, names the planner situation summary, and forbids leaking Chinese to the user.
func TestLangDirectiveLocalized(t *testing.T) {
	for _, code := range SupportedOutputLangs {
		dir := langDirective(code)
		if !strings.Contains(dir, langMarker[code]) {
			t.Fatalf("%s: langDirective must name the display language (%q), got %q", code, langMarker[code], dir)
		}
		if !strings.Contains(dir, "payload") || !strings.Contains(dir, "kept word for word") {
			t.Fatalf("%s: langDirective must keep commands/payloads verbatim, got %q", code, dir)
		}
		if !strings.Contains(dir, "do not mirror or copy") {
			t.Fatalf("%s: langDirective must forbid mirroring the target/material language, got %q", code, dir)
		}
		if !strings.Contains(dir, "situation summary") {
			t.Fatalf("%s: langDirective must name the planner situation summary as user-facing, got %q", code, dir)
		}
		// Chinese is not a supported display language any more, so the anti-Chinese-leak
		// clause (planner situation-summary drift) applies to every supported language.
		if !strings.Contains(dir, "with no Chinese") {
			t.Fatalf("%s: langDirective must forbid leaking Chinese to the user, got %q", code, dir)
		}
	}
}

// TestLangDirectiveAppendedToUserFacingRoles pins the localization tail wiring:
// every user-facing role's system prompt must end with the code-owned
// output-language directive in the configured language, and a DB-edited body must
// NOT be able to drop it. Exercised for each supported display language by driving
// the OutputLanguage hook the roles resolve through.
func TestLangDirectiveAppendedToUserFacingRoles(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil; OutputLanguage = nil })

	// Even with a DB body that is pure non-directive text, the code-owned tail is
	// still appended for each user-facing builder — identical guarantee to the
	// artifact tail. A custom body can never translate away the output-language mandate.
	PromptOverride = func(string) (string, bool) { return "BODY-ONLY", true }

	for _, code := range SupportedOutputLangs {
		OutputLanguage = func() string { return code }
		marker := langMarker[code]
		cases := map[string]string{
			"worker":    workerSystem("", "", "/data", "/data"),
			"planner":   plannerSystem("g", "/data", "/data"),
			"mainagent": mainAgentSystem("g", "/data", "/data"),
			"chat":      chatSystem("chat", "/data", "/data"),
			// goals is user-facing too: set_goals/set_constraints persist goal and
			// constraint nodes shown in the UI graph/plan tab. withScope=true exercises
			// the longer assembly (body + scope tail), so the language tail must still
			// land last — after both the body and the code-owned scope tail.
			"goals": goalsSystem("/data", true),
		}
		for role, sys := range cases {
			if !strings.HasPrefix(sys, "BODY-ONLY") {
				t.Fatalf("%s/%s: DB body not honored: %q", code, role, sys)
			}
			if !strings.Contains(sys, marker) {
				t.Fatalf("%s/%s: missing output-language tail (%q): %q", code, role, marker, sys)
			}
			// The directive is the tail — it must come AFTER the body (recency).
			if strings.Index(sys, marker) <= strings.Index(sys, "BODY-ONLY") {
				t.Fatalf("%s/%s: langDirective must be appended after the body: %q", code, role, sys)
			}
		}
	}
}
