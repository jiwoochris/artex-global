package notify

import (
	"fmt"
	"strings"
)

// This file holds the message rendering shared by the "Markdown family" channels
// (DingTalk, WeCom). Feishu uses card JSON, Telegram uses HTML, and email uses HTML;
// each renders in its own adapter.

// maxAssetsShown is the maximum number of assets listed in a message. A finding may
// be anchored to dozens of assets, and listing them all would blow up the message
// with no informational value — nobody reads the domains past the 4th one in an IM.
const maxAssetsShown = 3

// maxSummaryRunes is how many characters the summary is compressed to. An IM message
// is a "go look at the details" prompt, not the report itself; the full content is
// in the platform.
const maxSummaryRunes = 120

// markdownReservedBytes is reserved for the message header (digest line + severity
// distribution + a possible truncation notice) and footer (platform link). When
// packing whole items, this portion is deducted from the budget so the header and
// footer never get truncated — once they are cut, readers cannot even tell "which
// batch this is, and how many items were not shown".
const markdownReservedBytes = 320

// markdownEscape escapes markdown metacharacters.
//
// Why this is required: finding titles, summaries, classes, and asset display names
// all come from **untrusted sources** — titles and summaries come from model output
// (and the model reads responses from the target under test), and an asset's url is
// the full URL obtained by scanning (including a target-controlled query string).
// Without escaping, a finding titled
//
//	登录口 SQL 注入\n[紧急：点此验证账号](http://attacker.tld)
//
// (i.e. "SQL injection at login\n[Urgent: click here to verify your account](...)")
// would render as a **clickable external link** in a security engineer's
// DingTalk/Feishu; and `![](http://attacker.tld/beacon)` would be fetched by the
// client on render, effectively announcing "this finding has been viewed" and leaking
// the reader's IP. Even with non-malicious content, injected bold text or block
// quotes can push the critical findings below out past the fold.
//
// The escape set covers characters that change structure or produce clickable
// elements: headings/links/emphasis/lists/quotes/strikethrough. `\` must be handled
// first, otherwise the backslashes added afterwards would be escaped again.
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

// markdownText collapses untrusted text to a single line and escapes it, for use in
// markdown bodies. Collapsing to a single line is the other half beyond escaping: a
// newline by itself can forge a new list item or block quote, and escaping characters
// cannot stop that.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns the message title (the IM platform's title bar / card title);
// its content is the **unescaped original text**.
//
// It is deliberately not escaped here: this title is shared by renderers in four
// contexts — markdown bodies, Telegram HTML, Feishu card plain_text, and the generic
// Webhook's JSON and email subjects. Each context has different escaping rules
// (markdown escaping inside HTML leaves visible backslashes, and inside JSON it
// pollutes the data), so escaping must be the responsibility of each output; see
// writeItem / feishuItemLines / telegramEscape. Markdown escaping was once added in
// the shared function, and visible backslashes like `\(1\)` showed up in Telegram
// messages as a result.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("취약점 요약 · 총 %d건", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "취약점 알림"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody renders the message body and returns the body and **the number of
// items actually written**.
//
// The return value kept is the number of items actually delivered by this send; the
// caller uses it to mark only the first kept items as delivered — items kept out by
// the channel's length limit must wait for the next batch rather than being marked
// successful along with the rest. This is exactly where "silent loss" comes from:
// the message got truncated, but the delivery record shows everything delivered, and
// nowhere reveals that the second half was never sent.
//
// maxBytes<=0 means no limit.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// A single message is sent even if it is over-long (the final truncation is the
		// fallback): partial information about one finding is better than sending nothing.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[플랫폼에서 전체 보기](%s)\n", m.HomeURL)
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
// item count, and the severity distribution. With these, someone receiving the digest
// can judge whether the batch needs immediate attention without clicking into the
// platform.
//
// items are the entries **that actually fit**, and total is the total this batch
// should have. When the two differ, it must say explicitly "how many more items are
// in the next message" — otherwise readers would assume the number in the header is
// everything, while the items that were never sent simply do not exist in the UI.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**최근 %d분간 신규 취약점 %d건**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**신규 취약점 %d건**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, "(이 메시지에는 앞 %d건만 표시하며, 나머지 %d건은 다음 메시지에서 이어서 보냅니다)", len(items), extra)
	}
	// Give the distribution by severity so readers can see at a glance whether there is
	// anything critical. Only entries **actually contained in this message** are counted,
	// so that "critical: 3" matches the entries that can be counted below.
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

// writeItem renders a single finding entry.
//
// prefix is used for the ordinal in a digest list; with single=true the full version
// is rendered (with summary and back-link), while a digest list renders only a
// one-line summary — otherwise a 50-item digest would turn into a long document.
//
// All content from outside (title/class/assets/summary) goes through markdownText:
// collapsed to a single line + escaped. The back-link is built from the
// admin-configured public_base_url, is not untrusted content, and must be a clickable
// link, so it is output as-is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Digest mode: rendered on a single line, with the compressed assets and summary
		// following.
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
		fmt.Fprintf(b, "**상태 변경**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**유형**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**자산**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**개요**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[상세 보기](%s)\n", it.DetailURL)
	}
}
