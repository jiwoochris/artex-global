package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertEnglish(t *testing.T, label, s string) {
	t.Helper()
	for _, r := range s {
		if r > 127 {
			t.Errorf("%s: expected ASCII English: %q", label, s)
			return
		}
	}
}

// TestPostgresDSNErrorIsEnglish pins the startup message to English whatever
// ARTEX_LANG says. When neither ARTEX_PG_DSN nor a config file supplies a database,
// PostgresDSN returns an error that propagates verbatim to the
// `log.Fatalf("open stores: %v", err)` in cmd/artex/main.go.
func TestPostgresDSNErrorIsEnglish(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	t.Setenv("ARTEX_LANG", "ko")

	_, _, err := PostgresDSN()
	if err == nil {
		t.Fatal("missing config should error")
	}
	assertEnglish(t, "startup error", err.Error())

	// The identifiers an operator must act on stay verbatim.
	for _, want := range []string{"ARTEX_PG_DSN", "database", "dsn", "host/user/dbname"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("startup error should keep identifier %q verbatim: %q", want, err.Error())
		}
	}
}

// TestPostgresDSNSourceIsEnglish pins the three source labels, which only reach
// the startup log, to English whatever ARTEX_LANG says.
func TestPostgresDSNSourceIsEnglish(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("ARTEX_LANG", "ko")

	// env source
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "nope.json"))
	t.Setenv("ARTEX_PG_DSN", "postgres://envwins/x")
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("env source: %v", err)
	} else {
		assertEnglish(t, "env source", source)
	}

	// config file dsn source
	t.Setenv("ARTEX_PG_DSN", "")
	if err := os.WriteFile(cfgPath, []byte(`{"database":{"dsn":"postgres://full/dsn"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", cfgPath)
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("dsn source: %v", err)
	} else {
		assertEnglish(t, "dsn source", source)
	}

	// config file fields source
	if err := os.WriteFile(cfgPath, []byte(`{"database":{"host":"10.1.2.3","dbname":"d","user":"u"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, source, err := PostgresDSN(); err != nil {
		t.Fatalf("fields source: %v", err)
	} else {
		assertEnglish(t, "fields source", source)
	}
}
