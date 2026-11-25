package db

import (
	"testing"
	"unicode"
)

// assertRetestReasonKorean asserts that a retest reason constant contains Hangul and no Chinese Hanzi
// (F9). Since the constants are concatenated into SQL literal positions in FinishFindingRetest and RecoverFindingRetests,
// pinning the constants also protects the user-facing text shown in the panel.
func assertRetestReasonKorean(t *testing.T, name, s string) {
	t.Helper()
	if s == "" {
		t.Fatalf("%s: 빈 문자열", name)
	}
	hasHangul := false
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			t.Fatalf("%s: 중국어 한자가 남아 있습니다: %q", name, s)
		}
		if unicode.Is(unicode.Hangul, r) {
			hasHangul = true
		}
	}
	if !hasHangul {
		t.Fatalf("%s: 한글이 없습니다: %q", name, s)
	}
}

// TestFindingRetestReasonsLocalized asserts that the two termination reason constants, stored in the finding_retests.error column
// and shown in the retest panel (finding-retest-panel) as item.error, are in Korean.
// They share the same column and panel as the sibling reasons in server/conversations.go (convRetest*), so if only one of them
// were Korean, the same panel would mix languages.
func TestFindingRetestReasonsLocalized(t *testing.T) {
	assertRetestReasonKorean(t, "retestNoConclusionReason", retestNoConclusionReason)
	assertRetestReasonKorean(t, "retestServiceRestartReason", retestServiceRestartReason)
}
