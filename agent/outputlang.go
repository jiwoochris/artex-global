package agent

// Selection of the user-facing output language (display language). artex-global forces the agent to write the natural language the user sees
// in one of five languages: English, Korean, Chinese, Spanish or Japanese. The agent's
// "brain" (the benchmarked Chinese reasoning body, segment [A]) is left as-is regardless of language. What is decided here
// is only the language of the surface the user reads.

// OutputLanguage is a hook the server wires up to return the output language code ("en"/"ko"/"zh"/"es"/"ja")
// chosen from configuration (ARTEX_LANG in config, the runtime settings KV). If it is nil or
// returns an empty value, the default below is used. The server injects it the same way as PromptOverride.
var OutputLanguage func() string

// The default output language when the server has not wired the hook (e.g. testing the agent package on its own).
// The default is Korean (it is the fallback for testing the agent package on its own; in production the server wires the OutputLanguage hook
// with the configuration layer's default, English). English, Chinese, Spanish and Japanese are switched to via configuration.
const defaultOutputLang = "ko"

// SupportedOutputLangs is the list of output language codes that can be switched to.
var SupportedOutputLangs = []string{"en", "ko", "zh", "es", "ja"}

// normalizeLang narrows codes outside the supported list to the default.
func normalizeLang(code string) string {
	for _, c := range SupportedOutputLangs {
		if c == code {
			return code
		}
	}
	return defaultOutputLang
}

// resolveOutputLang returns the output language code to apply right now. It checks the hook wired by the server
// first, and uses the default if there is none.
func resolveOutputLang() string {
	if OutputLanguage != nil {
		if code := OutputLanguage(); code != "" {
			return normalizeLang(code)
		}
	}
	return defaultOutputLang
}

// langPhrase is the wording used to name the language ("write in this language") inside the directive (which is written in Chinese).
// It also includes the language's native name in parentheses so the model doesn't confuse which language is meant.
var langPhrase = map[string]string{
	"ko": "韩语（한국어）",
	"en": "英语（English）",
	"zh": "中文（简体中文）",
	"es": "西班牙语（Español）",
	"ja": "日语（日本語）",
}

// langBare is the short language name used when naming it repeatedly inside the directive.
var langBare = map[string]string{
	"ko": "韩语",
	"en": "英语",
	"zh": "中文",
	"es": "西班牙语",
	"ja": "日语",
}
