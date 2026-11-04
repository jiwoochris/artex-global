package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// This file covers the notification push feature end to end: finding persisted ->
// event -> fan-out -> a real HTTP send.
//
// One security caveat: these cases **do not call the global Notifier.step()**, they only
// call stepRealtime/stepDigest on the channels they created themselves. step() walks
// every enabled channel in the database, so on a dev database already wired to real
// DingTalk/WeCom robots a global step would push findings produced by the test into
// those real groups. Per-channel calls keep the blast radius strictly inside the fake
// receivers the test creates.
//
// Cleanup: each case deletes the events it produced (cascading to deliveries) and its
// channels, so no backlog is left behind on real channels.
//
// Assertion stance: stepRealtime/stepDigest return nothing and only log internally, so
// what is asserted here is **observable external behaviour** (what the fake receiver
// got, what state the delivery rows ended in) rather than a return value - closer to
// the real call path than stubbing a return value would be.

// notifyFixture is the shared harness for the cases in this file.
type notifyFixture struct {
	s       *Server
	pg      *db.DB
	request func(method, path, body string) *httptest.ResponseRecorder
	n       *Notifier
	// Own task/exploration: cases record findings here, isolated from other cases' data.
	taskID int64
	expID  int64
	// Events created after cleanupMark are deleted along with everything else at cleanup.
	cleanupMark int64
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	// Every fake receiver in this file runs on 127.0.0.1, and delivery rejects loopback
	// addresses by default (SSRF guard against same-host services and cloud metadata).
	// The test flips that switch explicitly; the "reject by default" behaviour itself is
	// covered by the notify package's ssrf_test.go.
	t.Setenv(notify.AllowLocalTargetsEnv, "1")
	// Disable the background delivery loop (3s tick). The cases in this file call
	// stepRealtime/stepDigest directly to verify the outcome of a single dispatch (the
	// first K delivered, the rest deferred); if the background loop worked the same
	// channel concurrently the counts would wobble with timing (flaky on slow CI) and
	// determinism is lost. This variable only takes effect if it is set before
	// trafficEvidenceServer stands the server up.
	t.Setenv(notifyBackgroundDisabledEnv, "1")
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg

	// Create an own task: the task built by the shared trafficEvidenceServer harness has
	// no exploration id, and recording a finding requires one.
	task, err := s.m.CreateTask("notification push test", "delivery behaviour verification", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM tasks WHERE id=$1`, taskID) })

	var mark int64
	if err := pg.QueryRow(`SELECT COALESCE(max(id),0) FROM notification_events`).Scan(&mark); err != nil {
		t.Fatal(err)
	}
	// Make the harness self-contained: mark every event that existed before the fixture
	// was built as already fanned out.
	//
	// Why this is required: FanOutPendingEvents is **global** and expands every pending
	// event in the database onto every matching channel. The shared trafficEvidenceServer
	// harness records a finding of its own (the initial finding it returns) and other
	// cases may leave residue. Without this isolation those stray events get fanned out
	// onto this case's channels, making assertions like "there should be N deliveries"
	// pass or fail depending on execution order - harder to debug than a plain failure.
	if _, err := pg.Exec(`UPDATE notification_events SET fanned_out = true WHERE id <= $1 AND NOT fanned_out`, mark); err != nil {
		t.Fatal(err)
	}

	f := &notifyFixture{s: s, pg: pg, request: request, n: newNotifier(s), taskID: taskID, expID: task.ExpID, cleanupMark: mark}
	t.Cleanup(func() {
		if _, err := pg.Exec(`DELETE FROM notification_events WHERE id > $1`, f.cleanupMark); err != nil {
			t.Logf("failed to clean up notification events: %v", err)
		}
	})
	// The master switch must be on (other cases may have turned it off).
	if err := pg.SetBool(settingNotifyEnabled, true); err != nil {
		t.Fatal(err)
	}
	return f
}

// record persists a finding through the real evidence write path and returns the finding
// id. That path registers the push event in the **same transaction** - the exact hook this
// feature hangs on.
func (f *notifyFixture) record(t *testing.T, vulnclass, severity string) int64 {
	t.Helper()
	out, err := f.s.evidenceStore().Record(context.Background(), db.RecordFindingInput{
		TaskID:        f.taskID,
		ExplorationID: f.expID,
		Worker:        "test",
		VulnClass:     vulnclass,
		Name:          vulnclass,
		Severity:      severity,
		Summary:       "summary of " + vulnclass,
		Evidence:      "poc",
	}, nil)
	if err != nil {
		t.Fatalf("failed to record finding: %v", err)
	}
	return out.FindingID
}

// channel reads a channel's config back (used to call stepX per channel).
func (f *notifyFixture) channel(t *testing.T, id int64) *db.NotificationChannel {
	t.Helper()
	ch, err := f.pg.NotificationChannelByID(context.Background(), id)
	if err != nil {
		t.Fatalf("failed to read channel: %v", err)
	}
	return ch
}

// deliver fans events out and runs one delivery round for the given channel only.
func (f *notifyFixture) deliver(t *testing.T, chID int64, baseURL string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatalf("fan-out failed: %v", err)
	}
	f.n.stepRealtime(ctx, f.channel(t, chID), 50, baseURL)
}

// createChannel creates a channel through the HTTP endpoint, which also exercises the
// endpoint's own validation path.
func (f *notifyFixture) createChannel(t *testing.T, payload map[string]any) int64 {
	t.Helper()
	raw, _ := json.Marshal(payload)
	r := f.request("POST", "/api/notify/channels", string(raw))
	if r.Code != 200 {
		t.Fatalf("failed to create channel %d: %s", r.Code, r.Body)
	}
	var res struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &res); err != nil || res.ID == 0 {
		t.Fatalf("unexpected create-channel response: %s (%v)", r.Body, err)
	}
	t.Cleanup(func() { f.pg.Exec(`DELETE FROM notification_channels WHERE id=$1`, res.ID) })
	return res.ID
}

// fakeWebhook is a fake receiver that records the request bodies it receives.
type fakeWebhook struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newFakeWebhook(t *testing.T) *fakeWebhook {
	t.Helper()
	f := &fakeWebhook{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWebhook) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fakeWebhook) body(t *testing.T, i int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.bodies) {
		t.Fatalf("fake receiver got only %d requests, cannot take request %d", len(f.bodies), i)
	}
	return f.bodies[i]
}

func (f *fakeWebhook) last(t *testing.T) map[string]any {
	t.Helper()
	if f.count() == 0 {
		t.Fatal("the fake receiver got no request at all")
	}
	return f.body(t, f.count()-1)
}

// markdownText extracts the body text from a request body, tolerating the per-platform
// field name differences: DingTalk markdown uses `text`, ActionCard uses `text`, WeCom
// markdown uses `content`.
func markdownText(t *testing.T, body map[string]any) string {
	t.Helper()
	for _, key := range []string{"markdown", "actionCard"} {
		section, ok := body[key].(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"text", "content"} {
			if s, ok := section[field].(string); ok && s != "" {
				return s
			}
		}
	}
	t.Fatalf("no recognisable body text in the request: %v", body)
	return ""
}

// agePendingBatch backdates the channel's pending deliveries so the digest batch looks due.
func (f *notifyFixture) agePendingBatch(t *testing.T, chID int64) {
	t.Helper()
	if _, err := f.pg.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '2 hours'
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyEndToEndRealtimeDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "realtime push",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "SQL injection", "high")
	f.deliver(t, chID, "")

	if hook.count() != 1 {
		t.Fatalf("expected exactly 1 message, got %d", hook.count())
	}
	text := markdownText(t, hook.last(t))
	// "SQL injection" is the vulnerability title fed in, so the card echoes it verbatim
	// (user data, not translated). "High" and "Summary" are render labels: the severity
	// comes from notify.SeverityLabel("high") ("🟠 High") and the summary header from the
	// "**Summary**:" prefix writeItem attaches.
	for _, want := range []string{"SQL injection", "🟠 High", "**Summary**:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("message body is missing %q:\n%s", want, text)
		}
	}
	// The delivery must transition to sent.
	var pending int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state <> $2`,
		chID, db.NotifyStateSent).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d deliveries are still not marked sent", pending)
	}
}

func TestNotifyChannelAPIMasksSecretsAndPreservesOnUpdate(t *testing.T) {
	f := newNotifyFixture(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "masking case",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc123456", "secret": "SECabcdef123456"},
	})

	r := f.request("GET", "/api/notify/channels", "")
	if r.Code != 200 {
		t.Fatalf("failed to list channels %d: %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "abc123456") || strings.Contains(r.Body.String(), "SECabcdef123456") {
		t.Fatalf("the endpoint echoed credentials back: %s", r.Body)
	}
	var listed struct {
		Channels []struct {
			ID         int64          `json:"id"`
			Config     map[string]any `json:"config"`
			SecretKeys []string       `json:"secret_keys"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var mine *struct {
		ID         int64          `json:"id"`
		Config     map[string]any `json:"config"`
		SecretKeys []string       `json:"secret_keys"`
	}
	for i := range listed.Channels {
		if listed.Channels[i].ID == chID {
			mine = &listed.Channels[i]
		}
	}
	if mine == nil {
		t.Fatal("the newly created channel is missing from the list")
	}
	if !notify.IsMasked(fmt.Sprint(mine.Config["webhook"])) || !notify.IsMasked(fmt.Sprint(mine.Config["secret"])) {
		t.Fatalf("credential fields must be masked: %v", mine.Config)
	}
	if len(mine.SecretKeys) == 0 {
		t.Fatal("the endpoint must tell the front end which fields are credentials")
	}

	// PATCH renames only and echoes the masked credentials: the real ones must survive as-is.
	body, _ := json.Marshal(map[string]any{
		"name":   "after rename",
		"config": map[string]any{"webhook": fmt.Sprint(mine.Config["webhook"]), "secret": fmt.Sprint(mine.Config["secret"])},
	})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("update failed %d: %s", r.Code, r.Body)
	}
	cfg := f.channelConfig(t, chID)
	if cfg["webhook"] != "https://oapi.dingtalk.com/robot/send?access_token=abc123456" {
		t.Fatalf("the masked echo overwrote the real credential: %v", cfg["webhook"])
	}
	if cfg["secret"] != "SECabcdef123456" {
		t.Fatalf("the masked echo overwrote the secret: %v", cfg["secret"])
	}
	if f.channel(t, chID).Name != "after rename" {
		t.Fatal("the name was not updated")
	}

	// Explicitly clearing the secret must take effect (unlike echoing the mask, which means
	// "keep unchanged").
	body, _ = json.Marshal(map[string]any{"config": map[string]any{"secret": ""}})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("failed to clear the secret %d: %s", r.Code, r.Body)
	}
	if _, still := f.channelConfig(t, chID)["secret"]; still {
		t.Fatal("an empty string must clear the secret")
	}
}

func (f *notifyFixture) channelConfig(t *testing.T, id int64) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(f.channel(t, id).Config, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNotifyChannelAPICreateValidation(t *testing.T) {
	f := newNotifyFixture(t)
	cases := []struct {
		name    string
		payload map[string]any
		wantSub string
	}{
		// wantSub matches the English validation errors server/notify_api.go emits (the
		// stable wording of notifyErrKindInvalidFmt, notifyErrNameMissing and
		// notifyErrModeInvalid). The two webhook cases are errors produced by Validate in
		// notify/dingtalk.go; that wording is English too (checked as "The webhook URL is
		// missing" / "The webhook URL is invalid" by the notify package's own tests), so the
		// expected strings match it as well.
		{"invalid kind", map[string]any{"name": "x", "kind": "nope", "config": map[string]any{}}, "Invalid channel type"},
		{"missing name", map[string]any{"kind": notify.KindDingTalk, "config": map[string]any{"webhook": "https://e.com/h"}}, "Enter a channel name"},
		{"missing webhook", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{}}, "The webhook URL is missing"},
		{"invalid webhook scheme", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{"webhook": "file:///etc/passwd"}}, "The webhook URL is invalid"},
		{"invalid mode", map[string]any{"name": "x", "kind": notify.KindDingTalk, "mode": "sometimes", "config": map[string]any{"webhook": "https://e.com/h"}}, "Invalid delivery mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.payload)
			r := f.request("POST", "/api/notify/channels", string(raw))
			if r.Code != 400 {
				t.Fatalf("expected 400, got %d: %s", r.Code, r.Body)
			}
			if !strings.Contains(r.Body.String(), tc.wantSub) {
				t.Fatalf("the error message should mention %q, got %s", tc.wantSub, r.Body)
			}
		})
	}
	if r := f.request("DELETE", "/api/notify/channels/99999999", ""); r.Code != 404 {
		t.Fatalf("deleting a non-existent channel should return 404, got %d", r.Code)
	}
}

func TestNotifyFilterBlocksBelowThreshold(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "critical only",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"min_severity": "critical"},
	})
	f.record(t, "low risk issue", "low")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a finding below the threshold must not produce a delivery, got %d", n)
	}
	f.n.stepRealtime(context.Background(), f.channel(t, chID), 50, "")
	if hook.count() != 0 {
		t.Fatal("a filtered finding must not be sent")
	}
}

func TestNotifyDigestBatchesMultipleFindingsIntoOneMessage(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "digest push",
		"kind":   notify.KindDingTalk,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	for i := 0; i < 3; i++ {
		f.record(t, fmt.Sprintf("digest finding %d", i+1), "high")
	}
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)

	// Not due yet: nothing is sent.
	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count() != 0 {
		t.Fatal("the digest batch was sent before it was due")
	}

	// After backdating the batch: the three items collapse into one message.
	f.agePendingBatch(t, chID)
	f.n.stepDigest(ctx, ch, 50, "")
	if got := hook.count(); got != 1 {
		t.Fatalf("three items should collapse into one message, %d were sent", got)
	}
	text := markdownText(t, hook.last(t))
	// A digest header with a time window is rendered by markdown.go as
	// "**3 new vulnerabilities in the last N minutes**" (digestInterval defaults to 30
	// minutes, so WindowMinutes > 0).
	if !strings.Contains(text, "new vulnerabilities in the last") || !strings.Contains(text, "3 new vulnerabilities") {
		t.Fatalf("the digest message is missing the count / window text:\n%s", text)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(text, fmt.Sprintf("digest finding %d", i)) {
			t.Fatalf("the digest message is missing item %d:\n%s", i, text)
		}
	}
	// Items of the same batch must share batch_id.
	var distinct, total int
	if err := f.pg.QueryRow(`SELECT count(DISTINCT batch_id), count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&distinct, &total); err != nil {
		t.Fatal(err)
	}
	if total != 3 || distinct != 1 {
		t.Fatalf("the three deliveries should share one batch_id, got distinct=%d total=%d", distinct, total)
	}
}

func TestNotifyDisabledChannelDoesNotSend(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":    "disabled channel",
		"kind":    notify.KindDingTalk,
		"enabled": false,
		"config":  map[string]any{"webhook": hook.URL},
	})
	f.record(t, "finding while disabled", "critical")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a disabled channel must not produce deliveries, got %d", n)
	}
}

func TestNotifyStatusChangeDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "status change subscription",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"on_status_change": true},
	})
	finding := f.record(t, "status change case", "high")
	r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"fixed"}`)
	if r.Code != 200 {
		t.Fatalf("failed to change the status %d: %s", r.Code, r.Body)
	}
	f.deliver(t, chID, "")

	// There should be two: the "fixed" one is the status change; the finding_created one may
	// also go out in the same round. The status-change card is actually created later, but
	// the check does not rely on order - it scans everything.
	found := false
	for i := 0; i < hook.count(); i++ {
		text := markdownText(t, hook.body(t, i))
		// markdown.go renders the status-change card as "**Status change**: %s → %s", with
		// the status values coming from notify.StatusLabel (fixed -> "Fixed").
		if strings.Contains(text, "**Status change**") && strings.Contains(text, "Fixed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no message containing \"Status change -> Fixed\" was received (%d in total)", hook.count())
	}
}

func TestNotifyStatusChangeSuppressedByDefault(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "no status change subscription",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "no change subscription", "high")
	if r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"false_positive"}`); r.Code != 200 {
		t.Fatalf("failed to change the status %d: %s", r.Code, r.Body)
	}
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
WHERE d.channel_id=$1 AND e.kind=$2`, chID, notify.EventFindingStatusChanged).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a channel not subscribed to status changes must not receive them, got %d", n)
	}
}

func TestNotifyTestMessageEndpoint(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "test send",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", chID), ""); r.Code != 200 {
		t.Fatalf("test send failed %d: %s", r.Code, r.Body)
	}
	if hook.count() != 1 {
		t.Fatalf("the fake receiver should get exactly 1 test message, got %d", hook.count())
	}
	// A test message must be recognisable as a test at a glance and must not be mistaken for
	// a real finding. Its title is notify_api.go's notifyTestName ("Test message · channel
	// configuration is healthy").
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "Test message") {
		t.Fatalf("the test message should say it is a test: %s", text)
	}
	// When the config is broken the channel's raw error goes back to the user verbatim.
	badID := f.createChannel(t, map[string]any{
		"name":   "bad address",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", badID), ""); r.Code != 502 {
		t.Fatalf("a failed delivery should return 502, got %d: %s", r.Code, r.Body)
	}
}

func TestNotifyDeliveriesHistoryAndRetry(t *testing.T) {
	f := newNotifyFixture(t)
	// Point at an address that always fails so a failed delivery is produced.
	chID := f.createChannel(t, map[string]any{
		"name":   "failing retry",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	f.record(t, "delivery that fails", "high")
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)
	// Keep sending until the retry budget is exhausted.
	for i := 0; i < db.MaxNotifyAttempts; i++ {
		f.n.stepRealtime(ctx, ch, 50, "")
		if _, err := f.pg.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, chID); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := f.pg.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != db.NotifyStateFailed {
		t.Fatalf("state should be failed once retries are exhausted, got %s", state)
	}

	r := f.request("GET", fmt.Sprintf("/api/notify/deliveries?channel_id=%d&state=failed", chID), "")
	if r.Code != 200 {
		t.Fatalf("failed to read the history %d: %s", r.Code, r.Body)
	}
	var hist struct {
		Deliveries []struct {
			ID        int64  `json:"id"`
			State     string `json:"state"`
			LastError string `json:"last_error"`
			Attempts  int    `json:"attempts"`
			Title     string `json:"title"`
		} `json:"deliveries"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if hist.Total != 1 || len(hist.Deliveries) != 1 {
		t.Fatalf("expected exactly 1 failed delivery, got total=%d len=%d", hist.Total, len(hist.Deliveries))
	}
	if hist.Deliveries[0].LastError == "" {
		t.Fatal("the history must carry the failure reason, otherwise users cannot debug it")
	}
	if hist.Deliveries[0].Attempts < db.MaxNotifyAttempts {
		t.Fatalf("the attempt count must be recorded, got %d", hist.Deliveries[0].Attempts)
	}
	if hist.Deliveries[0].Title != "delivery that fails" {
		t.Fatalf("the history should surface the finding title, got %q", hist.Deliveries[0].Title)
	}

	// Manual resend: the row must go back to pending with a zeroed attempt count.
	if r := f.request("POST", fmt.Sprintf("/api/notify/deliveries/%d/retry", hist.Deliveries[0].ID), ""); r.Code != 200 {
		t.Fatalf("resend failed %d: %s", r.Code, r.Body)
	}
	var attempts int
	if err := f.pg.QueryRow(`SELECT state, attempts FROM notification_deliveries WHERE id=$1`, hist.Deliveries[0].ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != db.NotifyStatePending || attempts != 0 {
		t.Fatalf("after a resend the state should be pending with attempts=0, got %s/%d", state, attempts)
	}
}

func TestNotifyMetaAndSettingsRoundTrip(t *testing.T) {
	f := newNotifyFixture(t)
	r := f.request("GET", "/api/notify/meta", "")
	if r.Code != 200 {
		t.Fatalf("meta request failed: %s", r.Body)
	}
	var meta struct {
		Kinds []struct {
			Kind       string   `json:"kind"`
			SecretKeys []string `json:"secret_keys"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Kinds) != len(notify.Kinds()) {
		t.Fatalf("meta should list all %d channel kinds, got %d", len(notify.Kinds()), len(meta.Kinds))
	}
	for _, k := range meta.Kinds {
		if len(k.SecretKeys) == 0 {
			t.Errorf("channel kind %s reports no credential fields", k.Kind)
		}
	}

	// Round-trip the three global settings. A trailing slash must be normalised away,
	// otherwise deep links come out as "//function/...".
	if r := f.request("PUT", "/api/settings", `{"notify_public_base_url":"https://artex.example.com/","notify_digest_interval_min":15,"notify_enabled":true}`); r.Code != 200 {
		t.Fatalf("failed to write settings %d: %s", r.Code, r.Body)
	}
	t.Cleanup(func() {
		f.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, settingNotifyPublicBaseURL, settingNotifyDigestMinutes)
	})
	payload := f.s.settingsPayload()
	if payload["notify_public_base_url"] != "https://artex.example.com" {
		t.Fatalf("the deep-link base URL was not normalised: %v", payload["notify_public_base_url"])
	}
	if payload["notify_digest_interval_min"] != 15 {
		t.Fatalf("the digest interval did not take effect: %v", payload["notify_digest_interval_min"])
	}

	// Invalid values must be rejected.
	for _, body := range []string{
		`{"notify_public_base_url":"ftp://x"}`,
		`{"notify_digest_interval_min":0}`,
		`{"notify_digest_interval_min":99999}`,
	} {
		if r := f.request("PUT", "/api/settings", body); r.Code != 400 {
			t.Errorf("%s should return 400, got %d", body, r.Code)
		}
	}
}

// TestNotifyDeepLinkUsesPublicBaseURL covers deep-link assembly: with public_base_url set,
// a single-item message must use an ActionCard with a button, and the link must point at the
// finding detail page.
func TestNotifyDeepLinkUsesPublicBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "deep link",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "finding with deep link", "high")
	f.deliver(t, chID, "https://artex.example.com")

	body := hook.last(t)
	card, _ := body["actionCard"].(map[string]any)
	if card == nil {
		t.Fatalf("an ActionCard is expected when a deep link is configured, got msgtype=%v", body["msgtype"])
	}
	want := fmt.Sprintf("https://artex.example.com/function/findings/detail?id=%d", finding)
	if card["singleURL"] != want {
		t.Fatalf("wrong deep link\nwant %s\ngot  %v", want, card["singleURL"])
	}
}

// TestNotifyNoDeepLinkWithoutBaseURL covers the reverse: with no external address
// configured, no broken link may be produced (for example one pointing at localhost or a
// relative path); the message falls back to plain markdown.
func TestNotifyNoDeepLinkWithoutBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "no deep link",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "finding without deep link", "high")
	f.deliver(t, chID, "")

	body := hook.last(t)
	if body["msgtype"] != "markdown" {
		t.Fatalf("plain markdown is expected without an external address, got %v", body["msgtype"])
	}
	// markdown.go renders the detail link as "[View details](URL)"; without an external
	// address that link must not appear at all. (Checking for the old Chinese label would
	// now always pass and catch no regression, since the label is English.)
	if text := markdownText(t, body); strings.Contains(text, "View details") {
		t.Fatalf("no detail link may appear without an external address:\n%s", text)
	}
}

// TestNotifyDigestSegmentsAndDefersRemainder is the end-to-end evidence for the
// "silently dropped" fix.
//
// Digest messages are bounded by the channel's length limit (4096 bytes for WeCom), so a
// batch that does not fit must be split **on whole items**: the ones that fit are marked
// delivered and the rest go back on the queue for the next message. The old implementation
// marked the whole batch successful - the truncated items were neither in the message nor in
// the failure list, delivery history still said success, and the findings were simply gone.
//
// Four things are asserted: (1) only the items that actually fit are marked sent, (2) the
// rest stay pending, (3) deferred items **do not consume retry budget**, (4) another round
// sends the remainder (nothing gets stuck).
func TestNotifyDigestSegmentsAndDefersRemainder(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	// Use WeCom: its markdown limit is 4096 bytes, the tightest of the six channels.
	chID := f.createChannel(t, map[string]any{
		"name":   "segmented digest",
		"kind":   notify.KindWeCom,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	const total = 60
	// Use a long title so 60 items far exceed 4096 bytes and segmentation is guaranteed.
	longName := strings.Repeat("very long vulnerability name", 6)
	for i := 0; i < total; i++ {
		f.record(t, longName+strconv.Itoa(i+1), "high")
	}
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	f.agePendingBatch(t, chID)
	ch := f.channel(t, chID)

	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count() != 1 {
		t.Fatalf("exactly one message should be sent, got %d", hook.count())
	}

	var sent, pending int
	if err := f.pg.QueryRow(`SELECT
    count(*) FILTER (WHERE state=$2),
    count(*) FILTER (WHERE state=$3)
  FROM notification_deliveries WHERE channel_id=$1`, chID, db.NotifyStateSent, db.NotifyStatePending).
		Scan(&sent, &pending); err != nil {
		t.Fatal(err)
	}
	if sent == 0 {
		t.Fatal("some items should have been marked delivered")
	}
	if pending == 0 {
		t.Fatalf("a batch of %d cannot fit in 4096 bytes, some must stay pending; sent=%d", total, sent)
	}
	if sent+pending != total {
		t.Fatalf("item count mismatch: sent=%d pending=%d total=%d (neither sent nor pending = lost)", sent, pending, total)
	}
	// The message body must state truthfully how many items are not in this message.
	// markdown.go renders the segmentation notice as "(this message shows only the first N;
	// the remaining M follow in the next message)".
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "remaining") {
		t.Fatalf("the message should say that some items are not included:\n%.400s", text)
	}

	// Deferred items must not consume retry budget: claiming optimistically bumps attempts by
	// 1, so deferring has to decrement it again.
	var maxAttempts int
	if err := f.pg.QueryRow(`SELECT COALESCE(max(attempts),0) FROM notification_deliveries
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending).Scan(&maxAttempts); err != nil {
		t.Fatal(err)
	}
	if maxAttempts > 0 {
		t.Fatalf("deferred items must not consume retries (otherwise they fail after a few rounds), got attempts=%d", maxAttempts)
	}

	// Loop until it converges. What is asserted is that **everything is eventually
	// delivered** and that several rounds really were needed - stronger than "the second
	// round sends everything", because it proves segmentation neither stalls nor drops the
	// remainder.
	rounds := 0
	for {
		var undelivered int
		if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries
WHERE channel_id=$1 AND state <> $2 AND state <> $3`, chID, db.NotifyStateSent, db.NotifyStateFailed).
			Scan(&undelivered); err != nil {
			t.Fatal(err)
		}
		if undelivered == 0 {
			break
		}
		rounds++
		if rounds > total+5 {
			t.Fatalf("segmented delivery does not converge: after %d rounds %d items are unresolved", rounds, undelivered)
		}
		before := hook.count()
		f.n.stepDigest(ctx, ch, 50, "")
		if hook.count() == before {
			t.Fatalf("round %d made no progress, the remaining %d items are stuck forever", rounds, undelivered)
		}
	}
	if rounds < 2 {
		t.Fatalf("one 4096-byte message cannot hold %d long-titled findings, so several rounds are needed, but only %d ran", total, rounds)
	}
	// Every round after the first is a **pure continuation**; no item is rejected by the channel.
	var failed int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state=$2`,
		chID, db.NotifyStateFailed).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("the fake receiver always succeeds, so no item may fail, got %d", failed)
	}
}

// TestNotifyBackoffTableMatchesAttemptBudget is an anti-drift assertion.
//
// The retry budget (db.MaxNotifyAttempts) and the backoff ladder (notifyBackoff) live in
// two different packages: the former is state-machine policy, the latter is the engine's
// pacing. Change only one of them - say raise the budget to 5 and forget to add a rung -
// and nothing errors; the 4th and 5th retries simply reuse the last interval, showing up as
// "retries got mysteriously slower", which is hard to trace back here.
// Asserting equal lengths exposes that drift in CI.
func TestNotifyBackoffTableMatchesAttemptBudget(t *testing.T) {
	if len(notifyBackoff) != db.MaxNotifyAttempts {
		t.Fatalf("backoff rungs (%d) and max attempts (%d) disagree - changing one requires changing the other",
			len(notifyBackoff), db.MaxNotifyAttempts)
	}
	// Backoff intervals must be non-decreasing, otherwise retries get faster over time and make
	// rate limiting worse.
	for i := 1; i < len(notifyBackoff); i++ {
		if notifyBackoff[i] < notifyBackoff[i-1] {
			t.Fatalf("backoff intervals must be non-decreasing: rung %d %v < rung %d %v",
				i, notifyBackoff[i], i-1, notifyBackoff[i-1])
		}
	}
}

// TestNotifyRateLimitDoesNotConsumeRetryBudget pins the "take tokens before claiming" order.
// If it were reversed (claim, then drop), a delivery stopped by the rate limiter would already
// have counted one attempt, the budget would be burnt on pure waiting and the row would end up
// in failed.
func TestNotifyRateLimitDoesNotConsumeRetryBudget(t *testing.T) {
	// Only the token bucket is under test; no Server is needed (and none should be built for it).
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// 1 per minute: a full bucket yields at most 1.
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now); got != 1 {
		t.Fatalf("a full bucket at 1/min should yield 1 token, got %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("an exhausted bucket must return 0 immediately, got %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(30*time.Second)); got != 0 {
		t.Fatalf("half the interval must not refill a whole token, got %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Minute)); got != 1 {
		t.Fatalf("a full interval should refill 1 token, got %d", got)
	}
	// An unlimited channel still gets a finite per-round cap so one round is never dragged down
	// by an unbounded backlog.
	if got := n.takeTokens(2, 0, notifyUnlimitedBurstPerTick+10, now); got != notifyUnlimitedBurstPerTick {
		t.Fatalf("an unlimited channel should yield the per-round cap %d, got %d", notifyUnlimitedBurstPerTick, got)
	}
	// Token buckets are independent per channel.
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("channel 1's bucket should still be empty, got %d", got)
	}
}

// TestNotifyTakeTokensKeepsUnusedTokens pins the "take only want" semantics.
//
// The old implementation drained the whole bucket and let the caller truncate afterwards, so
// a channel at rate=100/min that only used 5 items per round threw the other 95 tokens away;
// a round with nothing to send deducted them just the same. The upshot was that the documented
// "a backlog can flush rate_per_min items at once" never actually held.
func TestNotifyTakeTokensKeepsUnusedTokens(t *testing.T) {
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// The bucket starts full (100) and this round wants only 5.
	if got := n.takeTokens(1, 100, 5, now); got != 5 {
		t.Fatalf("want=5 should take exactly 5 tokens, got %d", got)
	}
	// Key assertion: the other 95 must still be in the bucket, not drained away.
	// Time is not advanced, so anything taken can only come from the stock, not a refill.
	if got := n.takeTokens(1, 100, 95, now); got != 95 {
		t.Fatalf("the remaining tokens should still be available (expected 95), got %d - the bucket was drained", got)
	}
	if got := n.takeTokens(1, 100, 1, now); got != 0 {
		t.Fatalf("the bucket is empty and must return 0, got %d", got)
	}
	// want<=0 must not deduct any token (an empty round is free).
	n2 := &Notifier{buckets: map[int64]*notifyBucket{}}
	if got := n2.takeTokens(1, 20, 0, now); got != 0 {
		t.Fatalf("want=0 should return 0, got %d", got)
	}
	if got := n2.takeTokens(1, 20, 20, now); got != 20 {
		t.Fatalf("the want=0 call must not consume tokens, 20 should still be available, got %d", got)
	}
}

// TestDigestTickPlanDecouplesBatchSizeFromSendBudget pins the two dimensions of digest mode.
//
// Once the digest batch size is tied to the per-round request budget, a channel with
// rate_per_min=20 can only accrue 1 token per 3-second tick, so every digest message carries a
// single finding - functionally there is no digest at all, while the message header still says
// "1 new vulnerability in the last 30 minutes". That regression raises no error and the
// existing end-to-end cases cannot see it (they pass stepDigest a large limit by hand, bypassing
// the quota arithmetic inside step), so the decision itself is asserted here.
func TestDigestTickPlanDecouplesBatchSizeFromSendBudget(t *testing.T) {
	tokens, claimLimit := digestTickPlan()
	// One batch = one message = one request = one token. Tokens are counted in messages, not findings.
	if tokens != 1 {
		t.Fatalf("a digest batch sends exactly one message, so it consumes exactly 1 token, got %d", tokens)
	}
	if claimLimit != db.MaxDigestBatchSize {
		t.Fatalf("the digest batch size should be the in-memory bound db.MaxDigestBatchSize=%d, got %d",
			db.MaxDigestBatchSize, claimLimit)
	}
	// Key relationship: the batch size must be far larger than the per-round request budget.
	// Once the two are the same order of magnitude, "how many messages to send" and "how many
	// findings fit in one batch" have been conflated into a single number again.
	if claimLimit <= notifyMaxSendsPerChannelPerTick {
		t.Fatalf("the digest batch size %d must not be constrained by the per-round request budget %d - "+
			"the request budget is derived backwards from the lease (how many requests to make) and is a different dimension from how many findings fit in a batch",
			claimLimit, notifyMaxSendsPerChannelPerTick)
	}
}

// TestNotifyTickBudgetFitsWithinLease is another anti-drift assertion.
//
// The per-channel, per-round delivery cap (notifyMaxSendsPerChannelPerTick) is derived
// backwards from the lease duration: the worst-case time to deliver one round serially must be
// < the lease, otherwise the lease expires before the last items are sent and, in a
// multi-instance deployment, a peer re-claims them and sends them again. The three constants
// sit in different places, so changing any one can break the relationship without any error -
// hence it is pinned here.
func TestNotifyTickBudgetFitsWithinLease(t *testing.T) {
	worst := time.Duration(notifyMaxSendsPerChannelPerTick) * notifySendTimeout
	if worst >= notifyLease {
		t.Fatalf("the worst-case duration of one channel round %v must stay below the lease %v"+
			" (notifyMaxSendsPerChannelPerTick=%d x notifySendTimeout=%v) - "+
			"changing any one of these three constants requires re-checking the other two",
			worst, notifyLease, notifyMaxSendsPerChannelPerTick, notifySendTimeout)
	}
}
