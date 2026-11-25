package notify

// Snapshot is the contract for the notification_events.snapshot JSONB column. The
// writer is the db layer's transaction that persists a finding to the DB; the
// readers are the server layer's delivery engine and filter matching. The
// definition lives in this package because it is a "notification domain" payload:
// db only serializes it and does not understand what the fields mean.
//
// Why the finding fields are stored redundantly instead of looked up at render
// time: findings get renamed, re-graded, and change status after the fact, while a
// pushed notification should reflect the conclusion **at the time of the event** —
// a lookup would produce the dangerously misleading "later changed to low".
// Also, fan-out and rendering therefore do not need to JOIN the three tables
// findings/tasks/assets.
type Snapshot struct {
	// Event type: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// Non-empty only when kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item is one finding waiting to be pushed, for channels to render.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets are the resolved asset display names (e.g. domain/IP). Filled in by the
	// server layer — this package does not touch the database and cannot get the names.
	Assets []string
	// DetailURL is the back-link to the finding's detail page; empty means
	// public_base_url is not configured, and it is omitted when rendering.
	DetailURL string
	// Used only by status-change events; when both are non-empty they render as
	// "pending → fixed".
	FromStatus string
	ToStatus   string
}

// IsStatusChange reports whether this item is a status-change event.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title returns the item's display title: prefer the manually assigned name, fall
// back to the vulnerability class vulnclass, and use a placeholder when both are
// empty — never output an empty title.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(이름 없는 취약점)"
}

// Message is the complete content of one channel send.
type Message struct {
	// Length 1 for a single push; a whole batch for a digest push.
	// An empty slice is invalid; the caller must guarantee at least one item.
	Items []Item
	// When Batch=true it is rendered as a digest message (different title, with the
	// time window and item count).
	Batch bool
	// WindowMinutes is the digest period (minutes), used only when Batch=true for the
	// "last N minutes" text. It is deliberately passed in explicitly from config rather
	// than computed with time.Since at render time: keeping rendering deterministic
	// makes it testable.
	WindowMinutes int
	// HomeURL is the platform dashboard URL (the global public_base_url); when empty,
	// no dashboard link is included.
	HomeURL string
}
