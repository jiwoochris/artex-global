package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes truncates s to at most max bytes, guaranteeing the result is valid
// UTF-8 and never splits a character.
//
// Why cutting on character boundaries is required: WeCom group bot markdown has a
// hard limit of 4096 **bytes** (not characters), and a Chinese character is 3 bytes.
// Slicing directly by bytes would cut a Chinese character in half and produce
// invalid UTF-8 — the platform then either rejects the whole message or shows garbled
// boxes. The approach here is to back up from the budget position to the nearest rune
// start byte (utf8.RuneStart identifies continuation bytes 0b10xxxxxx).
//
// max<=0 means no limit. An ellipsis is appended after truncation, unless max is too
// small to fit the ellipsis.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max is shorter than the ellipsis: drop the ellipsis and truncate plainly, so the
		// result does not end up exceeding max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine collapses multi-line text into a single line: it folds all whitespace and
// then truncates by character count.
// Used for the title line of IM messages — summaries often contain newlines, and
// stuffing them straight into a table/title breaks the layout.
// max<=0 means no length limit.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes truncates s to at most max characters (not bytes), appending an
// ellipsis when it exceeds the limit. max<=0 means no limit.
//
// The difference from TruncateBytes is how the platform measures: WeCom limits
// length in bytes, Telegram in characters. Using the wrong measure does not raise an
// error, it only makes messages get cut far shorter than expected (1 Chinese
// character = 3 bytes, so cutting at 4096 bytes leaves only about 1365 characters),
// so both functions must be kept and chosen per channel.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML truncates an HTML fragment by character count while guaranteeing it
// does not produce a half tag.
//
// Truncating HTML directly by characters can cut out a broken tag like
// `<a href="htt`, and the platform's parser then either errors and rejects the whole
// message or swallows the following body text as an attribute value. The approach
// here is: truncate by characters first, then check whether the tail has an unclosed
// `<`, and if so back up to before it.
//
// No tag balancing (adding </b> and the like): Telegram's HTML parser automatically
// closes unclosed tags, and implementing balancing ourselves would have to handle
// quotes in attributes, comments, and self-closing tags — the complexity is out of
// proportion to the benefit.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// If the tail is a fragment starting with `<` (no `>` after the last `<`), back up to
	// before the `<`.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// If the tail is a cut-off HTML entity (e.g. `&amp;` cut into `&amp`), likewise back
	// up. In a parser that only recognizes entities, an entity fragment can get the
	// **whole message** rejected — a digest message exceeding the length limit is common
	// to begin with, and it is not worth losing the whole notification over it.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount computes how many items fit **completely** within the budget, so
// digest messages are packed by whole items.
//
// Why pack by whole items rather than render the whole thing and then truncate:
// truncation makes the trailing items vanish into thin air, while their delivery
// records are still marked as delivered — invisible in the message and invisible in
// the delivery history, and the finding is simply gone. With packing by whole items,
// the items that do not fit stay in the DB and become the next batch, and the kept
// value the caller gets is the number of items this message actually delivered.
//
// Parameters: maxSize<=0 means no limit; reserve is the amount set aside for the
// message header/footer; size does the measuring (platforms measure differently:
// WeCom/DingTalk in bytes, Telegram in characters — using the wrong measure does not
// raise an error, it only squeezes Chinese messages far below the limit); render
// renders item idx into its actual text — the length varies with content and cannot
// be estimated.
//
// Returns at least 1 (as long as there are items). Even when a single item is
// extremely long, that item must still be sent, with the caller's final truncation as
// the fallback; otherwise one over-long finding would leave the whole batch stuck in
// place forever.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize are the two measures for packItemCount. They are named so that
// call sites do not contain bare func(s string) int closures, which would make it
// hard to tell at a glance which measure is in use.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine renders the asset list as one line of display text; beyond limit assets,
// the rest are omitted and the total count is noted.
// A finding may be anchored to dozens of assets, and listing them all would blow up
// the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " 등 " + itoa(len(assets)) + "개"
}

// itoa is a short alias for strconv.Itoa, used only for building display text, to
// avoid importing strconv everywhere.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
