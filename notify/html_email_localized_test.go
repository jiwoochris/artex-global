package notify

import (
	"strings"
	"testing"
)

// This file guards the F4 ③ Korean localization (email body and HTML template) against regressions.
// It catches html.go's labels and email.go's validation errors reverting to Chinese.
// The helpers hasHan, hasHangul and assertKorean live in notify_localized_test.go (same package).
//
// All data (title, type, assets, summary) is kept ASCII. That way the assertion "zero Han
// characters in the whole output" catches regressions in the skeleton labels only, not in content.
// Severity/status labels were already Koreanized in F4 ①, so they render as Hangul (not Han) and pass.

// TestHTMLItemLabelsLocalized turns on every label branch of a single-item email body
// and checks that the skeleton is Korean and contains no Chinese Han characters.
func TestHTMLItemLabelsLocalized(t *testing.T) {
	m := Message{
		HomeURL: "https://example.com/panel",
		Items: []Item{{
			Name:       "sqli-login", // Title() = Name
			VulnClass:  "injection",  // must differ from Title() for the type line to render
			Severity:   "high",       // SeverityLabel → 높음 ("high", in Hangul)
			Summary:    "login form is injectable",
			Assets:     []string{"host-a.example.com"},
			DetailURL:  "https://example.com/f/1",
			FromStatus: "pending", // IsStatusChange()=true → status-change line
			ToStatus:   "fixed",
		}},
	}
	out := htmlBody(m, 0)
	if hasHan(out) {
		t.Errorf("이메일 본문에 중국어 한자가 남아 있습니다:\n%s", out)
	}
	for _, want := range []string{
		"상태 변경", "유형", "자산", "개요", "상세 보기", "플랫폼에서 전체 보기",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("이메일 본문에 %q 라벨이 없습니다:\n%s", want, out)
		}
	}
}

// TestHTMLBatchIntroLocalized checks that both branches (with and without a time window) of the
// multi-item (digest) intro render in Korean.
func TestHTMLBatchIntroLocalized(t *testing.T) {
	items := []Item{{Name: "a", Severity: "high"}, {Name: "b", Severity: "low"}}

	withWindow := htmlBatchIntro(Message{Batch: true, WindowMinutes: 30, Items: items})
	assertKorean(t, "htmlBatchIntro(시간창)", withWindow)
	for _, want := range []string{"최근 30분간", "신규 취약점", "2건"} {
		if !strings.Contains(withWindow, want) {
			t.Errorf("시간창 머리말에 %q 가 없습니다: %q", want, withWindow)
		}
	}

	noWindow := htmlBatchIntro(Message{Batch: true, WindowMinutes: 0, Items: items})
	assertKorean(t, "htmlBatchIntro(시간창 없음)", noWindow)
	if !strings.Contains(noWindow, "신규 취약점 2건") {
		t.Errorf("시간창 없는 머리말이 %q 를 포함해야 합니다: %q", "신규 취약점 2건", noWindow)
	}
	if strings.Contains(noWindow, "분간") {
		t.Errorf("시간창이 없는데 '분간' 이 들어갔습니다: %q", noWindow)
	}
}

// TestEmailValidateLocalized checks that the four SMTP config validation errors are Korean and
// that each one says which field is the problem. These messages are shown verbatim to the user
// configuring the notification channel.
func TestEmailValidateLocalized(t *testing.T) {
	cases := []struct {
		name   string
		cfg    map[string]any
		substr string
	}{
		{"서버 주소 누락", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}, "SMTP"},
		{"포트 범위 벗어남", map[string]any{"host": "h"}, "포트"},
		{"발신자 누락", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}, "발신자"},
		{"수신자 누락", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}, "수신자"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (emailChannel{}).Validate(tc.cfg)
			if err == nil {
				t.Fatalf("검증이 실패해야 합니다: %v", tc.cfg)
			}
			assertKorean(t, "Validate("+tc.name+")", err.Error())
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("오류 메시지에 %q 가 있어야 합니다, 받은 값 %q", tc.substr, err.Error())
			}
		})
	}
}
