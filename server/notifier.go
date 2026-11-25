package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// Global settings keys (stored in the settings key-value table; no new table needed).
const (
	// settingNotifyEnabled is the master push switch. On by default: it exists as a one-click
	// kill switch during maintenance, not as the condition that enables the feature — the real
	// enabling condition is "whether any channel is configured".
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL is the externally reachable address used to build back-links
	// to vulnerability details (e.g. https://artex.example.com). If left empty, messages carry
	// no back-link button. The project has no reusable external-address setting, so a new one
	// is added here.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes is the period (in minutes) of digest mode.
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick is the polling interval of the delivery engine. 3 seconds is the upper bound on
	// this engine's latency, and also the main source of delay between "vulnerability persisted
	// to the DB" and "message arrives in the IM".
	notifyTick = 3 * time.Second
	// When notifyBackgroundDisabledEnv is set, the server does not start the background delivery
	// loop (newNotifier(s).Run). Integration tests call stepRealtime · stepDigest directly to
	// verify the distribution results deterministically, and if the 3-second background loop
	// processes the same channels concurrently, the counts fluctuate with timing. This is a
	// test-only switch to prevent that; without the variable it starts as usual (production
	// behavior unchanged).
	notifyBackgroundDisabledEnv = "ARTEX_NOTIFY_BACKGROUND_DISABLED"
	// notifyLease is the lease duration when claiming a delivery. It must be significantly
	// longer than the worst-case duration of a single delivery (the notify package's HTTP client
	// timeout of 15 seconds), otherwise the same row could be delivered by two dispatchers at once.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick caps the number of events dispatched per round, so that when a channel
	// is first enabled the whole historical backlog isn't expanded into delivery jobs all at once.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes is the default digest period.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick is the per-round delivery cap when a channel has no rate limit
	// set. It exists to prevent "one channel configured with no rate limit + one scan producing
	// thousands of vulnerabilities" from dragging a single loop round into a long block.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick is the maximum number of deliveries per channel per round.
	//
	// This cap is derived backwards from the **lease duration**: a claimed row is given a lease
	// (notifyLease = 3 minutes), and if a round serially delivers so many items that the
	// worst-case duration exceeds the lease, the leases of the last few expire before they are
	// sent. Within a single process it doesn't matter (Run is a single goroutine running
	// serially, and tick never re-enters), but when **two processes share the same database**,
	// the other side will re-claim rows whose lease expired and send them again, also
	// incrementing attempts twice and judging them failed while the original process is still
	// delivering.
	//
	// Choice of value: 3-minute lease / 30-second per-send timeout = 6 **exactly uses up the
	// lease** with zero margin, so it can't be used; 5 makes the worst case 150 seconds, leaving
	// a 30-second margin. This relationship is pinned by TestNotifyTickBudgetFitsWithinLease —
	// changing any one of notifyLease, notifySendTimeout or this value makes that assertion fail.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout is the timeout of a single delivery. It also determines the value of the
	// previous constant; their product must not exceed notifyLease, see
	// TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff is the backoff sequence for failure retries, indexed by the number of
// attempts made. 3 chances (including the first) correspond to db.MaxNotifyAttempts; the two
// must be changed together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Delivery failure · delay reasons. Stored in notification_deliveries.last_error and shown
// verbatim, via notify_api.go's delivery-history response, in the notifications page's
// 「전달 기록」 (delivery history) table (delivery-list.tsx). They are therefore user-facing
// text and kept in Korean (the diagnostic log.Printf calls keep their originals, Z2).
const (
	errDeliveryChannelKindUnregistered = "등록되지 않은 채널 유형입니다: %q"
	errDeliverySnapshotUnrenderable    = "이벤트 스냅샷을 해석하지 못해 이 취약점을 메시지로 만들지 못했습니다"
	errDeliveryChannelLengthCapped     = "이 메시지가 채널 길이 상한에 도달해 앞의 %d건만 전송했고, 나머지는 다음 배치로 미룹니다"
	errDeliveryNoDeliveredCount        = "채널이 전송 건수를 보고하지 않았습니다(delivered=%d)"
	errDeliveryRetryExhausted          = "%d회 재시도 후에도 실패했습니다: %s"
	errDeliverySnapshotEmpty           = "전달 항목 #%d 의 이벤트 스냅샷이 비어 있습니다"
	errDeliverySnapshotParse           = "전달 항목 #%d 의 이벤트 스냅샷을 해석하지 못했습니다: %w"
	errDeliveryBatchAllUnparseable     = "요약 배치의 전달 %d건을 모두 해석하지 못했습니다"
)

// Notifier is the delivery engine for vulnerability push notifications.
//
// Runs alongside the Scheduler as an independent goroutine (see server.New). It deliberately
// does not reuse the Scheduler's tick: the latency requirement of pushes (3 seconds) differs
// from the business rhythm of triggers, and their failures stay independent of each other —
// a stuck push should not affect agent triggers.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu protects buckets. There are few channels and little contention, so one mutex is
	// enough; a finer-grained structure isn't worth introducing for it.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket is a per-channel token bucket.
//
// A token bucket is used rather than a "count per minute, then reset" sliding window because
// the latter has bad boundary effects: sending 20 at the end of a window and another 20 the
// next instant is 40 within one second as far as the platform is concerned, and gets rate
// limited; a token bucket refills at a constant rate and naturally avoids such bursts.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run loops until ctx ends. Started once by server.New.
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

// step runs one round: first dispatch new events, then deliver due jobs.
//
// Any step that fails only gets logged and does not interrupt the loop — a notification
// system failure must never escalate into a process-level problem. Every tick is
// independent, and the next round naturally retries.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] failed to dispatch events: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] failed to read channels: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// The token bucket's unit is the **number of messages** (equivalent to the number of HTTP
		// requests), not the number of vulnerabilities. In realtime mode the two are the same (one
		// message per vulnerability); in digest mode a whole batch of vulnerabilities is combined
		// into one message, so it consumes only one token.
		//
		// Both modes ask the token bucket first and then claim according to the allowance — the
		// order can't be reversed, otherwise deliveries blocked by rate limiting would already have
		// used up retry attempts.
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

// digestTickPlan returns the digest channel's token consumption for this round and the upper bound on batch size.
//
// The two return values are **two different dimensions**, which is exactly why this is a separate function:
//
//   - tokens is the number of messages. A batch of vulnerabilities is combined into one
//     message and sent as one HTTP request, so it is always 1.
//     rate_per_min therefore still applies to digest (at most that many digest messages per minute).
//   - claimLimit is the maximum number of vulnerabilities this batch holds. It is bound only by
//     the memory upper bound, unrelated to the request budget.
//
// To make rate_per_min apply to digest, the per-round request budget
// (notifyMaxSendsPerChannelPerTick, derived backwards from the lease) was once passed down
// directly as the batch size. The result was that a channel with rate_per_min=20 refilled
// only 1 token per 3-second tick, so each digest message held just 1 vulnerability — digest
// degenerated into "realtime pushes with digest wording", readers received a string of
// "1 new vulnerability in the last 30 minutes", and db.MaxDigestBatchSize was never reachable.
//
// This symptom is hard to spot in end-to-end tests (the existing cases all manually pass a
// large enough limit to stepDigest, bypassing the allowance calculation in step), so the
// decision is gathered here and pinned directly by
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime claims and delivers a channel's realtime jobs, one message per vulnerability.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] failed to claim realtime deliveries channel=%d: %v", ch.ID, err)
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
			// A render failure is a local data problem; retrying won't make it better.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest aggregates a channel's pending deliveries into one message and sends it when the batch is due.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] failed to evaluate digest batch channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] failed to claim digest batch channel=%d: %v", ch.ID, err)
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
	// Deliveries whose snapshot is broken and that didn't make it into the message must be
	// explicitly marked failed. Otherwise they would be left outside included, in neither the
	// message nor the failure list — on a successful send the subsequent bulk marking would miss
	// them, and they would sit in sending forever until their lease expired and they got
	// claimed again and again.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := errDeliverySnapshotUnrenderable
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] failed to mark deliveries with bad snapshots channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] skipped %d deliveries with unparsable snapshots channel=%d", len(skipped), ch.ID)
	}
	// Hand only the ones that made it into the message to send: included[i] corresponds strictly
	// to msg.Items[i], and send relies on this correspondence to apply "the channel reported
	// fitting the first K items" to the right delivery rows.
	n.send(ctx, channel, cfg, msg, included)
}

// send delivers and transitions the state according to the result.
//
// A batch of deliveries (possibly dozens in digest mode) shares a single send result: either
// delivered, or the whole batch is retried. No per-item retry — the digest is one message,
// and resending part of it would scramble the batch semantics.
//
// The only exception is **splitting caused by the channel's length limit**: if the channel
// reports it actually fit only the first K items, then items K+1 onwards must be held for the
// next batch rather than being marked successful along with the rest. Otherwise the truncated
// vulnerabilities would be in neither the message nor the failure list, and vanish entirely.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Cap a single delivery so that one stuck channel can't drag down all the remaining channels in this round.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// The count the channel reports can't exceed the number of deliveries; if it does, the
			// render layer miscalculated, so treat it as fully delivered and log the problem — better
			// than corrupting the records.
			log.Printf("[notify] channel reported %d delivered, more than the %d sent channel=%s; treating all as delivered",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] failed to mark as delivered channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// This message has hit the channel's length limit: requeue the rest immediately for the next
			// tick to continue sending. Use DeferDeliveries rather than RescheduleDeliveries — this is
			// not a failure and shouldn't consume retry budget (it was optimistically +1'd at claim
			// time; that gets subtracted back there).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf(trMsg(errDeliveryChannelLengthCapped), delivered)); err != nil {
				log.Printf("[notify] failed to queue remaining segments channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// The channel neither reported an error nor said how many items were delivered. Treat it as
		// a failure (go through backoff), so this delivery doesn't get claimed over and over without
		// ever being marked done.
		err = fmt.Errorf(trMsg(errDeliveryNoDeliveredCount), delivered)
	}

	// The failure handling is decided **per item**, not by judging on the whole batch's maximum attempt count.
	//
	// It used to be `if maxAttempts(deliveries) >= MaxNotifyAttempts`, killing the whole batch,
	// but the attempt counts of items within a batch differ: an old delivery already retried
	// twice (attempts=2) would drag brand-new deliveries (attempts=1) in the same batch into
	// failed with it — new vulnerabilities would be lost permanently without getting a single
	// retry, the exact opposite of the intent "don't let old rows drag new rows down".
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
			log.Printf("[notify] failed to mark failure state channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf(trMsg(errDeliveryRetryExhausted), db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] failed to mark failure state channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Regroup by delay: there are only 3 backoff tiers, so the number of groups is naturally
	// small, and there's no need to send a separate UPDATE for each item (that would make a
	// 500-item batch produce 500 round trips).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] failed to requeue deliveries channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] delivery failed channel=%d kind=%s permanent=%d retries_exhausted=%d pending_retry=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries returns the items in all that are not in keep (compared by pointer
// identity). Used to find deliveries that "didn't make it into the message" — they must be
// handled explicitly and can't be left in a gray zone.
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

// adapt fetches the channel implementation and parses its config.
// Returning ok=false means the type isn't registered, and the delivery should be failed
// outright rather than retried forever.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// On a config parse failure, return an empty map: the channel's own Validate will report
		// "which field is missing", and that error guides the user's fix better than a JSON parse error.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle renders a single-vulnerability message.
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

// renderBatch renders a digest message. Snapshots are parsed one by one — a single broken one
// only skips that item, rather than taking the whole digest down with it.
//
// The returned included corresponds **strictly one-to-one** with msg.Items (i-th delivery ↔
// i-th item). This correspondence is a hard requirement: the caller decides to mark the first
// K deliveries as delivered based on "the channel reported fitting the first K items". If a
// broken snapshot were skipped here without also removing the skipped delivery from included,
// the indices would shift — a broken item that should fail would be marked delivered, and a
// good item would be misjudged as undelivered. The broken ones are explicitly marked failed
// by the caller; see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// A broken snapshot goes into neither the message nor included — its handling is the
			// caller's responsibility (explicitly marked failed, rather than slipping through mixed in
			// with "delivered").
			log.Printf("[notify] skipped unparsable snapshot in digest batch delivery=%d: %v", dl.ID, err)
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

// itemFor renders the event snapshot into an item to push, resolving the asset name and details back-link along the way.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// Failing to resolve the asset name shouldn't block the push: not being able to read a name
		// is far less bad than not getting a notification — the message just lacks the asset line.
		log.Printf("[notify] failed to resolve asset name finding=%d: %v", snap.FindingID, err)
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
		// The details page route is web/src/app/(main)/function/findings/detail/page.tsx, which reads
		// the vulnerability id from the id query parameter.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens takes **at most want** tokens from the channel's token bucket and returns how many it actually got.
//
// One token = one message (one HTTP request). In realtime mode the caller passes however many
// it wants; in digest mode a whole batch of vulnerabilities sends just one message, so it passes 1.
//
// The bucket capacity is the channel's per-minute cap, refilled at a constant rate.
// ratePerMin<=0 means no rate limit, and returns a finite but large enough value so a single
// loop round can't be held up by an unbounded backlog.
//
// The want cap is necessary: without it the only option is to drain the whole bucket, but
// the caller has its own per-round cap too, so the extra tokens taken can't be used and
// simply vanish before the next refill — the saved-up burst capacity would never be
// reachable, and even "no pending deliveries at all this round" would still be charged.
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
	// Refill by the real elapsed time, at a rate of ratePerMin/60 per second.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add a tiny epsilon before truncating: the token count is accumulated in floating point,
	// and filling up in two steps, 0.5 + 0.5 may give 0.9999999999, which a plain int() truncates
	// to 0 — a bucket that is mathematically full would yield no token. 1e-9 is far smaller than
	// one token and won't let a real deficit through.
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

// publicBaseURL returns the external address used for back-links, with the trailing slash removed.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval returns the digest period, falling back to the default when invalid or unconfigured.
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

// parseSnapshot parses the snapshot of the event a delivery corresponds to.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf(trMsg(errDeliverySnapshotEmpty), dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf(trMsg(errDeliverySnapshotParse), dl.ID, err)
	}
	if snap.Kind == "" {
		// The event type is taken from the event row; the copy in the snapshot may have been written by an older version.
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
