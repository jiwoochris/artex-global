package notify

import (
	"strings"
	"testing"
)

// hanFreeItems builds only items with no CJK Han characters (ASCII title, type, summary).
// If a label regresses to Chinese, Han characters appear throughout the rendered output,
// so only with zero Han in the data does the "zero Han in the whole output" assertion pin label regressions precisely.
func hanFreeItems(n int) []Item {
	items := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, Item{
			FindingID: int64(i + 1),
			Name:      "login-flaw",
			VulnClass: "SQLi",
			Severity:  "high",
			Summary:   "SQL injection via q param",
			Assets:    []string{"a.example.com"},
		})
	}
	return items
}

// TestMarkdownTitleLocalized checks that markdownTitle — used by the markdown-family
// channels (dingtalk·wecom) and by feishu·html·telegram·webhook, which share the title —
// is English. If even one of them regresses to Chinese, all five channels' titles mix languages.
func TestMarkdownTitleLocalized(t *testing.T) {
	// Batch (digest): "Vulnerability digest · N total"
	got := markdownTitle(Message{Batch: true, Items: hanFreeItems(3)})
	assertNoUnsupportedScript(t, "markdownTitle(batch)", got)
	if !strings.Contains(got, "Vulnerability digest") || !strings.Contains(got, "3 total") {
		t.Errorf("the batch title must read 'Vulnerability digest · 3 total', got %q", got)
	}
	// No items: "Vulnerability alert"
	empty := markdownTitle(Message{})
	assertNoUnsupportedScript(t, "markdownTitle(empty)", empty)
	if empty != "Vulnerability alert" {
		t.Errorf("the empty-message title must be 'Vulnerability alert', got %q", empty)
	}
}

// TestMarkdownBatchIntroLocalized checks that the digest header (time window, count,
// overflow note) is English. The item data has zero Han characters, so any Han in the
// output means the header wording regressed to Chinese.
func TestMarkdownBatchIntroLocalized(t *testing.T) {
	items := hanFreeItems(3)

	// With a time window: "N new vulnerabilities in the last N minutes"
	win := markdownBatchIntro(Message{Batch: true, WindowMinutes: 30}, items, 3)
	if hasHan(win) {
		t.Errorf("the windowed header still contains Chinese Han characters: %q", win)
	}
	if !strings.Contains(win, "in the last 30 minutes") || !strings.Contains(win, "3 new vulnerabilities") {
		t.Errorf("the windowed header must read '3 new vulnerabilities in the last 30 minutes', got %q", win)
	}

	// Without a time window: "N new vulnerabilities" (no minutes wording)
	noWin := markdownBatchIntro(Message{Batch: true}, items, 3)
	if hasHan(noWin) {
		t.Errorf("the header still contains Chinese Han characters: %q", noWin)
	}
	if !strings.Contains(noWin, "3 new vulnerabilities") || strings.Contains(noWin, "minutes") {
		t.Errorf("a header without a time window must read '3 new vulnerabilities' (no minutes wording), got %q", noWin)
	}

	// When only part fits: "(this message shows only the first N … the remaining N follow in the next message …)"
	trunc := markdownBatchIntro(Message{Batch: true, WindowMinutes: 30}, items, 5)
	if hasHan(trunc) {
		t.Errorf("the overflow note still contains Chinese Han characters: %q", trunc)
	}
	for _, want := range []string{"only the first 3", "the remaining 2", "in the next message"} {
		if !strings.Contains(trunc, want) {
			t.Errorf("the overflow note must contain %q, got %q", want, trunc)
		}
	}
}

// TestWriteItemLabelsLocalized checks that the field labels of the single-item detail
// render (the path shared by the 3 markdown channels) — status change, type, assets, summary — and the detail link are English.
func TestWriteItemLabelsLocalized(t *testing.T) {
	it := Item{
		Name:       "login-flaw",
		VulnClass:  "SQLi",
		Severity:   "high",
		Summary:    "SQL injection via q param",
		Assets:     []string{"a.example.com"},
		DetailURL:  "https://platform.example/finding/1",
		FromStatus: "pending",
		ToStatus:   "fixed",
	}
	var b strings.Builder
	writeItem(&b, it, "", true)
	got := b.String()

	assertNoUnsupportedScript(t, "writeItem(single)", got)
	for _, want := range []string{
		"**Status change**: Pending → Fixed",
		"**Type**: SQLi",
		"**Assets**: a.example.com",
		"**Summary**: SQL injection via q param",
		"[View details](https://platform.example/finding/1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the single-item render must contain %q:\n%s", want, got)
		}
	}
}

// TestMarkdownBodyFooterLocalized checks that the platform entry link at the end of a
// digest body is English (it is appended only when HomeURL is set).
func TestMarkdownBodyFooterLocalized(t *testing.T) {
	m := Message{Batch: true, HomeURL: "https://platform.example", Items: hanFreeItems(2)}
	body, kept := markdownBody(m, 0)
	if kept != 2 {
		t.Fatalf("with no limit (0) both items must be included, got %d", kept)
	}
	if !strings.Contains(body, "[View everything in the platform](https://platform.example)") {
		t.Errorf("the end of the body must contain the 'View everything in the platform' link:\n%s", body)
	}
}
