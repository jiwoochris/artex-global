package notify

// Snapshot is the contract for the notification_events.snapshot JSONB column. The
// writer is the db-layer transaction that persists a finding; the readers are the
// server-layer delivery engine and the filter matcher. It lives in this package
// because it is a payload of the notification domain: db only serializes it and
// does not interpret the fields.
//
// Why the finding fields are stored redundantly instead of being re-read at render
// time: a finding can later be renamed, re-rated, or re-statused, but a pushed
// message should reflect the conclusion **as of the event** — re-reading would
// produce the dangerously misleading "since downgraded to low". It also means
// fan-out and rendering do not need to JOIN findings/tasks/assets.
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
	// Only non-empty when kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item is one finding to be pushed, for a channel to render.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets holds resolved display names for assets (domain/IP). Filled in by the
	// server layer — this package does not touch the database and cannot resolve
	// names itself.
	Assets []string
	// DetailURL is the back-link to the finding's detail view; empty means
	// public_base_url is unset and the renderer omits it.
	DetailURL string
	// Status-change events only; when both are non-empty this renders as
	// "Pending → Fixed".
	FromStatus string
	ToStatus   string
}

// IsStatusChange reports whether this item is a status-change event.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title returns the item's display title: the human-assigned name first, then the
// vulnerability class, and a placeholder when both are empty — never an empty
// title.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(unnamed vulnerability)"
}

// Message is the complete content of one channel send.
type Message struct {
	// Length is 1 for a single push and a whole batch for a digest push.
	// An empty slice is invalid; the caller must guarantee at least one item.
	Items []Item
	// Batch=true renders a digest message (different title, plus the time window
	// and item count).
	Batch bool
	// WindowMinutes is the digest period in minutes; only used with Batch=true,
	// for the "last N minutes" wording. Deliberately passed in from configuration
	// rather than computed with time.Since during rendering, so rendering stays
	// deterministic and testable.
	WindowMinutes int
	// HomeURL is the platform dashboard address (global public_base_url); empty
	// means no dashboard entry is included.
	HomeURL string
}
