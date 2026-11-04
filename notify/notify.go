// Package notify implements the IM / email delivery adapters for vulnerability
// findings.
//
// Layering: this package is a **leaf package** and depends only on the standard
// library. It knows nothing about the database or the server. Channel
// configuration arrives as a map[string]any (the notification_channels.config
// JSONB column) and the payload as a Message. Keeping it this way means the
// genuinely error-prone parts — signature computation, UTF-8 truncation, filter
// matching — can be unit-tested without PostgreSQL, and the host only has to do
// the orchestration on the server side.
//
// Concurrency contract: Channel implementations must be **stateless**. One
// Channel instance is reused concurrently by several channel configurations
// (even several bot instances of the same channel), so every credential is
// passed in through the cfg argument; nothing such as a webhook URL may be
// cached in the implementation's own fields.
package notify

// Channel type identifiers. These values are also the valid set for
// notification_channels.kind and are whitelisted on the server side (same
// reasoning as findings.status: no DB CHECK, so channels stay easy to add).
const (
	KindDingTalk = "dingtalk" // DingTalk custom robot
	KindFeishu   = "feishu"   // Feishu (including Lark) custom robot
	KindWeCom    = "wecom"    // WeCom group robot
	KindWebhook  = "webhook"  // Generic webhook: custom method/headers/JSON template
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP email
)

// Event types, matching notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind is the fallback for a channel config whose kind is empty.
const InitKind = KindDingTalk

// severityRank maps a vulnerability severity to a comparable ordinal. An unknown
// severity yields 0, so any min_severity setting filters it out — when in doubt
// do not push, which keeps false positives from flooding the channel.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank returns the ordinal for a severity; unknown severities return 0.
func SeverityRank(severity string) int { return severityRank[severity] }

// Lang, when wired by the server, returns the active user-facing output language
// ("en"/"es") for the strings this leaf package renders into push / email
// messages (severity and status labels). Kept as an injected func so the package
// stays standard-library-only: it does not import config/db/server. nil (unit
// tests / standalone) → labelLang falls back to "en".
var Lang func() string

func labelLang() string {
	if Lang != nil {
		switch v := Lang(); v {
		case "en", "es":
			return v
		}
	}
	return "en"
}

// severityLabels / statusLabels hold the per-language display strings. "en" is
// the source language and the fallback.
var severityLabels = map[string]map[string]string{
	"en": {"critical": "🔴 Critical", "high": "🟠 High", "medium": "🟡 Medium", "low": "🔵 Low"},
	"es": {"critical": "🔴 Crítico", "high": "🟠 Alto", "medium": "🟡 Medio", "low": "🔵 Bajo"},
}

var statusLabels = map[string]map[string]string{
	"en": {"pending": "Pending", "in_progress": "In progress", "confirmed": "Confirmed", "resolved": "Resolved", "fixed": "Fixed", "false_positive": "False positive", "ignored": "Ignored", "duplicate": "Duplicate", "risk_accepted": "Risk accepted"},
	"es": {"pending": "Pendiente", "in_progress": "En curso", "confirmed": "Confirmado", "resolved": "Resuelto", "fixed": "Corregido", "false_positive": "Falso positivo", "ignored": "Ignorado", "duplicate": "Duplicado", "risk_accepted": "Riesgo aceptado"},
}

// SeverityLabel returns the emoji-prefixed severity name in the active output
// language, used in message titles and card colors. Unknown severities are
// echoed back unchanged rather than invented.
func SeverityLabel(severity string) string {
	if v, ok := severityLabels[labelLang()][severity]; ok {
		return v
	}
	return severity
}

// StatusLabel translates a triage status into the active output language, used in
// status-change messages. Unknown statuses are echoed back unchanged.
func StatusLabel(status string) string {
	if v, ok := statusLabels[labelLang()][status]; ok {
		return v
	}
	return status
}

// AtLeast reports whether severity meets the min threshold. An empty min means no
// threshold, so everything passes. Note that an unknown severity has ordinal 0
// and is therefore rejected by any non-empty min (see the severityRank comment).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
