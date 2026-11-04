package server

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/yukiteruamano/artex/db"
)

// Regression guard for the notification-settings API response wording in notify_api.go: every
// message is its exact English source text (assertEnglishMessage).

// TestNotifyErrorConstantsLocalized asserts that all ten response constants and the three test
// message constants are their exact English source text. Reverting any of them to Korean or
// Chinese, or rewording one, fails this test.
func TestNotifyErrorConstantsLocalized(t *testing.T) {
	cases := []struct{ label, msg, wantEnglish string }{
		{"notifyErrBadJSON", notifyErrBadJSON, "The request body is not valid JSON: "},
		{"notifyErrKindInvalidFmt", fmt.Sprintf(notifyErrKindInvalidFmt, "email / webhook"),
			"Invalid channel type. Possible values: email / webhook"},
		{"notifyErrNameMissing", notifyErrNameMissing, "Enter a channel name"},
		{"notifyErrNameEmpty", notifyErrNameEmpty, "The channel name must not be empty"},
		{"notifyErrModeInvalid", notifyErrModeInvalid, "Invalid delivery mode. Possible values: realtime / digest"},
		{"notifyErrRateNegative", notifyErrRateNegative, "The rate limit must not be negative"},
		{"notifyErrChannelID", notifyErrChannelID, "Invalid channel id"},
		{"notifyErrKindUnregFmt", fmt.Sprintf(notifyErrKindUnregFmt, "bogus"),
			`Channel type "bogus" is not registered`},
		{"notifyErrDeliveryID", notifyErrDeliveryID, "Invalid delivery id"},
		{"notifyErrChannelNotFound", notifyErrChannelNotFound, "Notification channel not found"},
		{"notifyTestName", notifyTestName, "Test message · channel configuration is healthy"},
		{"notifyTestClass", notifyTestClass, "Connection test"},
		{"notifyTestSummary", notifyTestSummary,
			"This is an ARTEX notification channel test message. If you received it, your channel configuration is working."},
	}
	for _, c := range cases {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestNotifyChannelLookupErrLocalized exercises the "no channel → 404" path through the real code.
// notifyChannelLookupErr is a package function that runs with only w and err (no Server/DB), so
// passing the sentinel error through it proves without a DB that the 404 body carries the English
// constant.
func TestNotifyChannelLookupErrLocalized(t *testing.T) {
	rec := httptest.NewRecorder()
	notifyChannelLookupErr(rec, db.ErrNotificationChannelNotFound)
	if rec.Code != 404 {
		t.Fatalf("status code = %d, want = 404 (body %q)", rec.Code, rec.Body.String())
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	if got != notifyErrChannelNotFound {
		t.Fatalf("response message = %q, want = %q", got, notifyErrChannelNotFound)
	}
	assertEnglishMessage(t, "channel_not_found", got, "Notification channel not found")
}

// TestNotifyTestMessageLocalized assembles the channel connectivity test message with pure
// functions and checks that the body sent to the user (title, class, summary) is English. Needs no
// DB or network.
func TestNotifyTestMessageLocalized(t *testing.T) {
	const base = "https://artex.example.test"
	msg := notifyTestMessage(base)
	if len(msg.Items) != 1 {
		t.Fatalf("test message item count = %d, want = 1", len(msg.Items))
	}
	item := msg.Items[0]
	assertEnglishMessage(t, "test.name", item.Name, "Test message · channel configuration is healthy")
	assertEnglishMessage(t, "test.class", item.VulnClass, "Connection test")
	assertEnglishMessage(t, "test.summary", item.Summary,
		"This is an ARTEX notification channel test message. If you received it, your channel configuration is working.")
	if msg.HomeURL != base || item.DetailURL != base {
		t.Fatalf("link preservation failed: HomeURL=%q DetailURL=%q (want %q)", msg.HomeURL, item.DetailURL, base)
	}
}
