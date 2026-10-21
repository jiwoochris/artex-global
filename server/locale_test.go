package server

import (
	"encoding/json"
	"github.com/Autumn-27/artex/locale"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocaleMiddlewareErrorAndSSE(t *testing.T) {
	locale.Register("language must be en or ko", "language must be en or ko", "언어는 en 또는 ko여야 합니다")
	h := withLocale(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if locale.FromContext(r.Context()) != locale.Ko {
			t.Error("request language missing")
		}
		if _, ok := w.(http.Flusher); !ok {
			t.Error("SSE flushing unavailable")
		}
		writeErr(w, 400, "language must be en or ko")
	}))
	r := httptest.NewRequest("GET", "/api/settings?lang=ko", nil)
	r.Header.Set("Accept-Language", "en")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "언어는") || w.Header().Get("Content-Language") != "ko" {
		t.Fatalf("bad localized response: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Cookie") {
		t.Fatal("cookie language missing cache variation")
	}
}

func TestLocaleMiddlewarePreservesUnknownUserError(t *testing.T) {
	w := httptest.NewRecorder()
	lw := &localeWriter{w, locale.Ko}
	writeErr(lw, 400, "用户提供的数据 원문")
	if !strings.Contains(w.Body.String(), "用户提供的数据 원문") {
		t.Fatal("unknown content changed")
	}
}

func TestDynamicErrorLocalizesBeforeInterpolation(t *testing.T) {
	const template = "Asset %s is outside the authorized scope (task %d)"
	locale.Register(template, template, "자산 %s은(는) 승인 범위 밖입니다(작업 %d)")
	raw := "用户原文/한국어/%s?q=<raw>"
	err := locale.Errorf(template, raw, 17)
	h := withLocale(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeError(w, 400, err) }))
	r := httptest.NewRequest("GET", "/api/test", nil)
	r.Header.Set("Accept-Language", "ko")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Error string `json:"error"`
	}
	if decodeErr := json.Unmarshal(w.Body.Bytes(), &response); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if response.Error != "자산 "+raw+"은(는) 승인 범위 밖입니다(작업 17)" {
		t.Fatalf("wrong localized error or modified raw argument: %q", response.Error)
	}
	if err.Error() != "Asset "+raw+" is outside the authorized scope (task 17)" {
		t.Fatal("error identity changed with request language")
	}
}

func TestUpdateProgressRendersPerSubscriber(t *testing.T) {
	p := updateProgress{message: locale.M("Downloading %s (%s)…", "raw-한글.zip", "12 MB"), cause: locale.Errorf("Release package does not contain %s", "raw-한글.exe")}
	ko, en := p.inLanguage(locale.Ko), p.inLanguage(locale.En)
	if ko.Message != "raw-한글.zip(12 MB) 다운로드 중…" || en.Message != "Downloading raw-한글.zip (12 MB)…" {
		t.Fatalf("progress locale mismatch: %q / %q", ko.Message, en.Message)
	}
	if !strings.Contains(ko.Error, "릴리스 패키지") || !strings.Contains(ko.Error, "raw-한글.exe") {
		t.Fatalf("error locale/raw argument mismatch: %q", ko.Error)
	}
	if p.Message != "" || p.Error != "" {
		t.Fatal("subscriber mutated shared event")
	}
}

func TestAllProductionPackagesHaveRawShippedTranslations(t *testing.T) {
	keys := locale.Keys()
	var production []string
	for _, key := range keys {
		// This deliberate bilingual dynamic-error fixture is added by another test.
		if key == "Asset %s is outside the authorized scope (task %d)" {
			continue
		}
		production = append(production, key)
	}
	if len(production) != 2004 {
		t.Fatalf("production key count %d, want 2004 (1982 previous + host scope error + 20 auth setup/token keys + 1 chat input attribution error)", len(production))
	}
	if err := locale.ValidateTextTranslations(production); err != nil {
		t.Fatal(err)
	}
	t.Logf("raw completeness: %d production keys × %d locales", len(production), len(locale.Languages()))
	if err := locale.ValidateTextTranslations([]string{reporterToolCallMessage}); err != nil {
		t.Fatal(err)
	}
	for _, lang := range locale.Languages() {
		trigger, _ := locale.Raw(lang, reporterToolCallMessage)
		if reporterTriggerText(trigger, locale.En) != reporterToolCallMessage {
			t.Fatal("stock reporter trigger not reversed")
		}
		if reporterTriggerText(trigger+" user edit", locale.En) != trigger+" user edit" {
			t.Fatal("edited trigger changed")
		}
		if !isDefaultConversationTitle(locale.Text(lang, "New conversation")) {
			t.Fatal("stock conversation title not recognized")
		}
	}
}

func TestChineseLocaleMiddlewarePrecedenceAndFallback(t *testing.T) {
	old := locale.ServerDefault()
	locale.SetServerDefault(locale.En)
	defer locale.SetServerDefault(old)
	h := withLocale(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, 400, locale.Errorf("language must be one of %s", locale.SupportedLanguageNames()))
	}))
	for _, tc := range []struct{ query, header, cookie, want string }{
		{"zh-Hans", "ko", "en", "zh-Hans"}, {"", "zh-Hant;q=1,zh-CN;q=0.7,ko;q=0.2", "en", "zh-Hans"},
		{"zh-TW", "ko;q=0,zh-SG;q=0.6", "en", "zh-Hans"}, {"", "zh-Hant-CN", "zh-Hans", "zh-Hans"},
		{"zh-Hant", "zh-HK", "zh-Hant", "en"},
	} {
		r := httptest.NewRequest("GET", "/api/settings?lang="+tc.query, nil)
		r.Header.Set("Accept-Language", tc.header)
		r.AddCookie(&http.Cookie{Name: locale.CookieName, Value: tc.cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Content-Language") != tc.want {
			t.Fatalf("wrong negotiated language: %s", w.Header().Get("Content-Language"))
		}
		if !strings.Contains(w.Body.String(), "en, ko, zh-Hans") {
			t.Fatalf("canonical identifiers altered: %s", w.Body)
		}
		if tc.want == "zh-Hans" && !strings.Contains(w.Body.String(), "语言必须") {
			t.Fatal("Chinese error not rendered")
		}
	}
}
