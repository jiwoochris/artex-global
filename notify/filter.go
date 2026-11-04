package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is the contract for the notification_channels.filter JSONB column: the
// filter conditions of a channel instance. Every field is optional and absent
// means "no filtering" — which is exactly the fallback semantics for malformed
// configuration, see ParseFilter.
type Filter struct {
	// MinSeverity is the minimum severity threshold (low/medium/high/critical);
	// empty means no threshold.
	MinSeverity string `json:"min_severity"`
	// An empty TaskIDs / AssetIDs means unrestricted; non-empty requires the event
	// to intersect with it.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// An empty VulnClassInclude means accept everything; non-empty requires
	// vulnclass to contain one of the keywords. VulnClassExclude drops the event if
	// any keyword matches (exclusion wins over inclusion). Matching is a
	// case-insensitive substring — safer than a regex, because a mis-typed regex
	// cannot silently disable the channel.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange decides whether this channel receives finding status-change
	// events (only meaningful in realtime mode).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter parses a channel's filter configuration.
//
// **Never returns an error.** That is a deliberate design choice: a malformed
// filter degrades to the zero-value Filter (= no filtering = match everything),
// because for a vulnerability notification system **one extra push is far better
// than silently dropping a critical one**. Turning a parse failure into "push
// nothing" would hand the user a channel that looks configured but delivers
// nothing — the worst possible failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// f keeps its zero value if parsing fails, i.e. no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity reports whether s is a valid severity threshold (an empty
// string means no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate checks the filter fields whose **values are constrained**, for use when
// saving a channel.
//
// Why this must be caught on write: Match evaluates an unknown threshold as
// `rank >= 0`, which is always true — so one typo in min_severity ("hgih") makes
// the filter **silently ineffective** and everything gets pushed. That is
// consistent with this package's "rather over-push than under-push" tradeoff (no
// findings are lost), but the user ends up believing they configured tiered
// delivery while every finding floods the group, with nothing hinting at the
// misconfiguration. This kind of silent degradation is exactly what should be
// rejected at the entry point.
//
// Validate is only used on the **write** path. The read path keeps ParseFilter's
// lenient semantics, so bad values already present in historical data cannot make
// a channel unreadable.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("invalid minimum severity: %q. Choose one of low / medium / high / critical, or leave it empty for no restriction", f.MinSeverity)
	}
	return nil
}

// Match decides whether an event should be delivered to a channel carrying these
// filter conditions.
//
// **Never returns an error**, for the same reason as ParseFilter: any internal
// anomaly counts as "match". Order of checks: event type → severity threshold →
// task/asset scope → vulnerability-class keywords.
func Match(f Filter, s Snapshot) bool {
	// Status-change events are only received by channels that explicitly opt in.
	// Off by default, because the overwhelming majority of users read "push" as
	// "a new finding was discovered", not as a running commentary on every state
	// transition.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclusion wins: any matching exclude keyword drops the event even if it also
	// matched the include list.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// A linear scan over a small set is enough; both sides are "a few dozen
	// hand-picked entries", so building a map costs more than it saves.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether s contains any of the keywords
// (case-insensitively).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
