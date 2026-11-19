package notify

import (
	"strings"
	"testing"
)

// TestTelegramItemMeta checks the source line that carries the task name and the
// finding id. Both are operator-facing identity fields: the task name says which run
// produced the alert, the numeric id is what an operator quotes in a report.
func TestTelegramItemMeta(t *testing.T) {
	Lang = func() string { return "en" }
	defer func() { Lang = nil }()

	got := telegramItemMeta(Item{TaskID: 3, TaskName: "Nintendo-Reconn-Phase-01", FindingID: 42}, " · ")
	if !strings.Contains(got, "Nintendo-Reconn-Phase-01") || !strings.Contains(got, "#42") {
		t.Fatalf("meta line must carry the task name and the finding id, got %q", got)
	}
	if strings.Contains(got, "&lt;") || strings.Contains(got, "&#") {
		t.Fatalf("plain text must not be double-escaped, got %q", got)
	}

	// An unknown task and a zero id must render nothing rather than an empty label.
	if m := telegramItemMeta(Item{}, " · "); m != "" {
		t.Fatalf("an item with no task and no id must render no meta line, got %q", m)
	}

	// A task name is HTML-escaped: it can come from user input.
	if m := telegramItemMeta(Item{TaskName: "a<b>&c", FindingID: 1}, " · "); !strings.Contains(m, "a&lt;b&gt;&amp;c") {
		t.Fatalf("task name must be escaped, got %q", m)
	}
}

// TestTelegramBodyShowsTaskAndFinding pins the fields into both render paths so a
// later refactor cannot silently drop them.
func TestTelegramBodyShowsTaskAndFinding(t *testing.T) {
	Lang = func() string { return "en" }
	defer func() { Lang = nil }()

	it := Item{
		FindingID: 7, TaskID: 3, TaskName: "Nintendo-Reconn-Phase-01",
		Name: "Reflected XSS", Severity: "high", Summary: "q reflects",
		Assets: []string{"a.nintendo.com"}, DetailURL: "https://p/f?id=7",
	}

	single, kept := telegramHTML(Message{Items: []Item{it}})
	if kept != 1 {
		t.Fatalf("single push must report 1, got %d", kept)
	}
	batch, _ := telegramHTML(Message{Batch: true, WindowMinutes: 15, Items: []Item{it}})

	for name, body := range map[string]string{"single": single, "digest": batch} {
		for _, want := range []string{"Nintendo-Reconn-Phase-01", "#7", "상세 보기"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body is missing %q:\n%s", name, want, body)
			}
		}
	}

	// The digest must separate entries with a blank line so long summaries stay
	// readable in the Telegram client.
	if !strings.Contains(batch, "\n\n<b>1. ") {
		t.Errorf("digest entry must start on a fresh block:\n%s", batch)
	}
}

// TestTelegramDigestEscapesItemText guards the digest path specifically: it now
// writes pre-escaped HTML per block, so a missing escape would let a finding title
// inject formatting.
func TestTelegramDigestEscapesItemText(t *testing.T) {
	Lang = func() string { return "en" }
	defer func() { Lang = nil }()

	m := Message{Batch: true, Items: []Item{{FindingID: 1, Name: "<b>owned</b>", Severity: "low", Summary: "a & b"}}}
	out, _ := telegramHTML(m)
	if strings.Contains(out, "<b>owned</b>") {
		t.Fatalf("finding name was not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;b&gt;owned&lt;/b&gt;") {
		t.Fatalf("finding name must be HTML-escaped:\n%s", out)
	}
}
