package notify

import (
	"strings"
	"testing"
)

// hasHan reports whether the string contains at least one CJK Unified Ideograph.
// It catches a translated label reverting to Chinese (a regression).
func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// hasHangul reports whether the string contains at least one Hangul syllable.
func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xAC00 && r <= 0xD7A3 {
			return true
		}
	}
	return false
}

// assertKorean asserts that the label contains Hangul and no Chinese Han characters.
func assertKorean(t *testing.T, where, got string) {
	t.Helper()
	if hasHan(got) {
		t.Errorf("%s: 중국어 한자가 남아 있습니다: %q", where, got)
	}
	if !hasHangul(got) {
		t.Errorf("%s: 한글이 없습니다: %q", where, got)
	}
}

// TestSeverityLabelLocalized checks that every severity enum value renders as a Korean label.
// The labels are shared by every notification channel (telegram, email, html, markdown, webhook,
// feishu, dingtalk, wecom), so if any reverts to Chinese, messages on every channel become mixed-language.
func TestSeverityLabelLocalized(t *testing.T) {
	// Chinese labels (严重/高危/中危/低危) → Korean (심각/높음/중간/낮음: critical/high/medium/low), consistent with the UI's status.severity.
	want := map[string]string{
		"critical": "심각",
		"high":     "높음",
		"medium":   "중간",
		"low":      "낮음",
	}
	for sev, label := range want {
		got := SeverityLabel(sev)
		assertKorean(t, "SeverityLabel("+sev+")", got)
		if !strings.Contains(got, label) {
			t.Errorf("SeverityLabel(%q)=%q, %q 를 포함해야 합니다", sev, got, label)
		}
	}
	// An unknown severity is returned verbatim (no value is invented).
	if got := SeverityLabel("made_up"); got != "made_up" {
		t.Errorf("알 수 없는 심각도는 원문 유지여야 합니다, 받은 값 %q", got)
	}
}

// TestStatusLabelLocalized checks that all 9 remediation statuses render in Korean.
// They must use the same wording as the UI status.finding namespace (B4a), so status-change notifications match the screen.
func TestStatusLabelLocalized(t *testing.T) {
	want := map[string]string{
		"pending":        "처리 대기",
		"in_progress":    "처리 중",
		"confirmed":      "확인됨",
		"resolved":       "처리됨",
		"fixed":          "수정됨",
		"false_positive": "오탐",
		"ignored":        "무시",
		"duplicate":      "중복",
		"risk_accepted":  "위험 수용",
	}
	for status, label := range want {
		got := StatusLabel(status)
		assertKorean(t, "StatusLabel("+status+")", got)
		if got != label {
			t.Errorf("StatusLabel(%q)=%q, %q 를 기대했습니다", status, got, label)
		}
	}
	// An unknown status is returned verbatim (no label is invented).
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Errorf("알 수 없는 상태는 원문 유지여야 합니다, 받은 값 %q", got)
	}
}

// TestItemTitlePlaceholderLocalized checks that the fallback title for an item with neither name nor
// type is the Korean placeholder (이름 없는 취약점, "unnamed vulnerability"). An empty title is never emitted.
func TestItemTitlePlaceholderLocalized(t *testing.T) {
	got := Item{}.Title()
	assertKorean(t, "Item{}.Title()", got)
	// A name wins if present; with only a type, the type is used (checks the fallback logic has not regressed).
	if n := (Item{Name: "로그인 SQLi"}).Title(); n != "로그인 SQLi" {
		t.Errorf("이름 우선 로직이 깨졌습니다, 받은 값 %q", n)
	}
	if v := (Item{VulnClass: "XSS"}).Title(); v != "XSS" {
		t.Errorf("유형 대체 로직이 깨졌습니다, 받은 값 %q", v)
	}
}

// TestAssetLineLocalized checks that the separators and total-count wording in the asset list are Korean/ASCII.
// If the Chinese punctuation (、), 等 or 个 come back, the notification body becomes mixed-language.
func TestAssetLineLocalized(t *testing.T) {
	// At or below the limit: list everything, separated by ASCII commas.
	if got := assetLine([]string{"a.example.com", "b.example.com"}, 3); got != "a.example.com, b.example.com" {
		t.Errorf("구분자가 ASCII 쉼표여야 합니다, 받은 값 %q", got)
	}
	// Above the limit: the first `limit` items + Korean "등 N개" ("and N in total"; N is the total count).
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if hasHan(got) {
		t.Errorf("자산 나열에 중국어 한자가 남았습니다: %q", got)
	}
	if !strings.Contains(got, "등 5개") {
		t.Errorf("전체 개수 5 를 '등 5개'로 표기해야 합니다, 받은 값 %q", got)
	}
}
