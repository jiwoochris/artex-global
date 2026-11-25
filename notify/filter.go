package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is the contract for the notification_channels.filter JSONB column: the
// filter conditions of a channel instance. Every field is optional, and the default
// is "no filtering" — which is precisely the fallback semantics for a malformed
// configuration; see ParseFilter.
type Filter struct {
	// MinSeverity is the minimum severity threshold (low/medium/high/critical); empty
	// means no threshold.
	MinSeverity string `json:"min_severity"`
	// An empty TaskIDs / AssetIDs array means no restriction; when non-empty, the event
	// must intersect with it.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// An empty VulnClassInclude accepts everything; when non-empty, vulnclass must match
	// at least one of the keywords.
	// A match on any VulnClassExclude keyword excludes the event (exclusion takes
	// precedence over inclusion).
	// Matching is a case-insensitive substring match — safer than regexes: a user who
	// misconfigures a regex cannot silently disable the channel.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange decides whether this channel receives finding status-change events
	// (only meaningful in realtime mode).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter parses a channel's filter configuration.
//
// **It never returns an error.** This is a deliberate design choice: when the filter
// configuration is malformed, it always degrades to the zero-value Filter (= no
// filtering = everything matches), because for a vulnerability notification system,
// **pushing one extra message is far better than silently missing a high-severity
// finding**. Turning a parse failure into "don't push" would hand the user a channel
// that looks configured but actually pushes nothing — the worst possible failure
// mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// On a parse failure f stays at its zero value, i.e. no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity reports whether s is a valid severity threshold (the empty string
// means no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate checks the fields in a filter configuration whose **values are
// restricted**; it is called when saving a channel.
//
// Why this must be caught at write time: Match evaluates an unknown threshold as
// `rank >= 0`, which is always true — that is, a single typo in min_severity
// ("hgih") makes the filter **silently stop working** and turn into "push
// everything". This is consistent with this package's "rather push too much than
// miss something" trade-off (nothing gets missed), but the consequence is that the
// user believes they are doing severity-based pushing while actually flooding the
// group with every finding, with no indication that they misconfigured it. This kind
// of "silent degradation" is exactly what should be stopped at the entry point.
//
// Note that Validate is used only on the **write** path. The read path still goes
// through ParseFilter's lenient semantics, so bad values already present in
// historical data do not make the whole channel unreadable.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("올바르지 않은 최소 심각도입니다: %q. low / medium / high / critical 중에서 선택하거나, 비워 두면 제한하지 않습니다", f.MinSeverity)
	}
	return nil
}

// Match decides whether an event should be delivered to a channel with this filter.
//
// **It never returns an error**, for the same reason as ParseFilter: any internal
// anomaly is treated as a "match".
// Evaluation order: event type → severity threshold → task/asset scope →
// vulnerability-class keywords.
func Match(f Filter, s Snapshot) bool {
	// Status-change events are received only by channels that explicitly enable them.
	// Off by default, because the vast majority of users expect "push" to mean "a new
	// finding was discovered", not a running log that follows every status transition.
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
	// Exclusion takes precedence: matching any exclude keyword rules the event out, even
	// if it also matches the include list.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// A linear scan is fine for small sets; on both sides the magnitude is "a few dozen
	// items picked by hand", so the cost of building a map exceeds the benefit.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether s contains any of the keywords (case-insensitive).
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
