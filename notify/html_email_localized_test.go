package notify

import (
	"strings"
	"testing"
)

// This file guards F4 ③ (email body, HTML template) localization against regressions.
// It catches it when the labels in html.go or the validation errors in email.go revert to Chinese.
// The helpers hasHan, hasHangul and assertNoUnsupportedScript live in notify_localized_test.go (same package).
//
// The data (title, type, assets, summary) is all ASCII. Only then does the "zero Han
// in the whole output" assertion catch skeleton-label regressions precisely rather than content. Severity and status
// labels have been English since F4 ①, so they come out in English (not Han) and pass the assertion.

// TestHTMLItemLabelsLocalized turns on every label branch of the single-item email body,
// then checks that the skeleton is English and contains no Chinese Han characters.
func TestHTMLItemLabelsLocalized(t *testing.T) {
	m := Message{
		HomeURL: "https://example.com/panel",
		Items: []Item{{
			Name:       "sqli-login", // Title() = Name
			VulnClass:  "injection",  // must differ from Title() so the type line renders
			Severity:   "high",       // SeverityLabel → High
			Summary:    "login form is injectable",
			Assets:     []string{"host-a.example.com"},
			DetailURL:  "https://example.com/f/1",
			FromStatus: "pending", // IsStatusChange()=true → status change line
			ToStatus:   "fixed",
		}},
	}
	out := htmlBody(m, 0)
	if hasHan(out) {
		t.Errorf("the email body still contains Chinese Han characters:\n%s", out)
	}
	for _, want := range []string{
		"Status change", "Type", "Assets", "Summary", "View details", "View everything in the platform",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the email body is missing the %q label:\n%s", want, out)
		}
	}
}

// TestHTMLBatchIntroLocalized checks that both branches (with and without a time window)
// of the batch (digest) header render in English.
func TestHTMLBatchIntroLocalized(t *testing.T) {
	items := []Item{{Name: "a", Severity: "high"}, {Name: "b", Severity: "low"}}

	withWindow := htmlBatchIntro(Message{Batch: true, WindowMinutes: 30, Items: items})
	assertNoUnsupportedScript(t, "htmlBatchIntro(with window)", withWindow)
	for _, want := range []string{"in the last 30 minutes", "new vulnerabilities", "2 new vulnerabilities"} {
		if !strings.Contains(withWindow, want) {
			t.Errorf("the windowed header is missing %q: %q", want, withWindow)
		}
	}

	noWindow := htmlBatchIntro(Message{Batch: true, WindowMinutes: 0, Items: items})
	assertNoUnsupportedScript(t, "htmlBatchIntro(no window)", noWindow)
	if !strings.Contains(noWindow, "2 new vulnerabilities") {
		t.Errorf("the header without a time window must contain %q: %q", "2 new vulnerabilities", noWindow)
	}
	if strings.Contains(noWindow, "minutes") {
		t.Errorf("there is no time window but 'minutes' is present: %q", noWindow)
	}
}

// TestEmailValidateLocalized checks that the four SMTP config validation errors are in
// English and that each one says which field is the problem. These messages are shown
// verbatim to the user configuring the notification channel.
func TestEmailValidateLocalized(t *testing.T) {
	cases := []struct {
		name   string
		cfg    map[string]any
		substr string
	}{
		{"server address missing", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}, "SMTP"},
		{"port out of range", map[string]any{"host": "h"}, "port"},
		{"sender missing", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}, "Sender"},
		{"recipient missing", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}, "recipient"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (emailChannel{}).Validate(tc.cfg)
			if err == nil {
				t.Fatalf("validation must fail: %v", tc.cfg)
			}
			assertNoUnsupportedScript(t, "Validate("+tc.name+")", err.Error())
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("the error message must contain %q, got %q", tc.substr, err.Error())
			}
		})
	}
}
