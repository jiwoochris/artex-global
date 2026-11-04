package notify

import (
	"strings"
	"testing"
)

// hasHan reports whether s contains a CJK Han ideograph (Chinese). Only English and
// Spanish are supported, so Han must not appear in user-visible strings.
func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// hasHangul reports whether s contains a Hangul syllable (Korean). Korean is not a
// supported language any more, so it must not appear in user-visible strings.
func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xAC00 && r <= 0xD7A3 {
			return true
		}
	}
	return false
}

// assertNoUnsupportedScript rejects Hangul/Han in output, so a stale Korean or
// Chinese translation cannot slip back in.
func assertNoUnsupportedScript(t *testing.T, where, got string) {
	t.Helper()
	if hasHan(got) {
		t.Errorf("%s: Han ideographs remain (Chinese is unsupported): %q", where, got)
	}
	if hasHangul(got) {
		t.Errorf("%s: Hangul remains (Korean is unsupported): %q", where, got)
	}
}

// TestSeverityLabelLocalized pins the severity labels: English is the source
// language and Spanish the only translation. The labels are shared by every channel
// (telegram, email, html, markdown, webhook, feishu, dingtalk, wecom), so one stale
// entry would mix languages across all messages.
func TestSeverityLabelLocalized(t *testing.T) {
	want := map[string]map[string]string{
		"en": {"critical": "Critical", "high": "High", "medium": "Medium", "low": "Low"},
		"es": {"critical": "Crítico", "high": "Alto", "medium": "Medio", "low": "Bajo"},
	}
	t.Cleanup(func() { Lang = nil })
	for lang, labels := range want {
		Lang = func() string { return lang }
		for sev, label := range labels {
			got := SeverityLabel(sev)
			assertNoUnsupportedScript(t, "SeverityLabel("+lang+","+sev+")", got)
			if !strings.Contains(got, label) {
				t.Errorf("SeverityLabel(%q) in %s = %q, want it to contain %q", sev, lang, got, label)
			}
		}
	}
	Lang = nil
	// An unknown severity is echoed back unchanged (no invented label).
	if got := SeverityLabel("made_up"); got != "made_up" {
		t.Errorf("an unknown severity must be echoed unchanged, got %q", got)
	}
}

// TestStatusLabelLocalized pins all nine triage status labels in both supported
// languages, so a status-change message and the UI status namespace keep one wording.
func TestStatusLabelLocalized(t *testing.T) {
	want := map[string]map[string]string{
		"en": {
			"pending":        "Pending",
			"in_progress":    "In progress",
			"confirmed":      "Confirmed",
			"resolved":       "Resolved",
			"fixed":          "Fixed",
			"false_positive": "False positive",
			"ignored":        "Ignored",
			"duplicate":      "Duplicate",
			"risk_accepted":  "Risk accepted",
		},
		"es": {
			"pending":        "Pendiente",
			"in_progress":    "En curso",
			"confirmed":      "Confirmado",
			"resolved":       "Resuelto",
			"fixed":          "Corregido",
			"false_positive": "Falso positivo",
			"ignored":        "Ignorado",
			"duplicate":      "Duplicado",
			"risk_accepted":  "Riesgo aceptado",
		},
	}
	t.Cleanup(func() { Lang = nil })
	for lang, labels := range want {
		Lang = func() string { return lang }
		for status, label := range labels {
			got := StatusLabel(status)
			assertNoUnsupportedScript(t, "StatusLabel("+lang+","+status+")", got)
			if got != label {
				t.Errorf("StatusLabel(%q) in %s = %q, want %q", status, lang, got, label)
			}
		}
	}
	Lang = nil
	// An unknown status is echoed unchanged (no invented label).
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Errorf("an unknown status must be echoed unchanged, got %q", got)
	}
}

// TestItemTitlePlaceholderLocalized checks an item with neither name nor type falls
// back to an English placeholder. A title is never empty.
func TestItemTitlePlaceholderLocalized(t *testing.T) {
	got := Item{}.Title()
	assertNoUnsupportedScript(t, "Item{}.Title()", got)
	if got != "(unnamed vulnerability)" {
		t.Errorf("the fallback title must be %q, got %q", "(unnamed vulnerability)", got)
	}
	// Name first, then type (fallback order must not regress).
	if n := (Item{Name: "login SQLi"}).Title(); n != "login SQLi" {
		t.Errorf("name precedence broke, got %q", n)
	}
	if v := (Item{VulnClass: "XSS"}).Title(); v != "XSS" {
		t.Errorf("type fallback broke, got %q", v)
	}
}

// TestAssetLineLocalized checks the asset list separator and the overflow note. The
// Chinese punctuation that used to be here must not come back: it would mix scripts
// inside a push body.
func TestAssetLineLocalized(t *testing.T) {
	// At or under the limit: everything listed, ASCII comma separator.
	if got := assetLine([]string{"a.example.com", "b.example.com"}, 3); got != "a.example.com, b.example.com" {
		t.Errorf("the separator must be an ASCII comma, got %q", got)
	}
	// Over the limit: the first limit entries plus an English "more" counter.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if hasHan(got) {
		t.Errorf("Han ideographs remain in the asset list: %q", got)
	}
	if got != "a, b +3 more" {
		t.Errorf("the overflow note must read %q, got %q", "a, b +3 more", got)
	}
}
