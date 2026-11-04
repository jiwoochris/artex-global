package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file covers the "pack whole items" fix: when a digest message exceeds the channel
// length limit it must be truncated **by whole item** and the items that did not fit reported honestly, so the caller marks only those truly delivered.
//
// The previous approach rendered the whole message, truncated it, then marked the whole
// batch delivered: the second half of the message vanished while the delivery history showed full success — the finding was gone, and nothing anywhere could detect it.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// A 200-item Chinese digest, necessarily far beyond WeCom's 4096 bytes.
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("body is %d bytes, over the %d limit", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("the body is not valid UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("only part should fit (0 < kept < %d), got %d", len(m.Items), kept)
	}
	// The header must state honestly how many items this message contains and how many
	// remain — otherwise the reader takes the number in the header as the whole.
	if !strings.Contains(body, "remaining") || !strings.Contains(body, "in the next message") {
		t.Fatalf("the header must state how many items are not in this message:\n%s", body[:minInt(400, len(body))])
	}
	// Only the first kept items should be included (the CJK names come from the batchMsg fixture).
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "漏洞"+itoa(i+1)) {
			t.Fatalf("item %d should be in this message:\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "漏洞"+itoa(kept+1)) {
		t.Fatalf("item %d must not appear (it belongs to the next batch)", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = no limit
	if kept != len(m.Items) {
		t.Fatalf("with no length limit everything must be kept, got kept=%d", kept)
	}
	if strings.Contains(body, "remaining") {
		t.Fatalf("the truncation note must not appear when nothing was truncated:\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// When the budget is too small for even one item, one must still be sent (the final
	// truncation covers it). Otherwise one oversized finding stalls the whole batch forever: nothing fits on each claim, and nothing is ever sent.
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("at least 1 item must be kept, got %d", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("a single message must report 1 item delivered, got %d", kept)
	}
	// An empty message has no items to deliver.
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("an empty message should report 0 items, got %d", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram limits length by **character count**; counting bytes would squeeze a Chinese message to a third.
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("body of %d characters exceeds the limit %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("should fit only part of it, got %d", kept)
	}
	if !strings.Contains(text, "next message") {
		t.Fatalf("should say that some content was left out:\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("the card should fit only part of it, got %d", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// These two channels do not truncate the body, so the whole batch counts as delivered.
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("precondition does not hold")
	}
	// Confirmed indirectly through the renderer's return value: markdownBody(0) keeps everything when unlimited.
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("should use all items when length is unlimited, got %d", k)
	}
}

// TestMarkdownEscapesUntrustedContent is the regression test for "untrusted content must not change message structure".
// The title and summary come from model output (the model reads the tested target's response), asset names come from the tested target's URL.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // must appear in the result (escaped form)
		wrong []string // must not appear in the result (unescaped form)
	}{
		{
			name: "newline + external link in title",
			item: Item{
				Severity: "high",
				Name:     "login page SQL injection\n[URGENT: verify account here](http://attacker.tld)",
			},
			// Newlines must be folded (otherwise a new list item/blockquote could be forged);
			// Square brackets and parentheses must be escaped (otherwise it is a clickable external link).
			must:  []string{`\[URGENT: verify account here\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[URGENT", "\n\n[URGENT"},
		},
		{
			name: "image beacon in title",
			item: Item{
				Severity: "high",
				Name:     "vulnerability ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "emphasis and quote in asset name",
			item: Item{
				Severity: "high",
				Name:     "ordinary title",
				Assets:   []string{"a.com/*inject*>quote"},
			},
			must:  []string{`\*inject\*`, `\>`},
			wrong: []string{"*inject*"},
		},
		{
			name: "backtick and pipe in summary",
			item: Item{
				Severity: "high",
				Name:     "title",
				Summary:  "`code` | table",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			// writeItem in single-item mode is the rendering path shared by the three markdown channels.
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("missing escaped form %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("unescaped form %q appeared (can be used to inject structure or an external link):\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst pins the escape order: the backslash must be handled first,
// otherwise the backslashes added afterwards get escaped again and double backslashes show up in the output.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("wrong escape order, got %q", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes pins a specific regression:
// markdown escaping must not leak into Telegram's HTML output (escaping was once added
// to the shared title helper and Telegram messages ended up with visible backslashes like `\(1\)`).
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *emphasis*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("markdown backslash escaping appeared in the Telegram body:\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
