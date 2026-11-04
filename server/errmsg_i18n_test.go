package server

import (
	"testing"

	"github.com/yukiteruamano/artex-global/config"
)

// TestTrMsgSwitchesLanguage pins the backend output-language switching: English is the
// source language, so an English message is returned verbatim under en (and when
// unwired), and the Spanish overlay is returned under es. This is the backend analogue of
// the web i18n catalog — it proves writeErr responses follow the configured language.
func TestTrMsgSwitchesLanguage(t *testing.T) {
	t.Cleanup(func() { config.CurrentLanguage = nil })

	// A representative catalog key (an existing server error constant value).
	const enMsg = errAssetIDRequired
	if enMsg != "asset_id is required" {
		t.Fatalf("precondition: the source message should be the English constant, got %q", enMsg)
	}

	// Unwired → English passthrough.
	config.CurrentLanguage = nil
	if got := trMsg(enMsg); got != enMsg {
		t.Fatalf("unwired: trMsg must pass through English, got %q", got)
	}

	// en → passthrough (the key itself; en is the identity mapping).
	config.CurrentLanguage = func() string { return "en" }
	if got := trMsg(enMsg); got != enMsg {
		t.Fatalf("en: trMsg must return the English source, got %q", got)
	}

	// es → the Spanish overlay of the same key.
	config.CurrentLanguage = func() string { return "es" }
	const esMsg = "asset_id es obligatorio"
	if got := trMsg(enMsg); got != esMsg {
		t.Fatalf("es: trMsg must return the Spanish overlay %q, got %q", esMsg, got)
	}

	// A removed or unknown language code → English fallback (config.NormalizeLanguage
	// rejects anything outside SupportedLanguages).
	for _, lang := range []string{"fr", "ko", "zh", "ja"} {
		config.CurrentLanguage = func() string { return lang }
		if got := trMsg(enMsg); got != enMsg {
			t.Fatalf("%s: trMsg must fall back to the English source, got %q", lang, got)
		}
	}

	// A string not in the catalog (propagated lower-layer error) passes through.
	config.CurrentLanguage = func() string { return "es" }
	const notInCatalog = "some-lower-layer-error: connection refused"
	if got := trMsg(notInCatalog); got != notInCatalog {
		t.Fatalf("non-catalog string must pass through unchanged, got %q", got)
	}
}
