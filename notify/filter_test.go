package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// Malformed JSON, empty input, wrongly typed fields — all must degrade to the zero-value
	// Filter, i.e. "no filtering". This invariant is where "better to over-send than to miss"
	// lives: if this ever became an error or a partial parse, one mistyped character from the
	// user would silently drop every high-severity notification.
	cases := []struct {
		name string
		raw  string
	}{
		{"empty input", ""},
		{"invalid JSON", `{not json`},
		{"truncated JSON", `{"min_severity":`},
		{"type mismatch", `{"min_severity": 123, "task_ids": "abc"}`},
		{"top level is an array", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("malformed config should degrade to the zero-value Filter, got %+v", f)
			}
			// The zero-value Filter must match any event.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("the zero-value Filter should match every event")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// An unknown severity ranks 0, so any non-empty threshold blocks it (when in doubt, do not send).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: want %v got %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL Injection",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"empty scope = unrestricted", Filter{}, true},
		{"task matches", Filter{TaskIDs: []int64{7}}, true},
		{"task does not match", Filter{TaskIDs: []int64{8}}, false},
		{"multi-select task includes a match", Filter{TaskIDs: []int64{8, 7}}, true},
		{"assets intersect", Filter{AssetIDs: []int64{20, 99}}, true},
		{"assets do not intersect", Filter{AssetIDs: []int64{99}}, false},
		{"task and asset both match", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"task matches but asset does not", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("want %v got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"empty include = accept all", Filter{}, "any class", true},
		{"include matches", Filter{VulnClassInclude: []string{"SQL"}}, "SQL Injection", true},
		{"include does not match", Filter{VulnClassInclude: []string{"Command Execution"}}, "SQL Injection", false},
		{"any of several include words matches", Filter{VulnClassInclude: []string{"Command Execution", "SQL"}}, "SQL Injection", true},
		{"case insensitive", Filter{VulnClassInclude: []string{"sql"}}, "SQL Injection", true},
		{"exclude match drops the event", Filter{VulnClassExclude: []string{"Information Disclosure"}}, "Information Disclosure", false},
		{"exclude miss lets the event through", Filter{VulnClassExclude: []string{"Information Disclosure"}}, "SQL Injection", true},
		// Exclude wins over include: a match on both drops the event.
		{"exclude wins over include", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"Injection"},
		}, "SQL Injection", false},
		// Whitespace-only keywords must be ignored, otherwise this degrades into
		// "match every string containing a space".
		{"whitespace keyword ignored", Filter{VulnClassInclude: []string{"", "  "}}, "SQL Injection", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("want %v got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// Off by default: when most people say "push findings" they mean new findings, not a status feed.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("status-change events should be skipped unless the option is on")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("status-change events should match once on_status_change is on")
	}
	// Created events are unaffected by on_status_change.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("created events should not depend on on_status_change")
	}
}
