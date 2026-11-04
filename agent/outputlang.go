package agent

// User-facing output language selection. artex-global forces the agent to write
// the natural-language surface a user reads in one of the supported display
// languages (English or Spanish). The agent's "brain" — the benchmarked reasoning
// body (section [A]) — stays language-independent; only the surface the user reads is
// selected here.

// OutputLanguage is the hook the server wires to the output language code
// ("en"/"es") it selected from settings (config's ARTEX_LANG, runtime settings KV).
// When nil or empty, the default below is used. Injected the same way as
// PromptOverride.
var OutputLanguage func() string

// defaultOutputLang is the output language used when the server wired no hook (for
// example, agent-package tests running on their own). English is the source
// language of the product, so this fallback is "en"; operations always wire
// OutputLanguage to the settings-layer value, which defaults to the same English.
const defaultOutputLang = "en"

// SupportedOutputLangs is the list of output language codes that can be selected.
var SupportedOutputLangs = []string{"en", "es"}

// normalizeLang narrows a code outside the supported list to the default.
func normalizeLang(code string) string {
	for _, c := range SupportedOutputLangs {
		if c == code {
			return code
		}
	}
	return defaultOutputLang
}

// resolveOutputLang returns the output language code currently in effect. It
// consults the hook the server wired first and falls back to the default.
func resolveOutputLang() string {
	if OutputLanguage != nil {
		if code := OutputLanguage(); code != "" {
			return normalizeLang(code)
		}
	}
	return defaultOutputLang
}

// langPhrase is the spelling used inside the directive to point at "write in this
// language". The native-script form is placed in parentheses so the model cannot
// confuse which language is meant.
var langPhrase = map[string]string{
	"en": "English",
	"es": "Spanish (Español)",
}

// langBare is the short language name used for repeated mentions inside the
// directive.
var langBare = map[string]string{
	"en": "English",
	"es": "Spanish",
}
