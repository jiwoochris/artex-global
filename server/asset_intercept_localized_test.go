package server

import "testing"

// Regression guard for the user-facing error messages the intercept rule validators in
// asset_intercept.go and task_intercept.go return: they must stay the exact English source text.
// Both validators (validateAssetInterceptRuleReq, validateTaskInterceptRuleReq) are pure functions
// with no DB access, so besides pinning the constants this test also drives the real error paths
// and checks the returned messages (assertEnglishMessage). Field names and enum values (pattern,
// exact_ip, cidr, kind, action, block, allow) are wire identifiers and stay ASCII; reverting the
// surrounding prose to another language fails the test.
func TestAssetInterceptErrorsLocalized(t *testing.T) {
	// pin the constants
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"pattern_empty", errAssetInterceptPatternEmpty, "Enter a pattern"},
		{"bad_exact_ip", errAssetInterceptInvalidExactIPFmt, "exact_ip must be a valid IP address: %s"},
		{"bad_cidr", errAssetInterceptInvalidCIDRFmt, "cidr must be a valid network range (e.g., 192.168.0.0/16): %s"},
		{"bad_kind", errAssetInterceptInvalidKindFmt, "Invalid kind value: %s"},
		{"bad_action", errTaskInterceptInvalidAction, "action must be block or allow"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}

	// real paths: the four rejection branches of validateAssetInterceptRuleReq
	for _, c := range []struct {
		label       string
		req         assetInterceptRuleReq
		wantEnglish string
	}{
		{"empty_pattern", assetInterceptRuleReq{Kind: "exact_ip", Pattern: "   "}, "Enter a pattern"},
		{"invalid_exact_ip", assetInterceptRuleReq{Kind: "exact_ip", Pattern: "not-an-ip"},
			"exact_ip must be a valid IP address: not-an-ip"},
		{"invalid_cidr", assetInterceptRuleReq{Kind: "cidr", Pattern: "nonsense"},
			"cidr must be a valid network range (e.g., 192.168.0.0/16): nonsense"},
		{"invalid_kind", assetInterceptRuleReq{Kind: "nope", Pattern: "example.com"},
			"Invalid kind value: nope"},
	} {
		req := c.req
		err := validateAssetInterceptRuleReq(&req)
		if err == nil {
			t.Fatalf("%s: no error (validation passed)", c.label)
		}
		assertEnglishMessage(t, c.label, err.Error(), c.wantEnglish)
	}

	// valid input is not rejected
	okReq := assetInterceptRuleReq{Kind: "cidr", Pattern: "192.168.0.0/16"}
	if err := validateAssetInterceptRuleReq(&okReq); err != nil {
		t.Fatalf("a valid CIDR was rejected: %v", err)
	}

	// real path: the action rejection branch of validateTaskInterceptRuleReq
	badAction := taskInterceptRuleReq{Action: "nope", Kind: "exact_domain", Pattern: "example.com"}
	if err := validateTaskInterceptRuleReq(&badAction); err == nil {
		t.Fatalf("bad_action_path: no error (validation passed)")
	} else {
		assertEnglishMessage(t, "bad_action_path", err.Error(), "action must be block or allow")
	}

	// a blank action is normalized to the block default and passes
	defaultAction := taskInterceptRuleReq{Action: "", Kind: "exact_domain", Pattern: "example.com"}
	if err := validateTaskInterceptRuleReq(&defaultAction); err != nil {
		t.Fatalf("default action normalization failed: %v", err)
	}
	if defaultAction.Action != "block" {
		t.Fatalf("default action was not normalized to block: %q", defaultAction.Action)
	}
}
