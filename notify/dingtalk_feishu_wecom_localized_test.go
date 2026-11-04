package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// asciiStatusChangeItem builds a status-change item whose data is pure ASCII. A label
// that regressed to Chinese or Korean would add ideographs/Hangul to the rendering, so
// the "no Han, no Hangul in the output" assertion pins the labels exactly when the data
// itself contributes none.
func asciiStatusChangeItem() Item {
	return Item{
		FindingID:  7,
		Name:       "login-flaw",
		VulnClass:  "SQLi",
		Severity:   "high",
		Summary:    "SQL injection via q param",
		Assets:     []string{"a.example.com"},
		DetailURL:  "https://platform.example/finding/7",
		FromStatus: "pending",
		ToStatus:   "fixed",
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("card serialisation failed: %v", err)
	}
	return string(b)
}

// TestFeishuItemLinesLocalized checks that the field labels (status change, type, assets,
// summary) of a single Feishu card body are English and match markdown.go character for
// character. feishu.go keeps its own copy of these labels instead of sharing the markdown
// helper, so this pin is needed on top of the shared-function regression test: otherwise
// only the Feishu body could drift back to another language unnoticed.
func TestFeishuItemLinesLocalized(t *testing.T) {
	got := feishuItemLines(asciiStatusChangeItem())
	assertNoUnsupportedScript(t, "feishuItemLines", got)
	for _, want := range []string{
		"**Status change**: Pending → Fixed",
		"**Type**: SQLi",
		"**Assets**: a.example.com",
		"**Summary**: SQL injection via q param",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("single Feishu card body must contain %q:\n%s", want, got)
		}
	}
}

// TestFeishuCardButtonsLocalized checks that the Feishu card button labels (view details,
// view everything in the platform) are English and that the whole card carries no Han or
// Hangul (with ASCII data).
func TestFeishuCardButtonsLocalized(t *testing.T) {
	// Single item: the "View details" button (when DetailURL is set).
	single, _ := feishuCard(Message{Items: []Item{asciiStatusChangeItem()}})
	singleJSON := mustJSON(t, single)
	assertNoUnsupportedScript(t, "single feishu card", singleJSON)
	if !strings.Contains(singleJSON, "View details") {
		t.Errorf("single Feishu card must have a 'View details' button:\n%s", singleJSON)
	}

	// Batch: the "View everything in the platform" button (when HomeURL is set).
	batch, _ := feishuCard(Message{Batch: true, HomeURL: "https://platform.example", Items: hanFreeItems(2)})
	batchJSON := mustJSON(t, batch)
	assertNoUnsupportedScript(t, "batch feishu card", batchJSON)
	if !strings.Contains(batchJSON, "View everything in the platform") {
		t.Errorf("batch Feishu card must have a 'View everything in the platform' button:\n%s", batchJSON)
	}
}

// TestDingTalkActionCardButtonLocalized checks the DingTalk ActionCard "View details"
// button (singleTitle) through the real Send path (fake receiver).
func TestDingTalkActionCardButtonLocalized(t *testing.T) {
	var singleTitle string
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(_ *testing.T, body map[string]any, _ *http.Request) {
		card, _ := body["actionCard"].(map[string]any)
		singleTitle, _ = card["singleTitle"].(string)
	})
	m := Message{Items: []Item{asciiStatusChangeItem()}}
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if singleTitle != "View details" {
		t.Errorf("DingTalk ActionCard singleTitle must be 'View details', got %q", singleTitle)
	}
}

// TestChinaPlatformValidateLocalized checks that the DingTalk/Feishu/WeCom config
// validation errors are English (the notification settings save and test APIs expose them
// to the user).
func TestChinaPlatformValidateLocalized(t *testing.T) {
	for _, kind := range []string{KindDingTalk, KindFeishu, KindWeCom} {
		ch, ok := Get(kind)
		if !ok {
			t.Fatalf("channel %s is not registered", kind)
		}

		// Empty config: "The webhook URL is missing"
		missing := ch.Validate(map[string]any{})
		if missing == nil {
			t.Fatalf("%s: an empty config must fail validation", kind)
		}
		assertNoUnsupportedScript(t, kind+" missing", missing.Error())
		if missing.Error() != "The webhook URL is missing" {
			t.Errorf("%s: the missing-field error must be 'The webhook URL is missing', got %q", kind, missing)
		}

		// Bad address (ftp): "The webhook URL is invalid: ..."
		bad := ch.Validate(map[string]any{"webhook": "ftp://x"})
		if bad == nil {
			t.Fatalf("%s: an ftp address must fail validation", kind)
		}
		assertNoUnsupportedScript(t, kind+" invalid", bad.Error())
		if !strings.Contains(bad.Error(), "The webhook URL is invalid") {
			t.Errorf("%s: the bad-address error must start with 'The webhook URL is invalid', got %q", kind, bad)
		}
	}
}

// TestChinaPlatformSendErrorsLocalized checks that DingTalk/Feishu/WeCom send failures are
// recorded in English in the delivery history (last_error). The platform names match the UI
// channel labels (DingTalk/Feishu/WeCom). Server errmsg values are ASCII, so any ideograph in
// the error means the surrounding wording regressed.
func TestChinaPlatformSendErrorsLocalized(t *testing.T) {
	// Business error code path (HTTP 200 plus errcode/code != 0 in the body).
	bizCases := []struct {
		kind     string
		resp     string
		wantSubs []string
	}{
		{KindDingTalk, `{"errcode":310000,"errmsg":"keyword not matched"}`, []string{"DingTalk returned an error", "310000"}},
		{KindFeishu, `{"code":19021,"msg":"sign error"}`, []string{"Feishu returned an error", "19021"}},
		{KindWeCom, `{"errcode":45009,"errmsg":"freq out of limit"}`, []string{"The WeCom request was rate limited", "45009"}},
		{KindWeCom, `{"errcode":93000,"errmsg":"invalid webhook"}`, []string{"WeCom returned an error", "93000"}},
	}
	for _, tc := range bizCases {
		srv := capturePost(t, tc.resp, nil)
		ch, ok := Get(tc.kind)
		if !ok {
			t.Fatalf("channel %s is not registered", tc.kind)
		}
		_, err := ch.Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
		if err == nil {
			t.Fatalf("%s: response %s must be an error", tc.kind, tc.resp)
		}
		assertNoUnsupportedScript(t, tc.kind+" send error", err.Error())
		for _, sub := range tc.wantSubs {
			if !strings.Contains(err.Error(), sub) {
				t.Errorf("%s: the send error must contain %q, got %q", tc.kind, sub, err)
			}
		}
	}

	// Parse-failure path when the response is not JSON: "Failed to parse the <platform> response".
	parseCases := []struct {
		kind     string
		platform string
	}{
		{KindDingTalk, "DingTalk"},
		{KindFeishu, "Feishu"},
		{KindWeCom, "WeCom"},
	}
	for _, pc := range parseCases {
		srv := capturePost(t, `not-json`, nil)
		ch, ok := Get(pc.kind)
		if !ok {
			t.Fatalf("channel %s is not registered", pc.kind)
		}
		_, err := ch.Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
		if err == nil {
			t.Fatalf("%s: a non-JSON response must be an error", pc.kind)
		}
		want := "Failed to parse the " + pc.platform + " response"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: the parse error must contain %q, got %q", pc.kind, want, err)
		}
	}
}
