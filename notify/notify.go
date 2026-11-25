// Package notify implements the IM / email push channel adapter layer for
// vulnerability findings.
//
// Layering: this package is a **leaf package** that depends only on the standard
// library. It knows nothing about the database or the server. Channel configuration
// is passed in as a map[string]any (corresponding to the notification_channels.config
// JSONB column), and the content to push is passed in as a Message. The benefit of
// this split is that the genuinely error-prone parts — signature computation, UTF-8
// truncation, filter matching — can be unit-tested without PostgreSQL, and the host
// only needs to orchestrate on the server side.
//
// Concurrency contract: Channel implementations must be **stateless**. The same
// Channel instance is reused concurrently by multiple channel configurations (even by
// multiple bot instances of the same channel); all credentials are passed in via the
// cfg parameter, and caching things like the webhook URL in the implementation's own
// fields is not allowed.
package notify

// Channel type identifiers. These values are also the valid set for
// notification_channels.kind, validated against an allowlist on the server side (as
// with findings.status, no DB CHECK is used, which makes adding channels later
// easier).
const (
	KindDingTalk = "dingtalk" // DingTalk custom bot
	KindFeishu   = "feishu"   // Feishu (including Lark) custom bot
	KindWeCom    = "wecom"    // WeCom (WeChat Work) group bot
	KindWebhook  = "webhook"  // generic Webhook: custom method/headers/JSON template
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP email
)

// Event types, corresponding to notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind is the fallback value for a kind that is empty in config.
const InitKind = KindDingTalk

// severityRank maps a finding's severity to a comparable ordinal. Unknown severities
// return 0, so any min_severity setting keeps unknown severities out — when in doubt,
// don't push, to avoid flooding the channel with false positives.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank returns the ordinal of a severity; unknown severities return 0.
func SeverityRank(severity string) int { return severityRank[severity] }

// Lang, when wired by the server, returns the active user-facing output language
// ("en"/"ko"/"zh"/"es"/"ja") for the strings this leaf package renders into push /
// email messages (severity and status labels). Kept as an injected func so the
// package stays standard-library-only: it does not import config/db/server. nil
// (unit tests / standalone) → labelLang falls back to "ko", preserving the Korean
// localization golden tests.
var Lang func() string

func labelLang() string {
	if Lang != nil {
		switch v := Lang(); v {
		case "en", "ko", "zh", "es", "ja":
			return v
		}
	}
	return "ko"
}

// severityLabels / statusLabels hold the per-language display strings. "ko" keeps
// the exact Korean values the localization golden tests pin.
var severityLabels = map[string]map[string]string{
	"ko": {"critical": "🔴 심각", "high": "🟠 높음", "medium": "🟡 중간", "low": "🔵 낮음"},
	"en": {"critical": "🔴 Critical", "high": "🟠 High", "medium": "🟡 Medium", "low": "🔵 Low"},
	"zh": {"critical": "🔴 严重", "high": "🟠 高危", "medium": "🟡 中危", "low": "🔵 低危"},
	"es": {"critical": "🔴 Crítico", "high": "🟠 Alto", "medium": "🟡 Medio", "low": "🔵 Bajo"},
	"ja": {"critical": "🔴 重大", "high": "🟠 高", "medium": "🟡 中", "low": "🔵 低"},
}

var statusLabels = map[string]map[string]string{
	"ko": {"pending": "처리 대기", "in_progress": "처리 중", "confirmed": "확인됨", "resolved": "처리됨", "fixed": "수정됨", "false_positive": "오탐", "ignored": "무시", "duplicate": "중복", "risk_accepted": "위험 수용"},
	"en": {"pending": "Pending", "in_progress": "In progress", "confirmed": "Confirmed", "resolved": "Resolved", "fixed": "Fixed", "false_positive": "False positive", "ignored": "Ignored", "duplicate": "Duplicate", "risk_accepted": "Risk accepted"},
	"zh": {"pending": "待处理", "in_progress": "处理中", "confirmed": "已确认", "resolved": "已处理", "fixed": "已修复", "false_positive": "误报", "ignored": "忽略", "duplicate": "重复", "risk_accepted": "接受风险"},
	"es": {"pending": "Pendiente", "in_progress": "En curso", "confirmed": "Confirmado", "resolved": "Resuelto", "fixed": "Corregido", "false_positive": "Falso positivo", "ignored": "Ignorado", "duplicate": "Duplicado", "risk_accepted": "Riesgo aceptado"},
	"ja": {"pending": "対応待ち", "in_progress": "対応中", "confirmed": "確認済み", "resolved": "対応済み", "fixed": "修正済み", "false_positive": "誤検知", "ignored": "無視", "duplicate": "重複", "risk_accepted": "リスク受容"},
}

// SeverityLabel returns the severity name with an emoji (in the current output
// language), used for message titles and card colors.
// Unknown severities are echoed back as-is rather than made up.
func SeverityLabel(severity string) string {
	if v, ok := severityLabels[labelLang()][severity]; ok {
		return v
	}
	return severity
}

// StatusLabel translates a triage status into the current output language, for
// status-change messages. Unknown statuses are echoed back as-is.
func StatusLabel(status string) string {
	if v, ok := statusLabels[labelLang()][status]; ok {
		return v
	}
	return status
}

// AtLeast reports whether severity reaches the min threshold. An empty min means no
// threshold, and everything passes.
// Note that an unknown severity has ordinal 0 and is rejected by any non-empty min
// (see the severityRank comment).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
