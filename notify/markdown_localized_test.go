package notify

import (
	"strings"
	"testing"
)

// hanFreeItems builds only items with no CJK Han characters (ASCII title, type, summary).
// If a label reverts to Chinese, Han characters appear in the rendered output, so only when the data
// has zero Han does the "zero Han in the whole output" assertion pinpoint label regressions.
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

// TestMarkdownTitleLocalized checks that markdownTitle is Korean. It is used by the markdown-family
// channels (dingtalk, wecom) and by feishu, html, telegram and webhook, which share the title.
// If any part reverts to Chinese, the titles of all five channels become mixed-language.
func TestMarkdownTitleLocalized(t *testing.T) {
	// Multiple items (汇总, digest): "취약점 요약 · 총 N건" ("vulnerability summary · N total")
	got := markdownTitle(Message{Batch: true, Items: hanFreeItems(3)})
	assertKorean(t, "markdownTitle(batch)", got)
	if !strings.Contains(got, "취약점 요약") || !strings.Contains(got, "총 3건") {
		t.Errorf("다건 제목이 '취약점 요약 · 총 3건' 형태여야 합니다, 받은 값 %q", got)
	}
	// No items: "취약점 알림" ("vulnerability alert")
	empty := markdownTitle(Message{})
	assertKorean(t, "markdownTitle(empty)", empty)
	if empty != "취약점 알림" {
		t.Errorf("빈 메시지 제목은 '취약점 알림' 이어야 합니다, 받은 값 %q", empty)
	}
}

// TestMarkdownBatchIntroLocalized checks that the digest intro (time window, count, overflow notice)
// is Korean. The item data has zero Han, so Han characters in the output mean the intro wording
// has regressed to Chinese.
func TestMarkdownBatchIntroLocalized(t *testing.T) {
	items := hanFreeItems(3)

	// With a time window: "최근 N분간 신규 취약점 N건" ("N new vulnerabilities in the last N minutes")
	win := markdownBatchIntro(Message{Batch: true, WindowMinutes: 30}, items, 3)
	if hasHan(win) {
		t.Errorf("시간창 머리말에 중국어 한자가 남았습니다: %q", win)
	}
	if !strings.Contains(win, "최근 30분간") || !strings.Contains(win, "신규 취약점 3건") {
		t.Errorf("시간창 머리말이 '최근 30분간 신규 취약점 3건' 형태여야 합니다, 받은 값 %q", win)
	}

	// Without a time window: "신규 취약점 N건" ("N new vulnerabilities", no minutes shown)
	noWin := markdownBatchIntro(Message{Batch: true}, items, 3)
	if hasHan(noWin) {
		t.Errorf("머리말에 중국어 한자가 남았습니다: %q", noWin)
	}
	if !strings.Contains(noWin, "신규 취약점 3건") || strings.Contains(noWin, "분간") {
		t.Errorf("시간창 없는 머리말은 '신규 취약점 3건'(분간 표기 없음)이어야 합니다, 받은 값 %q", noWin)
	}

	// When only part fits: "(이 메시지에는 앞 N건만 … 나머지 N건은 다음 메시지에서 …)" ("this message has only the first N … the remaining N follow in the next message …")
	trunc := markdownBatchIntro(Message{Batch: true, WindowMinutes: 30}, items, 5)
	if hasHan(trunc) {
		t.Errorf("초과 안내에 중국어 한자가 남았습니다: %q", trunc)
	}
	for _, want := range []string{"앞 3건만", "나머지 2건", "다음 메시지에서"} {
		if !strings.Contains(trunc, want) {
			t.Errorf("초과 안내에 %q 가 있어야 합니다, 받은 값 %q", want, trunc)
		}
	}
}

// TestWriteItemLabelsLocalized checks that the field labels (status change, type, assets, summary)
// and the details link in the single-item render (path shared by the 3 markdown channels) are Korean.
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

	assertKorean(t, "writeItem(single)", got)
	for _, want := range []string{
		"**상태 변경**: 처리 대기 → 수정됨",
		"**유형**: SQLi",
		"**자산**: a.example.com",
		"**개요**: SQL injection via q param",
		"[상세 보기](https://platform.example/finding/1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("단건 렌더에 %q 가 있어야 합니다:\n%s", want, got)
		}
	}
}

// TestMarkdownBodyFooterLocalized checks that the platform entry link at the end of a multi-item
// body is Korean (it is appended only when HomeURL is set).
func TestMarkdownBodyFooterLocalized(t *testing.T) {
	m := Message{Batch: true, HomeURL: "https://platform.example", Items: hanFreeItems(2)}
	body, kept := markdownBody(m, 0)
	if kept != 2 {
		t.Fatalf("한도 없음(0)이면 2건 모두 담겨야 합니다, 받은 값 %d", kept)
	}
	if !strings.Contains(body, "[플랫폼에서 전체 보기](https://platform.example)") {
		t.Errorf("본문 끝에 '플랫폼에서 전체 보기' 링크가 있어야 합니다:\n%s", body)
	}
}
