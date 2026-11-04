package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/yukiteruamano/artex/db"
	"github.com/yukiteruamano/artex/notify"
)

// Global settings keys (stored in the settings key/value table, no dedicated table).
const (
	// settingNotifyEnabled is the master switch for notifications. It defaults to on:
	// it exists to stop all pushes during maintenance, not to gate the feature -
	// the feature is gated by whether any channel is configured.
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL is the externally reachable address used to build
	// finding detail links (e.g. https://artex.example.com). When empty, messages
	// carry no link button. The project has no reusable external address setting,
	// so this one was added here.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes is the digest period (minutes).
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick is the delivery engine poll interval. 3s is the ceiling for how
	// realtime this engine gets and the dominant delay between a finding being
	// persisted and the message arriving in the IM client.
	notifyTick = 3 * time.Second
	// When notifyBackgroundDisabledEnv is set, the server does not start the
	// background delivery loop (newNotifier(s).Run). Integration tests call
	// stepRealtime/stepDigest directly to verify distribution deterministically;
	// a background loop on a 3s cadence touching the same channels makes the
	// aggregate flaky. This is a test-only switch: when the variable is absent the
	// server starts as usual, so production behaviour is unchanged.
	notifyBackgroundDisabledEnv = "ARTEX_NOTIFY_BACKGROUND_DISABLED"
	// notifyLease is the lease taken when claiming deliveries. It must be
	// significantly larger than the worst-case time of one delivery (the notify
	// package HTTP client timeout is 15s), otherwise the same row is delivered by
	// two dispatchers at once.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick caps how many events are fanned out per tick, so that
	// enabling a channel for the first time does not expand the whole historical
	// backlog into delivery rows in one go.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes is the default digest period.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick is the per-tick delivery cap for a channel with
	// no rate limit configured. It exists so that "unlimited channel + one scan
	// producing thousands of findings" cannot turn a single tick into a long
	// blocking loop.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick is how many deliveries a single channel may
	// send per tick.
	//
	// The cap is derived backwards from the **lease length**: claiming a row stamps
	// a lease on it (notifyLease = 3 minutes), so if a tick sends so many rows
	// serially that the worst case exceeds the lease, the last few rows expire
	// before they are sent. Within a single process that is harmless (Run is one
	// serial goroutine and ticks never re-enter), but with **two processes sharing
	// one database** the peer re-claims expired rows and sends them twice, doubles
	// the attempts counter and marks the row failed while the original process is
	// still delivering it.
	//
	// Value: 3 minute lease / 30 second per-send timeout = 6 exactly consumes the
	// lease with zero headroom, so it is not usable; 5 leaves 30 seconds of headroom
	// on a 150 second worst case. TestNotifyTickBudgetFitsWithinLease pins this
	// relation - changing notifyLease, notifySendTimeout or this value makes that
	// assertion fail.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout is the per-send timeout. It also determines the value of
	// the constant above: their product must not exceed notifyLease, see
	// TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff is the retry backoff sequence indexed by attempts already made.
// 3 chances (including the first) corresponds to db.MaxNotifyAttempts; both must
// be changed together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Delivery failure/delay reasons. They are stored in
// notification_deliveries.last_error, returned by the delivery history endpoint in
// notify_api.go and rendered verbatim in the notification page "delivery history"
// table (delivery-list.tsx), so they are user-visible and go through trMsg for
// en/es localisation; diagnostic log.Printf calls keep the raw text.
const (
	errDeliveryChannelKindUnregistered = "Unregistered channel type: %q"
	errDeliverySnapshotUnrenderable    = "Could not parse the event snapshot, so this vulnerability could not be turned into a message"
	errDeliveryChannelLengthCapped     = "This message reached the channel length limit, so only the first %d were sent and the rest are deferred to the next batch"
	errDeliveryNoDeliveredCount        = "The channel did not report the number of messages sent (delivered=%d)"
	errDeliveryRetryExhausted          = "Still failed after %d retries: %s"
	errDeliverySnapshotEmpty           = "The event snapshot for delivery item #%d is empty"
	errDeliverySnapshotParse           = "Failed to parse the event snapshot of delivery item #%d: %w"
	errDeliveryBatchAllUnparseable     = "Failed to parse all %d deliveries in the summary batch"
)

// Notifier is the delivery engine for finding notifications.
//
// It sits next to Scheduler and runs as its own goroutine (see server.New). It
// deliberately does not reuse the Scheduler tick: notifications need 3 second
// latency, the trigger cadence is a different concern, and the two failure
// domains stay isolated - a stalled push must not delay agent triggering.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu guards buckets. Channels are few and contention is low, so one mutex is
	// enough; finer-grained structure is not worth it.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket is the token bucket of a single channel.
//
// A token bucket is used instead of a "count per minute, then reset" window
// because the latter has a bad boundary effect: filling the quota at the end of a
// window and again right after looks like 40 messages in one second to the
// platform and gets rate limited. A bucket refills at a constant rate and avoids
// that burst by construction.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run loops until ctx is done. Started once by server.New.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step runs one round: fan out new events, then deliver the due rows.
//
// Any step failure is only logged and never breaks the loop - a notification
// subsystem fault must not escalate into a process-level problem. Every tick is
// independent, so the next one retries naturally.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] fanning out pending events failed: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] listing channels failed: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// The token bucket counts **messages** (equivalently HTTP requests), not
		// findings. In realtime mode both are the same (one message per finding); in
		// digest mode a whole batch collapses into one message, so it costs one token.
		//
		// Both modes ask the bucket first and only then claim up to the allowance -
		// order matters, otherwise deliveries blocked by the rate limit have already
		// burned a retry attempt.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan returns the token cost and the batch size upper bound for a
// digest channel in the current tick.
//
// The two return values are **two different units**, which is why this is its own
// function:
//
//   - tokens is a message count. A batch of findings becomes one message and one
//     HTTP request, so it is always 1. rate_per_min therefore still applies to
//     digest (at most that many digest messages per minute).
//   - claimLimit is how many findings the batch may hold. It is bounded only by
//     memory, never by the request budget.
//
// To make rate_per_min apply to digest, this once passed the per-tick request
// budget (notifyMaxSendsPerChannelPerTick, derived from the lease) straight down
// as the batch size. The consequence: a channel with rate_per_min=20 refilled only
// one token per 3 second tick, so every digest message held exactly one finding -
// digest degraded into "realtime push with digest wording", readers got a stream of
// "1 new finding in the last 30 minutes" and db.MaxDigestBatchSize was unreachable.
//
// The symptom is hard to catch end to end (existing cases pass a large limit to
// stepDigest directly, bypassing the allowance computation in step), so the
// decision lives here and is pinned by
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime claims and delivers the realtime rows of one channel, one message
// per finding.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] claiming realtime deliveries failed channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf(trMsg(errDeliveryChannelKindUnregistered), ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// A render failure is a local data problem; retrying will not help.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest aggregates the pending deliveries of one channel into a single
// message once the batch window is due.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] checking digest batch due failed channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] claiming digest batch failed channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf(trMsg(errDeliveryChannelKindUnregistered), ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Deliveries whose snapshot was broken and could not enter the message must be
	// failed explicitly. Otherwise they stay outside included - neither in the
	// message nor in any failure list - and on a successful send the later bulk mark
	// misses their state, leaving them in sending until the lease expires and they
	// are claimed again and again.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := errDeliverySnapshotUnrenderable
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] marking broken-snapshot deliveries failed channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] skipped %d deliveries with unparseable snapshots channel=%d", len(skipped), ch.ID)
	}
	// Only the deliveries that entered the message are handed to send:
	// included[i] corresponds exactly to msg.Items[i], and send relies on that to
	// land "the channel only took the first K" onto the right delivery rows.
	n.send(ctx, channel, cfg, msg, included)
}

// send delivers the message and moves state according to the result.
//
// One batch of deliveries (tens of rows in digest mode) shares a single send
// result: either delivered or retried as a whole. There is no per-row retry - the
// digest message is one message, and resending part of it would break batch
// semantics.
//
// The one exception is **splitting caused by the channel length limit**: when the
// channel reports it only took the first K items, item K+1 onwards must wait for
// the next batch instead of being marked successful with the rest. Otherwise the
// truncated findings are neither in the message nor in any failure list and vanish.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Bound a single send so one stuck channel cannot hold up the remaining
	// channels of this tick.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// A channel cannot report more delivered items than it was given; if it
			// happens the render layer miscounted. Treat all as delivered and log the
			// problem - better than writing inconsistent rows.
			log.Printf("[notify] channel reported %d delivered, more than %d deliveries channel=%s; treating all as delivered",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] marking delivered failed channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// This message hit the channel length limit: the remainder goes straight
			// back to the queue and continues on the next tick. Use DeferDeliveries
			// rather than RescheduleDeliveries - this is not a failure and must not
			// burn the retry budget (claiming already incremented it optimistically;
			// deferral rolls that back).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf(trMsg(errDeliveryChannelLengthCapped), delivered)); err != nil {
				log.Printf("[notify] requeueing length-capped remainder failed channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// The channel neither errored nor reported a delivered count. Treat it as a
		// failure (with backoff) so the row is not claimed over and over without ever
		// being marked.
		err = fmt.Errorf(trMsg(errDeliveryNoDeliveredCount), delivered)
	}

	// Failure handling is decided **per row**, not from the batch maximum attempt
	// count.
	//
	// It used to be `if maxAttempts(deliveries) >= MaxNotifyAttempts` failing the
	// whole batch, but attempts differ within a batch: an old delivery that already
	// retried twice (attempts=2) dragged brand new deliveries (attempts=1) in the
	// same batch into failed - a new finding was lost permanently without a single
	// retry, the exact opposite of "an old row must not sink a new one".
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] marking failed status errored channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf(trMsg(errDeliveryRetryExhausted), db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] marking failed status errored channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Reschedule grouped by delay: there are only 3 backoff steps, so the number
	// of groups is naturally tiny and one UPDATE per row is unnecessary (that would
	// mean 500 round trips for a 500 row batch).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] rescheduling deliveries failed channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] delivery failed channel=%d kind=%s permanent=%d exhausted=%d retry-pending=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries returns the entries of all that are not in keep (compared by
// pointer identity). It finds the deliveries that could not enter the message -
// they must be handled explicitly, never left in limbo.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt resolves the channel implementation and parses its config.
// ok=false means the kind is not registered: the delivery must fail immediately
// rather than retry forever.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// On a config parse failure hand over an empty map: the channel's own Validate
		// reports which field is missing, and that error guides users to a fix better
		// than a JSON parse error does.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle renders a single-finding message.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch renders a digest message. Snapshots are parsed one by one - a single
// broken snapshot is skipped instead of sinking the whole digest.
//
// The returned included corresponds **strictly one to one** with msg.Items (the
// i-th delivery <-> the i-th item). That correspondence is a hard requirement: the
// caller marks the first K deliveries as sent based on "the channel took the first
// K". If a broken snapshot were skipped without removing its delivery from
// included, indices would shift - a bad entry that should fail would be marked
// delivered and a good one would be treated as undelivered. The broken ones are
// failed explicitly by the caller, see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// A broken snapshot enters neither the message nor included - handling it
			// is up to the caller (fail it explicitly instead of letting it ride along
			// as "delivered").
			log.Printf("[notify] skipping unparseable snapshot in digest batch delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf(trMsg(errDeliveryBatchAllUnparseable), len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor renders an event snapshot into a notification item, resolving asset
// names and the detail link on the way.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// A failure to resolve asset names must not block the push: a missing name is
		// far cheaper than a missing notification - the message just has one fewer
		// asset line.
		log.Printf("[notify] resolving asset names failed finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// The detail route is web/src/app/(main)/function/findings/detail/page.tsx;
		// it reads the finding id from the `id` query parameter.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens removes **at most want** tokens from the channel bucket and returns
// how many were actually taken.
//
// One token = one message (one HTTP request). Realtime mode passes the number of
// findings it wants; digest mode sends one message for the whole batch and passes 1.
//
// Bucket capacity is the channel per-minute limit and refills at a constant rate.
// ratePerMin<=0 means unlimited: a finite but generous value is returned so a
// single tick cannot get stuck on an unbounded backlog.
//
// The want cap is required: without it the bucket would simply be drained, while
// the caller has its own per-tick cap, so the extra tokens are both unusable and
// gone before the next refill - the accumulated burst capacity would never be
// reachable, and even a tick with nothing pending would be charged.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Refill by real elapsed time; the rate is ratePerMin/60 per second.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add a tiny epsilon before truncating: tokens are accumulated in float, so two
	// 0.5 refills can sum to 0.9999999999 and a plain int() truncates that to 0 - a
	// mathematically full bucket that yields nothing. 1e-9 is far below one token, so
	// a genuine shortfall still gets rejected.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled reads the master switch.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL returns the external address used for links, without a trailing slash.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval returns the digest period, falling back to the default when it is
// invalid or unset.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot parses the snapshot of the event a delivery refers to.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf(trMsg(errDeliverySnapshotEmpty), dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf(trMsg(errDeliverySnapshotParse), dl.ID, err)
	}
	if snap.Kind == "" {
		// The event row is authoritative for the kind; the snapshot copy may have been
		// written by an older version.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
