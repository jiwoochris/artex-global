package notify

import (
	"errors"
	"strings"
	"testing"
)

// F4 ⑥: pins that the user-facing text in notify/mask.go and filter.go (the validation errors that
// server/notify_api.go returns verbatim via writeErr(400) when a notification channel config is saved
// or updated) is Korean with no Han characters. channel.go is out of scope because it has no
// user-facing text (only comments). hasHan / hasHangul / assertKorean are defined in notify_localized_test.go.

// TestFilterValidateLocalized checks the validation error for an invalid min_severity.
// The error is returned as a 400 on the save/update paths (server/notify_api.go:264, 369).
func TestFilterValidateLocalized(t *testing.T) {
	err := Filter{MinSeverity: "hgih"}.Validate()
	if err == nil {
		t.Fatal("잘못된 최소 심각도인데 오류가 없습니다")
	}
	assertKorean(t, "Filter.Validate", err.Error())
	// low/medium/high/critical are config enum values, so they must stay verbatim.
	for _, tok := range []string{"low", "medium", "high", "critical"} {
		if !strings.Contains(err.Error(), tok) {
			t.Errorf("심각도 토큰 %q 가 사라졌습니다: %q", tok, err.Error())
		}
	}
	// Valid values and the empty value must pass.
	if err := (Filter{MinSeverity: "high"}).Validate(); err != nil {
		t.Errorf("high 는 유효한데 오류가 났습니다: %v", err)
	}
	if err := (Filter{}).Validate(); err != nil {
		t.Errorf("빈 최소 심각도는 유효한데 오류가 났습니다: %v", err)
	}
}

// TestPrepareConfigUpdateUnknownKindLocalized checks the error when updating a config with an
// unregistered channel kind (a 400 at server/notify_api.go:326).
func TestPrepareConfigUpdateUnknownKindLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("definitely-not-a-channel", map[string]any{}, map[string]any{})
	if err == nil {
		t.Fatal("미등록 채널 유형인데 오류가 없습니다")
	}
	assertKorean(t, "PrepareConfigUpdate unknown kind", err.Error())
}

// TestDestinationChangedErrorLocalized triggers ErrDestinationChangedWithoutCredentials via the real
// path: a PATCH that changes only the webhook's destination address (url) to a new value and leaves
// the credentials (headers) untouched. The server answers it with a 400.
func TestDestinationChangedErrorLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("webhook",
		map[string]any{
			"url":     "https://old.example.com/hook",
			"headers": map[string]any{"Authorization": "Bearer real-token"},
		},
		map[string]any{"url": "https://attacker.example.com/hook"},
	)
	if err == nil {
		t.Fatal("대상 주소를 바꾸고 자격 증명을 표태하지 않았는데 오류가 없습니다")
	}
	var de *ErrDestinationChangedWithoutCredentials
	if !errors.As(err, &de) {
		t.Fatalf("예상한 오류 유형이 아닙니다: %T", err)
	}
	assertKorean(t, "ErrDestinationChangedWithoutCredentials", err.Error())
	// The changed destination key and missing credential key names are config field names, so they must appear verbatim in the message.
	if !strings.Contains(err.Error(), "url") || !strings.Contains(err.Error(), "headers") {
		t.Errorf("필드 키 이름이 누락됐습니다: %q", err.Error())
	}
}

// TestRejectMaskedInContainersLocalized triggers, via the real path, the rejection of a masking
// sentinel inside a structure: a PATCH that puts the masking sentinel value inside headers (an object that is also a credential field).
func TestRejectMaskedInContainersLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("webhook",
		map[string]any{},
		map[string]any{"headers": map[string]any{"Authorization": MaskedPrefix + ":…abc123"}},
	)
	if err == nil {
		t.Fatal("구조체 내부에 마스킹 센티넬을 끼워 넣었는데 오류가 없습니다")
	}
	assertKorean(t, "rejectMaskedInContainers", err.Error())
	// The sentinel literal (__masked__) is shown in the message so the operator can tell which value is the problem.
	if !strings.Contains(err.Error(), MaskedPrefix) {
		t.Errorf("마스킹 센티넬이 메시지에 없습니다: %q", err.Error())
	}
}
