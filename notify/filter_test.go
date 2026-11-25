package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// Malformed JSON, empty input, fields of the wrong type — all of them must degrade to
	// the zero-value Filter, i.e. "no filtering". This invariant is where "rather push
	// too much than miss something" lands: if this were ever changed to return an error
	// or parse partially, a user mistyping a single character would silently lose all
	// high-severity notifications.
	cases := []struct {
		name string
		raw  string
	}{
		{"空输入", ""},
		{"非法 JSON", `{not json`},
		{"截断的 JSON", `{"min_severity":`},
		{"类型不匹配", `{"min_severity": 123, "task_ids": "abc"}`},
		{"顶层是数组", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("畸形配置应退化为零值 Filter，得到 %+v", f)
			}
			// The zero-value Filter must match any event.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("零值 Filter 应命中所有事件")
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
		// Unknown severities have ordinal 0 and should be blocked by any non-empty threshold
		// (when in doubt, don't push).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: 期望 %v 得到 %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL注入",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"空范围=不限", Filter{}, true},
		{"任务命中", Filter{TaskIDs: []int64{7}}, true},
		{"任务未命中", Filter{TaskIDs: []int64{8}}, false},
		{"任务多选含命中", Filter{TaskIDs: []int64{8, 7}}, true},
		{"资产有交集", Filter{AssetIDs: []int64{20, 99}}, true},
		{"资产无交集", Filter{AssetIDs: []int64{99}}, false},
		{"任务与资产同时命中", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"任务命中但资产未命中", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("期望 %v 得到 %v", tc.expect, got)
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
		{"include 为空=全收", Filter{}, "任意类型", true},
		{"include 命中", Filter{VulnClassInclude: []string{"SQL"}}, "SQL注入", true},
		{"include 未命中", Filter{VulnClassInclude: []string{"命令执行"}}, "SQL注入", false},
		{"include 多词任一命中", Filter{VulnClassInclude: []string{"命令执行", "SQL"}}, "SQL注入", true},
		{"大小写不敏感", Filter{VulnClassInclude: []string{"sql"}}, "SQL注入", true},
		{"exclude 命中即排除", Filter{VulnClassExclude: []string{"信息泄露"}}, "信息泄露", false},
		{"exclude 未命中则放行", Filter{VulnClassExclude: []string{"信息泄露"}}, "SQL注入", true},
		// Exclusion takes precedence over inclusion: when both match, the event is out.
		{"排除优先于包含", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"注入"},
		}, "SQL注入", false},
		// Whitespace-only keywords should be ignored, otherwise they would degrade into
		// "match every string containing a space".
		{"空白关键词被忽略", Filter{VulnClassInclude: []string{"", "  "}}, "SQL注入", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("期望 %v 得到 %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// Off by default: what most people mean by "push findings" is newly discovered
	// findings, not a running log of status changes.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("状态变更事件在未开启时应被跳过")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("开启 on_status_change 后状态变更事件应命中")
	}
	// Creation events are not affected by on_status_change.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("创建事件不应依赖 on_status_change")
	}
}
