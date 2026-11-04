package intercept

import "github.com/yukiteruamano/artex/config"

// Multilingual table of the intercept verdict action labels (allow/deny/ask). It is used in verdict messages and follows
// the output language (config.ActiveLanguage). An unwired or unsupported language falls back to ko. The enum keys (allow/deny/
// ask) and the source marker ("[模型]") are stable identifiers, so they are not translated.
var judgeActionLabels = map[string]map[string]string{
	"en": {"allow": "allow", "deny": "deny", "ask": "ask"},
	"ko": {"allow": "allow", "deny": "deny", "ask": "ask"},
	"zh": {"allow": "allow", "deny": "deny", "ask": "ask"},
	"es": {"allow": "permitir", "deny": "bloquear", "ask": "preguntar"},
	"ja": {"allow": "allow", "deny": "deny", "ask": "ask"},
}

func judgeActionLang() string {
	lang := config.ActiveLanguage()
	if _, ok := judgeActionLabels[lang]; ok {
		return lang
	}
	return "ko"
}
