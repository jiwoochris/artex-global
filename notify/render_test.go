package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// This is the most important invariant of the package. WeCom limits by **bytes**
	// and a CJK character is 3 bytes, so any byte-slicing implementation cuts a
	// character in half, producing invalid UTF-8 that the platform rejects. Drive
	// every possible cut point with mixed CJK/Latin inputs of coprime lengths.
	inputs := []string{
		"中文测试内容",
		"混合 mixed 内容 content",
		"a中b文c测d试e",
		"🔴🟠🟡🔵", // 4 byte emoji, a wrong cut is even more visible
		strings.Repeat("漏洞", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("input %q max=%d: produced invalid UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("input %q max=%d: result %d bytes exceeds the limit", in, max, len(got))
			}
			// Content must not change when nothing was truncated.
			if len(in) <= max && got != in {
				t.Fatalf("input %q max=%d: content changed although under the limit -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0 must mean unlimited")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0 must mean unlimited")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// When max is smaller than the ellipsis itself, appending the ellipsis must not
	// push the result back over the limit.
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("max=1: result %q has length %d, over the limit", got, len(got))
	}
	// The normal case carries the ellipsis.
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("expected a trailing ellipsis, got %q", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// The difference from TruncateBytes must be preserved: Telegram limits by
	// character count, and measuring bytes would cut a CJK message down to a third.
	s := "一二三四五六七八九十"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("expected 5 characters, got %d (%q)", n, got)
	}
	// The same string measured in bytes must be clearly shorter.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("the byte unit must not yield the same character count as the rune unit")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("first line\n\nsecond line\twith tabs   multiple spaces", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("all whitespace must be folded, got %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("consecutive spaces must not survive, got %q", got)
	}
	// The truncated form must still be readable and valid.
	got = OneLine("一二三四五六七八九十", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("expected 4 characters, got %d (%q)", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// Truncating HTML directly leaves fragments like `<a href="htt`, and the platform
	// rejects the whole message.
	s := `<b>Title</b>BodyBodyBody<a href="https://example.com/very/long/path">View details</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: result has %d characters, over the limit", max, n)
		}
		// The tail must not leave an unclosed `<` (a `<` in the last segment with no
		// following `>`).
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: trailing tag cut in half -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("no assets must return an empty string, got %q", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("under the limit everything must be listed, got %q", got)
	}
	// Over the limit the remainder must be counted, otherwise readers cannot tell how
	// many assets are missing.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "+3 more") {
		t.Fatalf("expected the remaining 3 to be counted, got %q", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("an empty severity ranks 0 and must be stopped by any threshold")
	}
	if !AtLeast("critical", "") {
		t.Fatal("an empty threshold must let everything through")
	}
	if got := StatusLabel("fixed"); got != "Fixed" {
		t.Fatalf("unexpected status mapping, got %q", got)
	}
	// An unknown status is echoed back verbatim rather than invented.
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("an unknown status must be echoed back verbatim, got %q", got)
	}
}

// TestTruncateHTMLNeverCutsEntity covers a gap found in an audit: truncation must
// avoid not only half a tag but also a cut HTML entity.
//
// Once `&amp;` is cut to `&amp`, a parser that only recognises entities can reject
// the **whole** message - and over-long digest messages are common, so the cost is
// too high.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// No entity fragment with a `&` and no matching `;` may be left at the tail.
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: entity fragment left at the tail %q", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: malformed entity", max)
		}
	}
}
