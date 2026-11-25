package server

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Regression guard test that keeps the response text and test message of the notification
// settings API in notify_api.go in Korean. The Korean check reuses assertKoreanError
// (contains Hangul · zero Chinese Han), built in F3a.

// TestNotifyErrorConstantsLocalized asserts that all 10 response constants and 3 test message
// constants are Korean. This test fails if any one of them is reverted to Chinese.
func TestNotifyErrorConstantsLocalized(t *testing.T) {
	cases := map[string]string{
		"notifyErrBadJSON":         notifyErrBadJSON,
		"notifyErrKindInvalidFmt":  fmt.Sprintf(notifyErrKindInvalidFmt, "email / webhook"),
		"notifyErrNameMissing":     notifyErrNameMissing,
		"notifyErrNameEmpty":       notifyErrNameEmpty,
		"notifyErrModeInvalid":     notifyErrModeInvalid,
		"notifyErrRateNegative":    notifyErrRateNegative,
		"notifyErrChannelID":       notifyErrChannelID,
		"notifyErrKindUnregFmt":    fmt.Sprintf(notifyErrKindUnregFmt, "bogus"),
		"notifyErrDeliveryID":      notifyErrDeliveryID,
		"notifyErrChannelNotFound": notifyErrChannelNotFound,
		"notifyTestName":           notifyTestName,
		"notifyTestClass":          notifyTestClass,
		"notifyTestSummary":        notifyTestSummary,
	}
	for label, msg := range cases {
		assertKoreanError(t, label, msg)
	}
}

// TestNotifyChannelLookupErrLocalized checks the "channel not found → 404" path with the real
// code. notifyChannelLookupErr is a package function that runs with only w and err, without a
// Server/DB, so passing the sentinel error straight through confirms, without a DB, that the
// Korean constant ends up in the 404 response body.
func TestNotifyChannelLookupErrLocalized(t *testing.T) {
	rec := httptest.NewRecorder()
	notifyChannelLookupErr(rec, db.ErrNotificationChannelNotFound)
	if rec.Code != 404 {
		t.Fatalf("상태 코드 = %d, 기대 = 404 (본문 %q)", rec.Code, rec.Body.String())
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	if got != notifyErrChannelNotFound {
		t.Fatalf("응답 문구 = %q, 기대 = %q", got, notifyErrChannelNotFound)
	}
	assertKoreanError(t, "channel_not_found", got)
}

// TestNotifyTestMessageLocalized assembles the channel connection-check test message with a
// pure function and checks that the body sent to the user (title · category · summary) is
// Korean. No DB · network needed.
func TestNotifyTestMessageLocalized(t *testing.T) {
	const base = "https://artex.example.test"
	msg := notifyTestMessage(base)
	if len(msg.Items) != 1 {
		t.Fatalf("테스트 메시지 항목 수 = %d, 기대 = 1", len(msg.Items))
	}
	item := msg.Items[0]
	assertKoreanError(t, "test.name", item.Name)
	assertKoreanError(t, "test.class", item.VulnClass)
	assertKoreanError(t, "test.summary", item.Summary)
	if msg.HomeURL != base || item.DetailURL != base {
		t.Fatalf("링크 보존 실패: HomeURL=%q DetailURL=%q (기대 %q)", msg.HomeURL, item.DetailURL, base)
	}
}
