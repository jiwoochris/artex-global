package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// This file guards the English-only contract of the Telegram and generic webhook
// channels and of the shared HTTP transport layer against regression: the Telegram
// message labels and digest title, the webhook config validation errors, the
// transport errors, and validateHTTPURL, which five channels share.
//
// Data (title, type, assets, summary) is kept ASCII so the "no Han ideograph in the
// whole output" assertion catches skeleton label regressions rather than content.
// Severity and status labels come from the en/es overlay and render as Latin text.

// TestTelegramItemLabelsLocalized turns on every label branch of a single Telegram
// message and checks the skeleton is English with no Han ideographs left.
func TestTelegramItemLabelsLocalized(t *testing.T) {
	m := Message{
		HomeURL: "https://example.com/panel",
		Items: []Item{{
			Name:       "sqli-login", // Title() = Name
			VulnClass:  "injection",  // must differ from Title() or the type line is skipped
			Severity:   "high",       // SeverityLabel -> High
			Summary:    "login form is injectable",
			Assets:     []string{"host-a.example.com"},
			DetailURL:  "https://example.com/f/1",
			FromStatus: "pending", // IsStatusChange()=true -> status change line
			ToStatus:   "fixed",
		}},
	}
	out, kept := telegramHTML(m)
	if kept != 1 {
		t.Fatalf("a single-item message must report 1, got %d", kept)
	}
	if hasHan(out) {
		t.Errorf("Han ideographs remain in the Telegram body:\n%s", out)
	}
	for _, want := range []string{"Status change", "Type", "Assets", "Summary", "View details"} {
		if !strings.Contains(out, want) {
			t.Errorf("the Telegram body is missing the %q label:\n%s", want, out)
		}
	}
}

// TestTelegramBatchTitleLocalized checks the overflow and time-window branches of
// the digest title. The wording must match the Markdown and email renderers so the
// channels do not drift apart.
func TestTelegramBatchTitleLocalized(t *testing.T) {
	items := []Item{{Name: "a", Severity: "high"}, {Name: "b", Severity: "low"}}

	// Overflow plus a window: digest heading, total, first N, remainder, next
	// message, last N minutes.
	over := telegramBatchTitle(Message{WindowMinutes: 30}, items, 5)
	assertNoUnsupportedScript(t, "telegramBatchTitle(overflow)", over)
	for _, want := range []string{"Vulnerability digest", "5 total", "showing the first 2", "the remaining 3", "next message", "Last 30 minutes"} {
		if !strings.Contains(over, want) {
			t.Errorf("the overflow title is missing %q: %q", want, over)
		}
	}

	// No overflow and no window: there must be no "next message" hint.
	full := telegramBatchTitle(Message{}, items, 2)
	assertNoUnsupportedScript(t, "telegramBatchTitle(full)", full)
	if !strings.Contains(full, "Vulnerability digest") || !strings.Contains(full, "2 total") {
		t.Errorf("the full title must contain 'Vulnerability digest' and '2 total': %q", full)
	}
	if strings.Contains(full, "next message") {
		t.Errorf("a title without overflow must not carry the 'next message' hint: %q", full)
	}
}

// TestTelegramValidateLocalized checks the missing-required-field errors are English
// and name the field that is empty. The technical terms bot token and chat ID are
// kept verbatim.
func TestTelegramValidateLocalized(t *testing.T) {
	if err := (telegramChannel{}).Validate(map[string]any{}); err == nil {
		t.Fatal("a missing bot_token must fail validation")
	} else {
		assertNoUnsupportedScript(t, "telegram Validate(bot_token)", err.Error())
		if !strings.Contains(err.Error(), "bot token") {
			t.Errorf("the error must name the missing bot token: %q", err.Error())
		}
	}
	if err := (telegramChannel{}).Validate(map[string]any{"bot_token": "t"}); err == nil {
		t.Fatal("a missing chat_id must fail validation")
	} else {
		assertNoUnsupportedScript(t, "telegram Validate(chat_id)", err.Error())
		if !strings.Contains(err.Error(), "chat ID") {
			t.Errorf("the error must name the missing chat ID: %q", err.Error())
		}
	}
}

// TestWebhookValidateLocalized checks the three generic webhook config validation
// errors are English and each one names the cause of the problem.
func TestWebhookValidateLocalized(t *testing.T) {
	if err := (webhookChannel{}).Validate(map[string]any{}); err == nil {
		t.Fatal("a missing url must fail validation")
	} else {
		assertNoUnsupportedScript(t, "webhook Validate(url)", err.Error())
		if !strings.Contains(err.Error(), "target URL") {
			t.Errorf("the error must name the missing target URL: %q", err.Error())
		}
	}
	// Unsupported method: English plus the allowed list.
	if err := (webhookChannel{}).Validate(map[string]any{"url": "https://example.com/hook", "method": "DELETE"}); err == nil {
		t.Fatal("DELETE must fail validation")
	} else {
		assertNoUnsupportedScript(t, "webhook Validate(method)", err.Error())
		if !strings.Contains(err.Error(), "GET/POST/PUT/PATCH") {
			t.Errorf("the error must list the allowed methods: %q", err.Error())
		}
	}
	// Template syntax error: English (the wrapped cause is a Go template error, which
	// carries no Han ideographs).
	if err := (webhookChannel{}).Validate(map[string]any{"url": "https://example.com/hook", "body_template": "{{"}); err == nil {
		t.Fatal("a broken template must fail validation")
	} else {
		assertNoUnsupportedScript(t, "webhook Validate(template)", err.Error())
		if !strings.Contains(err.Error(), "template") {
			t.Errorf("the error must say the template is at fault: %q", err.Error())
		}
	}
}

// TestValidateHTTPURLLocalized checks the URL validation helper shared by the five
// channels (telegram, webhook, dingtalk, feishu, wecom) reports errors in English.
// A half-translated helper would mix languages inside one message.
func TestValidateHTTPURLLocalized(t *testing.T) {
	// Unsupported scheme.
	if err := validateHTTPURL("ftp://example.com/x"); err == nil {
		t.Fatal("the ftp scheme must be rejected")
	} else {
		assertNoUnsupportedScript(t, "validateHTTPURL(scheme)", err.Error())
		if !strings.Contains(err.Error(), "http") {
			t.Errorf("the error must name the supported schemes (http/https): %q", err.Error())
		}
	}
	// Missing host name.
	if err := validateHTTPURL("http://"); err == nil {
		t.Fatal("an address without a host must be rejected")
	} else {
		assertNoUnsupportedScript(t, "validateHTTPURL(host)", err.Error())
	}
}

// TestHTTPLocalTargetBlockedLocalized checks the local/link-local address block error
// is English and points at the environment variable that lifts it.
func TestHTTPLocalTargetBlockedLocalized(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "") // explicitly off to take the blocking path
	err := blockInternalDial("tcp", "127.0.0.1:25", nil)
	if err == nil {
		t.Fatal("a local address must be blocked by default")
	}
	assertNoUnsupportedScript(t, "blockInternalDial", err.Error())
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("the block error must point at the escape hatch (%s): %q", AllowLocalTargetsEnv, err.Error())
	}
}

// TestHTTPRedactHelpersLocalized checks the redaction placeholders and the "unknown
// error" branch of the transport error reducer are English.
func TestHTTPRedactHelpersLocalized(t *testing.T) {
	if got := redactRequestTarget(""); !strings.Contains(got, "could not be parsed") {
		t.Errorf("an unparsable address must yield the English placeholder, got %q", got)
	}
	// The branch where *url.Error's Err is nil.
	got := redactTransportError(&url.Error{Op: "Get", URL: "http://api.example", Err: nil})
	if hasHan(got) {
		t.Errorf("Han ideographs remain in the reduced transport error: %q", got)
	}
	if !strings.Contains(got, "unknown error") {
		t.Errorf("a nil Err must reduce to 'unknown error', got %q", got)
	}
}

// TestHTTPStatusErrorsLocalized drives a real doJSON round trip to check the
// status-code classification (rejected/server error/rate limited) surfaces English
// user-visible errors. Sending to a 127.0.0.1 httptest server is blocked by default,
// so it is explicitly allowed for this test only.
func TestHTTPStatusErrorsLocalized(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "1")
	cases := []struct {
		code int
		want string
	}{
		{http.StatusForbidden, "rejected the request"},
		{http.StatusInternalServerError, "returned an error"},
		{http.StatusTooManyRequests, "rate limited the request"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte("body"))
		}))
		_, err := doJSON(context.Background(), http.MethodPost, srv.URL, nil, map[string]any{"a": 1})
		srv.Close()
		if err == nil {
			t.Fatalf("HTTP %d must be an error", tc.code)
		}
		assertNoUnsupportedScript(t, "doJSON(HTTP status)", err.Error())
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("the HTTP %d error must contain %q, got %q", tc.code, tc.want, err.Error())
		}
	}
}
