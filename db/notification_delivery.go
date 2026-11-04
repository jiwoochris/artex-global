package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This file is the claim and state-transition layer for delivery jobs.
//
// Claiming uses a lease instead of a long transaction: set the row to sending and push
// next_attempt_at into the future as the lease deadline, commit, then do the network
// delivery. That way no database lock is held while delivering - a network request can
// take seconds (the client timeout is 15s) and holding its row lock would stall every
// other writer in the same database.
//
// The price is that a process crashing mid-delivery leaves the row in sending. That
// self-heals: once the lease expires next_attempt_at falls into the past and the next
// claim round picks the same row up again (see state IN ('pending','sending') in the
// claim condition). attempts is incremented at claim time already, so a crash cannot
// cause endless retries - after MaxNotifyAttempts chances the row lands in failed for
// manual handling.

// MaxNotifyAttempts is the maximum number of attempts for one delivery, first included.
// It lives here rather than in the delivery engine: it is policy of the state machine
// itself, the engine only executes it.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize is how many deliveries one digest batch may merge at most.
//
// It exists for resources: if a digest period uncovers tens of thousands of findings
// (entirely possible - one full scan does it), an unbounded claim would read every row
// into memory, render one huge message, and then lose most of it to the channel length
// limit - wasting memory and silently dropping the truncated findings.
// With the bound in place the excess stays in the database as the next batch and goes
// out in the following period, nothing is lost.
//
// 500 is where a rendered message still has readable content under the 4096-byte WeCom
// limit; anything larger only moves the truncation further down.
const MaxDigestBatchSize = 500

// NotificationDelivery is one delivery job, carrying the channel config and event
// snapshot needed to render it.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// Render context loaded by the join; kept out of JSON (the server layer builds the DTO).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind come from the event so the history list can jump straight to
	// the finding detail.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind are redundant fields for list display, sparing the
	// frontend a second query.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery is the single read shape for a delivery row: delivery + event
// snapshot + channel config. All three are required to render one message, so querying
// them separately would cost three round trips.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery describes one claim: select candidates by sel and lock them, then set them
// to sending and extend the lease. The caller leaves the lease position in sel as a $n
// placeholder and passes the argument itself.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries claims one channel's due realtime deliveries, at most limit.
//
// Claiming per channel rather than "claim one global batch and pick from it" is
// deliberate: the rate-limit gate is kept per channel in the delivery engine, and only
// by first knowing how many this channel may still send this round and then claiming
// exactly that many rows does rate limiting stay clear of retry budget. Claiming first
// and dropping later would charge attempts to rows that were merely held back by the
// limiter, spending the three chances on pure waiting until they land in failed.
//
// The condition includes "sending whose lease has expired" - that is where crash
// recovery lands. lease must be clearly larger than the worst case of one delivery
// (channel HTTP client timeout 15s), otherwise two dispatchers deliver the same row at
// once. Disabled channels are excluded as well: disabling has already marked the stored
// deliveries skipped, this is a second guard against a race between the two.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue reports whether the channel has accumulated a due batch: pending
// deliveries exist and the oldest one has reached the digest period.
//
// It judges by the age of the oldest delivery rather than the wall clock, so a freshly
// created channel does not emit a one-item "digest" the moment it crosses the hour, and
// a long-standing backlog does not wait one more round for nothing.
//
// It is separate from ClaimDigestBatch because the semantics differ: this function only
// answers "should we send", while a claim takes every pending row of the channel
// (including those not old enough yet) - otherwise one period would be split into
// several messages and the digest would lose its point.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch claims the channel's currently due pending deliveries as one digest
// batch, at most MaxDigestBatchSize per batch.
//
// Every delivery in a batch shares batch_id, using the smallest id in the set as the
// batch number (stable, readable, no extra sequence needed). On retry COALESCE keeps the
// original batch number so "these N went out together" still holds after several retries.
//
// Taking the first N by ascending id rather than at random: the earliest deliveries go
// out first, so a backlog never starves old findings behind new ones.
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit is a memory bound; callers pass MaxDigestBatchSize and it is clamped once
	// more here so a larger value cannot slip through.
	//
	// A rate-limit allowance is deliberately not accepted as batch size: the rate limit
	// counts messages - one batch sends one message and consumes one token, deducted by
	// takeTokens in the server layer - a different dimension from "how many findings fit
	// in one batch". Passing the per-round request budget in as batch size so that
	// rate_per_min would cover digests once made a rate=20/min channel carry one finding
	// per batch, degrading the digest into a realtime push with digest wording. To change
	// rate limiting change takeTokens' want, not this.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries performs "select + set sending and extend lease + read the full rows"
// in a single transaction. postClaim is an optional extra step (the digest batch uses it
// to write batch_id).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// Set sending and push next_attempt_at into the future: that future instant is the
	// lease deadline, so "lease not expired" and "retry time not reached" share one
	// condition and no extra column is needed.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent marks a batch of deliveries as sent.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries puts a batch of deliveries back to pending and pushes the retry
// time out.
//
// Going back to pending instead of adding another intermediate state keeps "how many
// chances are left" expressed in one place (MaxNotifyAttempts) and stops the state
// machine's branches from growing with the retry policy.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries puts a batch back to pending, immediately claimable again, and undoes
// the attempt charged at claim time.
//
// It has exactly one use: when a digest message is split by the channel length limit, the
// items that did not fit must remain for the next batch. That is not a failure, so it must
// not consume retry budget - attempts was optimistically incremented at claim time and
// has to be decremented here. Otherwise a 500-item backlog split into 25 segments of 20
// would have its tail judged failed by MaxNotifyAttempts at the third segment although
// those items never failed.
//
// GREATEST(...,0) covers the case where a manual resend zeroed attempts and execution
// still reaches here, so the counter never goes negative.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries marks a batch as finally failed, waiting for a manual resend from the
// delivery history.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// Placeholders start at $3: $1 is state and $2 is last_error.
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery manually resends one delivery: reset to pending, zero the
// attempt counter and make it due immediately. Zeroing is deliberate - a human pressing
// "resend" means the cause of the earlier failures was handled, so the old count has no
// business limiting it.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("delivery %d not found or its current state does not allow resend", id)
	}
	return nil
}

// NotificationDeliveryFilter is the query condition for the delivery history.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries pages through the delivery history, newest first.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError cuts an error message to the length the column accepts. A channel
// response body can be long (especially a generic webhook pointed at a self-hosted
// service), and keeping it whole would bloat the history list payload.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// Back off to a character boundary so no half UTF-8 character reaches the UI as mojibake.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "..."
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders builds the $n placeholder list starting at start and the matching
// arguments, for IN (...). For example start=3, ids=[7,8] -> "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
