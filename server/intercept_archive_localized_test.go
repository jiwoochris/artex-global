package server

import (
	"net/url"
	"strings"
	"testing"
)

// assertEnglishMessage fails when got is not exactly the English message want.
// The suite pins the English source output language (testmain_test.go), so a
// literal that reverts to Korean/Chinese, an accidental reword, or a message
// that stops reaching the caller all fail here.
func assertEnglishMessage(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: message = %q, want the English source %q", label, got, want)
	}
}

// assertEnglishPrefix is assertEnglishMessage for a message that appends a
// wrapped lower-layer detail: got must start with the exact English want.
func assertEnglishPrefix(t *testing.T, label, got, want string) {
	t.Helper()
	if !strings.HasPrefix(got, want) {
		t.Fatalf("%s: message = %q, want the English prefix %q", label, got, want)
	}
}

// TestInterceptArchiveErrorsLocalized guards the F3 first bundle: every
// user-facing error response produced by the pure validators in intercept.go and
// task_archives.go must be the exact English source message. If anyone reverts
// one of these literals to Korean or Chinese, this test fails.
func TestInterceptArchiveErrorsLocalized(t *testing.T) {
	// intercept.go: interceptFilterParams — query-string validation.
	if _, err := interceptFilterParams(url.Values{"status": {"bogus"}}); err == nil {
		t.Fatal("status validation must not pass")
	} else {
		assertEnglishMessage(t, "filter.status", err.Error(),
			"status must be one of pending, allowed, denied, timeout")
	}
	if _, err := interceptFilterParams(url.Values{"decision_source": {"bogus"}}); err == nil {
		t.Fatal("decision_source validation must not pass")
	} else {
		assertEnglishMessage(t, "filter.decision_source", err.Error(),
			"decision_source must be one of model, rule, unknown")
	}

	// intercept.go: validateInterceptRuleReq — each branch isolated by making the
	// earlier fields valid.
	ruleCases := []struct {
		label string
		req   interceptRuleReq
		want  string
	}{
		{"rule.name", interceptRuleReq{Name: ""}, "name must not be empty"},
		{"rule.match_target", interceptRuleReq{Name: "r", MatchTarget: "bad"},
			"match_target must be either tool_name or tool_input"},
		{"rule.match_type", interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "bad"},
			"match_type must be either string or regex"},
		{"rule.pattern_empty", interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "string", Pattern: ""},
			"pattern must not be empty"},
		{"rule.action", interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "string", Pattern: "p", Action: "bad"},
			"action must be one of allow, deny, ask"},
	}
	for _, tc := range ruleCases {
		err := validateInterceptRuleReq(tc.req)
		if err == nil {
			t.Fatalf("%s: validation must not pass", tc.label)
		}
		assertEnglishMessage(t, tc.label, err.Error(), tc.want)
	}
	// The regex branch wraps the regexp package detail, so only the English
	// source prefix is deterministic.
	if err := validateInterceptRuleReq(interceptRuleReq{Name: "r", MatchTarget: "tool_name", MatchType: "regex", Pattern: "(", Action: "allow"}); err == nil {
		t.Fatal("rule.regex: validation must not pass")
	} else {
		assertEnglishPrefix(t, "rule.regex", err.Error(), "pattern is not a valid regular expression: ")
	}

	// task_archives.go: normalizeArchiveIDs — batch id validation.
	if _, err := normalizeArchiveIDs(nil); err == nil {
		t.Fatal("empty archive_ids validation must not pass")
	} else {
		assertEnglishMessage(t, "archive_ids.empty", err.Error(), "archive_ids cannot be empty")
	}
	tooMany := make([]int64, 101)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	if _, err := normalizeArchiveIDs(tooMany); err == nil {
		t.Fatal("validation of 101 archive_ids must not pass")
	} else {
		assertEnglishMessage(t, "archive_ids.too_many", err.Error(),
			"At most 100 archives can be processed at once")
	}
	if _, err := normalizeArchiveIDs([]int64{0}); err == nil {
		t.Fatal("validation of a 0 archive id must not pass")
	} else {
		assertEnglishMessage(t, "archive_ids.nonpositive", err.Error(), "archive id 0 is invalid")
	}

	// task_archives.go: validateArchivePath — package path validation.
	if err := validateArchivePath("/tmp/artex-data", ""); err == nil {
		t.Fatal("empty archive path validation must not pass")
	} else {
		assertEnglishMessage(t, "archive_path.empty", err.Error(), "The archive package path is empty")
	}
	if err := validateArchivePath("/tmp/artex-data", "/etc/passwd"); err == nil {
		t.Fatal("validation of a path outside the managed directory must not pass")
	} else {
		assertEnglishMessage(t, "archive_path.outside", err.Error(),
			"The archive package path is not inside the managed directory")
	}
}
