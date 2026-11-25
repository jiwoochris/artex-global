package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// asciiStatusChangeItem builds a status-change item with no Han characters (ASCII only).
// If a label reverts to Chinese, Han characters appear in the rendered output, so only when the
// data has zero Han does the "zero Han in the whole output" assertion pinpoint label regressions.
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
		t.Fatalf("카드 직렬화 실패: %v", err)
	}
	return string(b)
}

// TestFeishuItemLinesLocalized checks that the field labels (status change, type, assets, summary)
// in a single-item 飞书 (Feishu) card body are Korean and match markdown.go (F4②) exactly. feishu.go
// uses its own copy of these labels (not the function shared by the 3 markdown channels), so this
// pin is needed, separately from the shared-function check, to catch the Feishu body alone reverting to Chinese.
func TestFeishuItemLinesLocalized(t *testing.T) {
	got := feishuItemLines(asciiStatusChangeItem())
	assertKorean(t, "feishuItemLines", got)
	for _, want := range []string{
		"**상태 변경**: 처리 대기 → 수정됨",
		"**유형**: SQLi",
		"**자산**: a.example.com",
		"**개요**: SQL injection via q param",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("飞书 단건 본문에 %q 가 있어야 합니다:\n%s", want, got)
		}
	}
}

// TestFeishuCardButtonsLocalized checks that the 飞书 (Feishu) card button labels (view details, view all on
// the platform) are Korean and that the whole card has no Chinese Han characters (given ASCII data).
func TestFeishuCardButtonsLocalized(t *testing.T) {
	// Single item: the "상세 보기" (view details) button (when DetailURL is set)
	single, _ := feishuCard(Message{Items: []Item{asciiStatusChangeItem()}})
	singleJSON := mustJSON(t, single)
	if hasHan(singleJSON) {
		t.Errorf("단건 飞书 카드에 중국어 한자가 남았습니다:\n%s", singleJSON)
	}
	if !strings.Contains(singleJSON, "상세 보기") {
		t.Errorf("단건 飞书 카드에 '상세 보기' 버튼이 있어야 합니다:\n%s", singleJSON)
	}

	// Multiple items: the "플랫폼에서 전체 보기" (view all on the platform) button (when HomeURL is set)
	batch, _ := feishuCard(Message{Batch: true, HomeURL: "https://platform.example", Items: hanFreeItems(2)})
	batchJSON := mustJSON(t, batch)
	if hasHan(batchJSON) {
		t.Errorf("다건 飞书 카드에 중국어 한자가 남았습니다:\n%s", batchJSON)
	}
	if !strings.Contains(batchJSON, "플랫폼에서 전체 보기") {
		t.Errorf("다건 飞书 카드에 '플랫폼에서 전체 보기' 버튼이 있어야 합니다:\n%s", batchJSON)
	}
}

// TestDingTalkActionCardButtonLocalized checks, via the real Send path (fake receiver), that the
// 钉钉 (DingTalk) ActionCard "상세 보기" (view details) button (singleTitle) is Korean.
func TestDingTalkActionCardButtonLocalized(t *testing.T) {
	var singleTitle string
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(_ *testing.T, body map[string]any, _ *http.Request) {
		card, _ := body["actionCard"].(map[string]any)
		singleTitle, _ = card["singleTitle"].(string)
	})
	m := Message{Items: []Item{asciiStatusChangeItem()}}
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err != nil {
		t.Fatalf("투递 실패: %v", err)
	}
	if singleTitle != "상세 보기" {
		t.Errorf("钉钉 ActionCard singleTitle 은 '상세 보기' 여야 합니다, 받은 값 %q", singleTitle)
	}
}

// TestChinaPlatformValidateLocalized checks that the 钉钉/飞书/企业微信 (DingTalk/Feishu/WeCom) config validation
// errors are Korean and keep the word "Webhook" (the notification save/test APIs show them to users).
func TestChinaPlatformValidateLocalized(t *testing.T) {
	for _, kind := range []string{KindDingTalk, KindFeishu, KindWeCom} {
		ch, ok := Get(kind)
		if !ok {
			t.Fatalf("%s 채널이 등록되어 있지 않습니다", kind)
		}

		// Empty config: "Webhook 주소가 없습니다" ("no Webhook address")
		missing := ch.Validate(map[string]any{})
		if missing == nil {
			t.Fatalf("%s: 빈 설정은 검증에 실패해야 합니다", kind)
		}
		assertKorean(t, kind+" missing", missing.Error())
		if !strings.Contains(missing.Error(), "Webhook") || !strings.Contains(missing.Error(), "없습니다") {
			t.Errorf("%s: 누락 오류는 'Webhook 주소가 없습니다' 여야 합니다, 받은 값 %q", kind, missing)
		}

		// Invalid address (ftp): "Webhook 주소가 올바르지 않습니다: …" ("invalid Webhook address")
		bad := ch.Validate(map[string]any{"webhook": "ftp://x"})
		if bad == nil {
			t.Fatalf("%s: ftp 주소는 검증에 실패해야 합니다", kind)
		}
		if hasHan(bad.Error()) {
			t.Errorf("%s: 잘못된 주소 오류에 중국어 한자가 남았습니다: %q", kind, bad)
		}
		if !strings.Contains(bad.Error(), "Webhook 주소가 올바르지 않습니다") {
			t.Errorf("%s: 잘못된 주소 오류는 'Webhook 주소가 올바르지 않습니다' 로 시작해야 합니다, 받은 값 %q", kind, bad)
		}
	}
}

// TestChinaPlatformSendErrorsLocalized checks that 钉钉/飞书/企业微信 (DingTalk/Feishu/WeCom) send-failure
// reasons are recorded in Korean in the delivery history (last_error). Platform names match the UI
// channel labels (DingTalk/Feishu/WeCom). The server errmsg is ASCII, so Han characters in the error
// mean the skeleton wording has regressed to Chinese.
func TestChinaPlatformSendErrorsLocalized(t *testing.T) {
	// Business error code path (HTTP 200 + errcode/code != 0 in the body).
	bizCases := []struct {
		kind     string
		resp     string
		wantSubs []string
	}{
		{KindDingTalk, `{"errcode":310000,"errmsg":"keyword not matched"}`, []string{"DingTalk에서 오류가 발생했습니다", "310000"}},
		{KindFeishu, `{"code":19021,"msg":"sign error"}`, []string{"Feishu에서 오류가 발생했습니다", "19021"}},
		{KindWeCom, `{"errcode":45009,"errmsg":"freq out of limit"}`, []string{"WeCom 요청이 제한되었습니다", "45009"}},
		{KindWeCom, `{"errcode":93000,"errmsg":"invalid webhook"}`, []string{"WeCom에서 오류가 발생했습니다", "93000"}},
	}
	for _, tc := range bizCases {
		srv := capturePost(t, tc.resp, nil)
		ch, ok := Get(tc.kind)
		if !ok {
			t.Fatalf("%s 채널이 등록되어 있지 않습니다", tc.kind)
		}
		_, err := ch.Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
		if err == nil {
			t.Fatalf("%s: %s 응답은 오류여야 합니다", tc.kind, tc.resp)
		}
		if hasHan(err.Error()) {
			t.Errorf("%s: 발송 오류에 중국어 한자가 남았습니다: %q", tc.kind, err)
		}
		for _, sub := range tc.wantSubs {
			if !strings.Contains(err.Error(), sub) {
				t.Errorf("%s: 발송 오류에 %q 가 있어야 합니다, 받은 값 %q", tc.kind, sub, err)
			}
		}
	}

	// Parse-failure path when the response is not JSON: "<플랫폼> 응답을 해석하지 못했습니다" ("could not parse the <platform> response").
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
			t.Fatalf("%s 채널이 등록되어 있지 않습니다", pc.kind)
		}
		_, err := ch.Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
		if err == nil {
			t.Fatalf("%s: JSON 이 아닌 응답은 오류여야 합니다", pc.kind)
		}
		want := pc.platform + " 응답을 해석하지 못했습니다"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: 파싱 실패 오류에 %q 가 있어야 합니다, 받은 값 %q", pc.kind, want, err)
		}
	}
}
