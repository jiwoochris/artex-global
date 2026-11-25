package server

import "testing"

// Regression guard test that keeps the user-facing error text returned by the intercept
// rule validators in asset_intercept.go · task_intercept.go in Korean. The two validators
// (validateAssetInterceptRuleReq · validateTaskInterceptRuleReq) are pure functions that
// do not touch the DB, so on top of pinning the constants, the test calls the real error
// paths directly and checks the returned text as well.
// The Korean check reuses assertKoreanError (contains Hangul · zero Chinese Han), built in F3a.
// Field names · enum values (pattern · exact_ip · cidr · kind · action · block · allow) are
// wire identifiers, so they stay ASCII and are not caught by the Han check. The test fails
// if someone reverts the surrounding description back to Chinese.
func TestAssetInterceptErrorsLocalized(t *testing.T) {
	// Pin the constants
	for _, c := range []struct{ label, msg string }{
		{"pattern_empty", errAssetInterceptPatternEmpty},
		{"bad_exact_ip", errAssetInterceptInvalidExactIPFmt},
		{"bad_cidr", errAssetInterceptInvalidCIDRFmt},
		{"bad_kind", errAssetInterceptInvalidKindFmt},
		{"bad_action", errTaskInterceptInvalidAction},
	} {
		assertKoreanError(t, c.label, c.msg)
	}

	// Real path: the four rejection branches of validateAssetInterceptRuleReq
	for _, c := range []struct {
		label string
		req   assetInterceptRuleReq
	}{
		{"empty_pattern", assetInterceptRuleReq{Kind: "exact_ip", Pattern: "   "}},
		{"invalid_exact_ip", assetInterceptRuleReq{Kind: "exact_ip", Pattern: "not-an-ip"}},
		{"invalid_cidr", assetInterceptRuleReq{Kind: "cidr", Pattern: "nonsense"}},
		{"invalid_kind", assetInterceptRuleReq{Kind: "nope", Pattern: "example.com"}},
	} {
		req := c.req
		err := validateAssetInterceptRuleReq(&req)
		if err == nil {
			t.Fatalf("%s: 오류가 없습니다(검증을 통과함)", c.label)
		}
		assertKoreanError(t, c.label, err.Error())
	}

	// Valid input is not rejected
	okReq := assetInterceptRuleReq{Kind: "cidr", Pattern: "192.168.0.0/16"}
	if err := validateAssetInterceptRuleReq(&okReq); err != nil {
		t.Fatalf("유효한 CIDR 가 거부되었습니다: %v", err)
	}

	// Real path: the action rejection branch of validateTaskInterceptRuleReq
	badAction := taskInterceptRuleReq{Action: "nope", Kind: "exact_domain", Pattern: "example.com"}
	if err := validateTaskInterceptRuleReq(&badAction); err == nil {
		t.Fatalf("bad_action_path: 오류가 없습니다(검증을 통과함)")
	} else {
		assertKoreanError(t, "bad_action_path", err.Error())
	}

	// An empty action is normalized to the default "block" and passes
	defaultAction := taskInterceptRuleReq{Action: "", Kind: "exact_domain", Pattern: "example.com"}
	if err := validateTaskInterceptRuleReq(&defaultAction); err != nil {
		t.Fatalf("기본 action 정규화에 실패했습니다: %v", err)
	}
	if defaultAction.Action != "block" {
		t.Fatalf("기본 action 이 block 으로 정규화되지 않았습니다: %q", defaultAction.Action)
	}
}
