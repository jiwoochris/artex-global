package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes truncates s to at most max bytes while guaranteeing the result is
// valid UTF-8 and no character is split in half.
//
// Why character boundaries are mandatory: the WeCom group robot's markdown has a
// hard limit of 4096 **bytes** (not characters), while a CJK character is 3 bytes.
// Slicing by bytes would cut a character in half and produce invalid UTF-8 — the
// platform either rejects the whole message or shows replacement boxes. The
// approach here is to walk back from the budget position to the nearest rune start
// byte (utf8.RuneStart identifies continuation bytes 0b10xxxxxx).
//
// max<=0 means unlimited. Truncation appends an ellipsis unless max is too small
// to hold it.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max is shorter than the ellipsis itself: drop the ellipsis and truncate
		// plainly so the result does not exceed max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine flattens multi-line text onto a single line: all whitespace is folded,
// then it is truncated by character count. Used for IM message title lines —
// summaries often contain newlines, and dropping them straight into a table or
// title breaks the layout. max<=0 means unlimited length.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes truncates s to at most max characters (not bytes), appending an
// ellipsis when it overflows. max<=0 means unlimited.
//
// The difference from TruncateBytes is the platform's unit: WeCom limits by bytes,
// Telegram by character count. Using the wrong unit does not error, it just
// truncates far shorter than intended (one CJK character = 3 bytes, so a 4096-byte
// limit leaves only ~1365 characters), which is why both functions must stay and
// each channel must pick the right one.
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

// TruncateHTML truncates an HTML fragment by character count without leaving a
// half tag behind.
//
// Naively truncating HTML produces fragments such as `<a href="htt`, which a
// platform parser either rejects outright or swallows the rest of the body into as
// an attribute value. The approach here: truncate by characters, then check
// whether the tail has an unclosed `<` and back off to before it.
//
// Tags are not balanced (no closing `</b>`): Telegram's HTML parser closes
// unclosed tags itself, and implementing balancing would have to handle quotes
// inside attributes, comments, and void elements — disproportionate complexity for
// the benefit.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// If the tail is a fragment starting with `<` (no `>` after the last `<`), back
	// off to before that `<`.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// A cut HTML entity (say `&amp;` truncated to `&amp`) has to be backed out too.
	// An entity fragment can make a parser that only understands entities reject
	// **the whole message** — an over-length digest message is common enough that
	// losing the entire notification is not worth it.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount computes how many items fit **whole** within the budget, so a
// digest can be packed item by item.
//
// Why pack whole items instead of rendering everything and truncating: truncation
// silently drops the trailing items while their delivery records are still marked
// as delivered — invisible in the message, invisible in the delivery history, and
// the findings are simply gone. Packing whole items leaves the ones that do not
// fit in the database for the next batch, and the kept value the caller receives is
// the number of items this message really delivered.
//
// Parameters: maxSize<=0 means unlimited; reserve is the amount set aside for the
// message header/footer; size does the measuring (the unit differs per platform:
// WeCom/DingTalk count bytes, Telegram counts characters — using the wrong unit
// does not error, it just squeezes the message well below the limit); render turns
// item idx into its actual text — lengths vary with content and cannot be
// estimated.
//
// Returns at least 1 as long as there are items. A single extremely long item is
// still sent and covered by the caller's final truncation, otherwise one oversized
// finding would block the whole batch forever.
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

// byteSize / runeSize are the two measuring units for packItemCount, named so call
// sites do not have to pass bare func(s string) int closures and make the unit
// impossible to see at a glance.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine renders an asset list as one display line, eliding the rest and noting
// the total once it exceeds limit. A finding can be anchored to dozens of assets
// and listing them all would burst the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " +" + itoa(len(assets)-limit) + " more"
}

// itoa is a short alias for strconv.Itoa, used only for assembling display text so
// strconv does not have to be imported everywhere.
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
