package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hasHan reports whether s contains a CJK Han ideograph (Chinese). Only English
// and Spanish are supported, so Han must not appear in user-visible strings.
func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// hasHangul reports whether s contains a Hangul syllable (Korean). Korean is no
// longer a supported language, so it must not appear in user-visible strings.
func hasHangul(s string) bool {
	for _, r := range s {
		if r >= 0xac00 && r <= 0xd7a3 {
			return true
		}
	}
	return false
}

// assertNoUnsupportedScript rejects Hangul/Han in output, so a stale Korean or
// Chinese translation cannot slip back in.
func assertNoUnsupportedScript(t *testing.T, label, s string) {
	t.Helper()
	if hasHangul(s) {
		t.Errorf("%s: Hangul remains (Korean is unsupported): %q", label, s)
	}
	if hasHan(s) {
		t.Errorf("%s: Han ideograph remains (Chinese is unsupported): %q", label, s)
	}
}

// TestSupportedLanguageSet pins the language contract: exactly English (the
// source language) and Spanish, with every other code rejected by
// NormalizeLanguage so callers fall through to the next source.
func TestSupportedLanguageSet(t *testing.T) {
	if got := strings.Join(SupportedLanguages, ","); got != "en,es" {
		t.Errorf("SupportedLanguages = %q, want [en es]", got)
	}
	if DefaultLanguage != "en" {
		t.Errorf("DefaultLanguage = %q, want en", DefaultLanguage)
	}
	for _, code := range []string{"en", "EN", " es "} {
		if got := NormalizeLanguage(code); got == "" {
			t.Errorf("NormalizeLanguage(%q) rejected a supported language", code)
		}
	}
	for _, code := range []string{"ko", "zh", "ja", "fr", ""} {
		if got := NormalizeLanguage(code); got != "" {
			t.Errorf("NormalizeLanguage(%q) = %q, want \"\" (unsupported)", code, got)
		}
	}
}

// TestActiveLanguageFallsBackToEnglish pins the fallback of the backend
// display-string language: an unwired hook or an unsupported hook value resolves
// to English; a supported value wins.
func TestActiveLanguageFallsBackToEnglish(t *testing.T) {
	prev := CurrentLanguage
	t.Cleanup(func() { CurrentLanguage = prev })

	CurrentLanguage = nil
	if got := ActiveLanguage(); got != "en" {
		t.Errorf("unwired ActiveLanguage = %q, want en", got)
	}
	CurrentLanguage = func() string { return "ko" }
	if got := ActiveLanguage(); got != "en" {
		t.Errorf("ActiveLanguage with unsupported hook = %q, want en", got)
	}
	CurrentLanguage = func() string { return "es" }
	if got := ActiveLanguage(); got != "es" {
		t.Errorf("ActiveLanguage with es hook = %q, want es", got)
	}
}

// TestPostgresDSNErrorDefaultsToEnglish pins the install-path startup message to
// English when nothing is configured. When neither ARTEX_PG_DSN nor a config file
// supplies a database, PostgresDSN returns an error that propagates verbatim
// (db.DSN → server.NewManager → the `log.Fatalf("open stores: %v", err)` in
// cmd/artex/main.go). An operator who boots with missing config sees it first, so
// it must read as English.
func TestPostgresDSNErrorDefaultsToEnglish(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	// Pin the default explicitly so ARTEX_LANG in the environment cannot leak in.
	t.Setenv("ARTEX_LANG", "")

	msg := mustDSNErr(t)
	assertNoUnsupportedScript(t, "default startup error", msg)
	if !strings.Contains(msg, "database configuration not found") {
		t.Errorf("default startup error should be English: %q", msg)
	}

	// The identifiers an operator must act on stay verbatim (not translated).
	for _, want := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !strings.Contains(msg, want) {
			t.Errorf("startup error should keep identifier %q verbatim: %q", want, msg)
		}
	}
}

// TestPostgresDSNErrorLocalized pins the Spanish overlay for the same startup
// error when ARTEX_LANG=es. Identifiers an operator must act on stay verbatim.
func TestPostgresDSNErrorLocalized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	t.Setenv("ARTEX_LANG", "es")

	msg := mustDSNErr(t)
	assertNoUnsupportedScript(t, "es startup error", msg)
	if !strings.Contains(msg, "no se encontró la configuración de la base de datos") {
		t.Errorf("es startup error should be Spanish: %q", msg)
	}
	if strings.Contains(msg, "database configuration not found") {
		t.Errorf("es startup error was not translated: %q", msg)
	}
	for _, want := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !strings.Contains(msg, want) {
			t.Errorf("es startup error should keep identifier %q verbatim: %q", want, msg)
		}
	}
}

// TestPostgresDSNSourceLocalized pins the three DSN source labels (logged at
// server/manager.go) to English by default and Spanish with ARTEX_LANG=es. They
// live in the same function as the startup error, so they share its language.
func TestPostgresDSNSourceLocalized(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	t.Setenv("ARTEX_LANG", "es")

	// env source
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	t.Setenv("ARTEX_PG_DSN", "postgres://envwins/x")
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("env source: %v", err)
	} else if source != "variable de entorno ARTEX_PG_DSN" {
		t.Errorf("es env source = %q", source)
	}

	// config file dsn source
	t.Setenv("ARTEX_PG_DSN", "")
	if err := os.WriteFile(cfgPath, []byte(`{"database":{"dsn":"postgres://full/dsn"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", cfgPath)
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("dsn source: %v", err)
	} else if !strings.Contains(source, "archivo de configuración") || !strings.Contains(source, "(database.dsn)") {
		t.Errorf("es dsn source = %q", source)
	}

	// config file fields source
	if err := os.WriteFile(cfgPath, []byte(`{"database":{"host":"10.1.2.3","dbname":"d","user":"u"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("fields source: %v", err)
	} else if !strings.Contains(source, "archivo de configuración") || !strings.Contains(source, "(campos database)") {
		t.Errorf("es fields source = %q", source)
	}

	// Default (no ARTEX_LANG) → English source language, so the error message is English.
	t.Setenv("ARTEX_LANG", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	if _, _, err := PostgresDSN(); err == nil {
		t.Fatal("default source: expected an error when no DSN is configured")
	} else if !strings.Contains(err.Error(), "database configuration not found") || !strings.Contains(err.Error(), "ARTEX_PG_DSN") {
		t.Errorf("default error = %q", err)
	}
}
