package notify

import (
	"fmt"
	"strings"
)

// This file holds the message rendering shared by the "Markdown family" channels
// (DingTalk, WeCom). Feishu renders card JSON, Telegram renders HTML, and email
// renders HTML, each inside its own adapter.

// maxAssetsShown is the maximum number of assets listed in a message. A finding
// may be anchored to dozens of assets and listing them all bursts the message
// without adding information — nobody reads the fourth domain in an IM message.
const maxAssetsShown = 3

// maxSummaryRunes is the character count a summary is compressed to. An IM message
// is a prompt to go read the details, not the report itself; the full content
// lives in the platform.
const maxSummaryRunes = 120

// markdownReservedBytes is reserved for the message header (batch line + severity
// breakdown + possible truncation note) and footer (platform link). It is deducted
// from the budget when packing whole items so the header and footer survive — once
// they are truncated, the reader cannot even tell which batch this is or how many
// items are not shown.
const markdownReservedBytes = 320

// markdownEscape escapes markdown metacharacters.
//
// Why this is mandatory: finding titles, summaries, classes, and asset display
// names all come from **untrusted sources** — titles and summaries are model
// output (and the model reads the target's responses), while an asset URL is the
// full scanned URL (including a target-controlled query string). Without escaping,
// a finding titled
//
//	login form SQL injection\n[URGENT: verify your account] (http://attacker.tld)
//
// renders in the security engineer's DingTalk/Feishu as a **clickable external
// link**; and `![](http://attacker.tld/beacon)` is fetched by the client while
// rendering, which both announces "this finding has been read" and leaks the
// reader's IP. Even with no malice, injected bold or a quote block can push the
// critical finding below the fold.
//
// The escape set covers the characters that change structure or create clickable
// elements in headings/links/emphasis/lists/quotes/strikethrough. `\` must be
// replaced first, otherwise the backslashes added afterwards get escaped again.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText flattens untrusted text to one line and escapes it, for markdown
// bodies. Flattening is the other half of escaping: a newline alone can forge a new
// list item or quote block, and escape characters do not stop that.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns the message title (the IM platform's title bar / card
// title) as **unescaped raw text**.
//
// Escaping is deliberately not done here: this title is shared by four rendering
// contexts — markdown bodies, Telegram HTML, Feishu card plain_text, and the
// generic webhook's JSON plus the email subject. Each context escapes differently
// (markdown escapes show up as visible backslashes in HTML and pollute JSON), so
// escaping must belong to each output side; see writeItem / feishuItemLines /
// telegramEscape. Markdown escaping was once added to this shared function and
// Telegram messages ended up showing visible backslashes like `\(1\)`.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Vulnerability digest · %d total", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Vulnerability alert"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody renders the message body and returns it plus the number of items
// **actually written**.
//
// kept is the number of items this delivery really carried, and the caller marks
// only those first kept items as delivered — items excluded by the channel's length
// limit must wait for the next batch instead of being marked successful along with
// the rest. That is exactly where "silent loss" comes from: the message was
// truncated, but the delivery records say everything was delivered and nothing
// anywhere shows the second half was never sent.
//
// maxBytes<=0 means unlimited.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// A single message is sent even when over-length (covered by the final
		// truncation): partial information about one finding beats sending nothing.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[View everything in the platform](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro renders the opening of a digest message: the time window, the
// item count, and the severity breakdown. With those, whoever receives the digest
// can decide whether it needs immediate attention without opening the platform.
//
// items are the entries that **actually fit** and total is what this batch should
// have contained. When they differ, the message must say how many items follow in
// the next message — otherwise the reader assumes the number in the header is all
// there is, and the entries that were never sent do not exist anywhere in the UI.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**%d new vulnerabilities in the last %d minutes**", total, m.WindowMinutes)
	} else {
		fmt.Fprintf(&b, "**%d new vulnerabilities**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, " (this message shows only the first %d; the remaining %d follow in the next message)", len(items), extra)
	}
	// Give the severity breakdown so the reader sees at a glance whether anything is
	// critical. Only the entries **this message contains** are counted, so
	// "Critical 3" matches the items that can be counted below.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem renders one finding entry.
//
// prefix is the numbering used in a digest list; single=true renders the full
// version (with summary and back-link), while a digest list renders just one summary
// line — otherwise a 50-item digest turns into a long document.
//
// Everything from outside (title/class/assets/summary) goes through markdownText:
// flattening plus escaping. The back-link is built from the administrator-configured
// public_base_url, is not untrusted content, and must stay clickable, so it is
// emitted as-is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Digest mode: single-line presentation, with compressed assets and summary
		// appended.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Status change**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Type**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**Assets**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**Summary**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[View details](%s)\n", it.DetailURL)
	}
}
