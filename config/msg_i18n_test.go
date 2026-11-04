package config

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestStartupMessageSwitchesLanguage pins that the DB-config startup error and
// source labels follow ARTEX_LANG: English by default, Spanish when configured.
// Only en/es are supported; ko/zh/ja are rejected by NormalizeLanguage and fall
// back to the English default.
func TestStartupMessageSwitchesLanguage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))

	// Default (no ARTEX_LANG) → English (artex-global source language).
	t.Setenv("ARTEX_LANG", "")
	def := mustDSNErr(t)
	assertNoUnsupportedScript(t, "default startup error", def)
	for _, id := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !containsStr(def, id) {
			t.Errorf("default startup error must keep identifier %q: %q", id, def)
		}
	}

	// Spanish → Spanish overlay, identifiers kept.
	t.Setenv("ARTEX_LANG", "es")
	es := mustDSNErr(t)
	assertNoUnsupportedScript(t, "es startup error", es)
	if !strings.Contains(es, "variable de entorno ARTEX_PG_DSN") {
		t.Errorf("es startup error should be Spanish: %q", es)
	}
	for _, id := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !containsStr(es, id) {
			t.Errorf("es startup error must keep identifier %q: %q", id, es)
		}
	}

	// ko/zh/ja are rejected and leave the English default in place.
	for _, lang := range []string{"ko", "zh", "ja"} {
		if got := NormalizeLanguage(lang); got != "" {
			t.Errorf("NormalizeLanguage(%q) = %q, want \"\" (unsupported)", lang, got)
		}
		t.Setenv("ARTEX_LANG", lang)
		if got := mustDSNErr(t); got != def {
			t.Errorf("ARTEX_LANG=%s should fall back to English, got %q", lang, got)
		}
	}
}

func mustDSNErr(t *testing.T) string {
	t.Helper()
	_, _, err := PostgresDSN()
	if err == nil {
		t.Fatal("missing config should error")
	}
	return err.Error()
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestCatalogsKeyedByEnglishSource pins the catalog key contract: cfgCatalog is
// keyed by exactly the English source literals PostgresDSN passes, both catalogs
// store only the Spanish overlay, and no Korean/Chinese text remains.
func TestCatalogsKeyedByEnglishSource(t *testing.T) {
	wantCfg := []string{
		"environment variable ARTEX_PG_DSN",
		"config file %s (database.dsn)",
		"config file %s (database fields)",
		"database configuration not found: environment variable ARTEX_PG_DSN is not set, and config file %s has no database (dsn or host/user/dbname) configuration. Create a config file or set the environment variable, then try again",
	}
	gotCfg := make([]string, 0, len(cfgCatalog))
	for key := range cfgCatalog {
		gotCfg = append(gotCfg, key)
	}
	sort.Strings(gotCfg)
	sort.Strings(wantCfg)
	if strings.Join(gotCfg, "\n") != strings.Join(wantCfg, "\n") {
		t.Errorf("cfgCatalog keys = %q, want %q", gotCfg, wantCfg)
	}

	for name, cat := range map[string]map[string]map[string]string{"cfgCatalog": cfgCatalog, "logCatalog": logCatalog} {
		for key, tr := range cat {
			if hasHangul(key) || hasHan(key) {
				t.Errorf("%s key is not English: %q", name, key)
			}
			for lang, v := range tr {
				if lang != "es" {
					t.Errorf("%s[%q] stores unexpected language %q", name, key, lang)
				}
				if v == "" {
					t.Errorf("%s[%q][%s] is empty", name, key, lang)
				}
			}
			if tr["es"] == "" {
				t.Errorf("%s[%q] has no Spanish translation", name, key)
			}
		}
	}
}

// TestCatalogFormatVerbsMatch pins that every Spanish translation keeps the
// English source template's format verbs in the same order. Callers wrap only
// the template (log.Printf(T(template), args...)), so a dropped or reordered
// verb would print %!s(MISSING) or swap arguments in the operator's console.
func TestCatalogFormatVerbsMatch(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for name, cat := range map[string]map[string]map[string]string{"cfgCatalog": cfgCatalog, "logCatalog": logCatalog} {
		for key, tr := range cat {
			want := strings.Join(verbs.FindAllString(key, -1), " ")
			for lang, v := range tr {
				if got := strings.Join(verbs.FindAllString(v, -1), " "); got != want {
					t.Errorf("%s[%q][%s] verbs = %q, want %q", name, key, lang, got, want)
				}
			}
		}
	}
}
