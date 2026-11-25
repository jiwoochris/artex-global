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

// This file covers the end-to-end behavior of the push feature: vulnerability persisted to the DB → event → dispatch → real HTTP send.
//
// A safety note: these cases **do not call the global Notifier.step()**; they only call
// stepRealtime/stepDigest on channels they created themselves. The reason is that step()
// iterates over every enabled channel in the database — running the tests against a dev
// database that already has real DingTalk/WeCom bots configured, a global step would push
// the vulnerabilities produced during the tests into those real groups. Calling per channel
// strictly limits the blast radius to the fake receivers the tests create themselves.
//
// Cleanup: at the end of a case, delete the events it produced (cascading to deliveries) and its channels, leaving no backlog for real channels.
//
// Assertion approach: stepRealtime/stepDigest return nothing and log internally, so what is
// asserted here is the **observable external behavior** (what the fake receiver got, what
// state the delivery rows ended up in) rather than the functions' return values — this is
// closer to the real call path than stubbing return values.

// notifyFixture is the shared fixture for the cases in this file.
type notifyFixture struct {
	s       *Server
	pg      *db.DB
	request func(method, path, body string) *httptest.ResponseRecorder
	n       *Notifier
	// A self-created task/exploration: cases record vulnerabilities here, isolated from other cases' data.
	taskID int64
	expID  int64
	// Events produced after cleanupMark are deleted together during cleanup.
	cleanupMark int64
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	// All the fake receivers in this file run on 127.0.0.1, while delivery rejects loopback
	// addresses by default (to prevent SSRF hitting same-host services and cloud metadata). The
	// tests explicitly turn this switch on; the "reject by default" guard is covered by the
	// notify package's ssrf_test.go.
	t.Setenv(notify.AllowLocalTargetsEnv, "1")
	// Turn off the background delivery loop (3-second tick). The cases in this file call
	// stepRealtime · stepDigest directly to verify the result of a single distribution (first K
	// items delivered · the rest held back), and if the background loop processes the same
	// channels concurrently, the counts fluctuate with timing (intermittent failures on slow
	// CI) and determinism breaks. This variable only takes effect if it is set before
	// trafficEvidenceServer brings up the server.
	t.Setenv(notifyBackgroundDisabledEnv, "1")
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg

	// Create our own task: the task built by the shared fixture trafficEvidenceServer doesn't
	// expose an exploration id, and recording a vulnerability requires one.
	task, err := s.m.CreateTask("通知推送测试", "推送行为验证", nil, 0, 0)
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
	// Make the fixture self-contained: mark all events that existed before the fixture was built as dispatched in one go.
	//
	// Why this is necessary: FanOutPendingEvents is **global** and expands every undispatched
	// event in the database onto every matching channel. The shared fixture
	// trafficEvidenceServer itself records a vulnerability (exactly the initial finding it
	// returns), and other cases may leave leftovers too. Without isolation, these stray events
	// would be dispatched to this case's channels, making assertions like "there should be N
	// deliveries" flaky — and how they fail depends on the order the cases run in, which is
	// harder to track down than a plain failure.
	if _, err := pg.Exec(`UPDATE notification_events SET fanned_out = true WHERE id <= $1 AND NOT fanned_out`, mark); err != nil {
		t.Fatal(err)
	}

	f := &notifyFixture{s: s, pg: pg, request: request, n: newNotifier(s), taskID: taskID, expID: task.ExpID, cleanupMark: mark}
	t.Cleanup(func() {
		if _, err := pg.Exec(`DELETE FROM notification_events WHERE id > $1`, f.cleanupMark); err != nil {
			t.Logf("清理通知事件失败: %v", err)
		}
	})
	// The master switch must be on (other cases may have turned it off).
	if err := pg.SetBool(settingNotifyEnabled, true); err != nil {
		t.Fatal(err)
	}
	return f
}

// record persists a vulnerability through the real evidence-write path and returns the
// finding id. This path registers the push event in the **same transaction** — which is
// exactly where this feature hooks in.
func (f *notifyFixture) record(t *testing.T, vulnclass, severity string) int64 {
	t.Helper()
	out, err := f.s.evidenceStore().Record(context.Background(), db.RecordFindingInput{
		TaskID:        f.taskID,
		ExplorationID: f.expID,
		Worker:        "test",
		VulnClass:     vulnclass,
		Name:          vulnclass,
		Severity:      severity,
		Summary:       vulnclass + " 的摘要",
		Evidence:      "poc",
	}, nil)
	if err != nil {
		t.Fatalf("记录漏洞失败: %v", err)
	}
	return out.FindingID
}

// channel reads back the channel config (for calling stepX per channel).
func (f *notifyFixture) channel(t *testing.T, id int64) *db.NotificationChannel {
	t.Helper()
	ch, err := f.pg.NotificationChannelByID(context.Background(), id)
	if err != nil {
		t.Fatalf("读渠道失败: %v", err)
	}
	return ch
}

// deliver dispatches events and runs one delivery round for the specified channel only.
func (f *notifyFixture) deliver(t *testing.T, chID int64, baseURL string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatalf("分派失败: %v", err)
	}
	f.n.stepRealtime(ctx, f.channel(t, chID), 50, baseURL)
}

// createChannel creates a channel through the HTTP API, covering the API's own validation paths along the way.
func (f *notifyFixture) createChannel(t *testing.T, payload map[string]any) int64 {
	t.Helper()
	raw, _ := json.Marshal(payload)
	r := f.request("POST", "/api/notify/channels", string(raw))
	if r.Code != 200 {
		t.Fatalf("建渠道失败 %d: %s", r.Code, r.Body)
	}
	var res struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &res); err != nil || res.ID == 0 {
		t.Fatalf("建渠道返回异常: %s (%v)", r.Body, err)
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
		t.Fatalf("假接收端只收到 %d 条请求，取不到第 %d 条", len(f.bodies), i)
	}
	return f.bodies[i]
}

func (f *fakeWebhook) last(t *testing.T) map[string]any {
	t.Helper()
	if f.count() == 0 {
		t.Fatal("假接收端没有收到任何请求")
	}
	return f.body(t, f.count()-1)
}

// markdownText extracts the body text from the request body, tolerating each vendor's
// different field names: DingTalk markdown uses `text`, ActionCard uses `text`, and WeCom
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
	t.Fatalf("请求体里没有可识别的正文: %v", body)
	return ""
}

// agePendingBatch ages the channel's pending deliveries, used to test digest batches coming due.
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
		"name":   "实时推送",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "SQL注入", "high")
	f.deliver(t, chID, "")

	if hook.count() != 1 {
		t.Fatalf("应发出 1 条消息，实际 %d", hook.count())
	}
	text := markdownText(t, hook.last(t))
	// "SQL注入" (SQL injection) is the vulnerability title given as input, so it is echoed onto
	// the card verbatim (user data, not a translation target). "높음" (high) · "개요" (overview)
	// are render labels — the severity is in "🟠 높음" produced by notify.SeverityLabel("high"),
	// and the summary heading is in "**개요**:" added by writeItem.
	for _, want := range []string{"SQL注入", "높음", "개요"} {
		if !strings.Contains(text, want) {
			t.Fatalf("消息正文缺少 %q:\n%s", want, text)
		}
	}
	// The delivery should transition to sent.
	var pending int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state <> $2`,
		chID, db.NotifyStateSent).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("投递后仍有 %d 条未标记 sent", pending)
	}
}

func TestNotifyChannelAPIMasksSecretsAndPreservesOnUpdate(t *testing.T) {
	f := newNotifyFixture(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "掩码用例",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc123456", "secret": "SECabcdef123456"},
	})

	r := f.request("GET", "/api/notify/channels", "")
	if r.Code != 200 {
		t.Fatalf("列渠道失败 %d: %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "abc123456") || strings.Contains(r.Body.String(), "SECabcdef123456") {
		t.Fatalf("接口回显泄露了凭据: %s", r.Body)
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
		t.Fatal("新建的渠道未出现在列表里")
	}
	if !notify.IsMasked(fmt.Sprint(mine.Config["webhook"])) || !notify.IsMasked(fmt.Sprint(mine.Config["secret"])) {
		t.Fatalf("凭据字段应为掩码值: %v", mine.Config)
	}
	if len(mine.SecretKeys) == 0 {
		t.Fatal("接口应告知前端哪些字段是凭据")
	}

	// PATCH only renames + sends back the masked credentials: the real credentials must be kept as-is.
	body, _ := json.Marshal(map[string]any{
		"name":   "改名后",
		"config": map[string]any{"webhook": fmt.Sprint(mine.Config["webhook"]), "secret": fmt.Sprint(mine.Config["secret"])},
	})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("更新失败 %d: %s", r.Code, r.Body)
	}
	cfg := f.channelConfig(t, chID)
	if cfg["webhook"] != "https://oapi.dingtalk.com/robot/send?access_token=abc123456" {
		t.Fatalf("掩码回传把真凭据覆盖了: %v", cfg["webhook"])
	}
	if cfg["secret"] != "SECabcdef123456" {
		t.Fatalf("掩码回传把 secret 覆盖了: %v", cfg["secret"])
	}
	if f.channel(t, chID).Name != "改名后" {
		t.Fatal("名字未更新")
	}

	// Explicitly clearing a secret should take effect (as opposed to "sending back the mask = keep unchanged").
	body, _ = json.Marshal(map[string]any{"config": map[string]any{"secret": ""}})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("清空 secret 失败 %d: %s", r.Code, r.Body)
	}
	if _, still := f.channelConfig(t, chID)["secret"]; still {
		t.Fatal("空串应清空 secret")
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
		// wantSub matches the Korean validation errors emitted by server/notify_api.go (the stable
		// messages of notifyErrKindInvalidFmt · notifyErrNameMissing · notifyErrModeInvalid). The
		// two webhook cases are errors produced by Validate in notify/dingtalk.go, and that webhook
		// address validation text has already been localized to Korean
		// (notify/dingtalk_feishu_wecom_localized_test.go checks it as "Webhook 주소가 없습니다"
		// (Webhook address missing) · "Webhook 주소가 올바르지 않습니다" (Webhook address invalid)), so
		// the expected strings are aligned with the stable Korean messages too.
		{"类型非法", map[string]any{"name": "x", "kind": "nope", "config": map[string]any{}}, "채널 유형이 올바르지 않습니다"},
		{"缺名称", map[string]any{"kind": notify.KindDingTalk, "config": map[string]any{"webhook": "https://e.com/h"}}, "채널 이름을 입력하세요"},
		{"缺 webhook", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{}}, "Webhook 주소가 없습니다"},
		{"webhook 协议非法", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{"webhook": "file:///etc/passwd"}}, "Webhook 주소가 올바르지 않습니다"},
		{"模式非法", map[string]any{"name": "x", "kind": notify.KindDingTalk, "mode": "sometimes", "config": map[string]any{"webhook": "https://e.com/h"}}, "발송 모드가 올바르지 않습니다"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.payload)
			r := f.request("POST", "/api/notify/channels", string(raw))
			if r.Code != 400 {
				t.Fatalf("应返回 400，得到 %d: %s", r.Code, r.Body)
			}
			if !strings.Contains(r.Body.String(), tc.wantSub) {
				t.Fatalf("错误信息应提到 %q，得到 %s", tc.wantSub, r.Body)
			}
		})
	}
	if r := f.request("DELETE", "/api/notify/channels/99999999", ""); r.Code != 404 {
		t.Fatalf("删除不存在的渠道应 404，得到 %d", r.Code)
	}
}

func TestNotifyFilterBlocksBelowThreshold(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "仅严重",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"min_severity": "critical"},
	})
	f.record(t, "低危问题", "low")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("低于阈值的漏洞不该产生投递，得到 %d 条", n)
	}
	f.n.stepRealtime(context.Background(), f.channel(t, chID), 50, "")
	if hook.count() != 0 {
		t.Fatal("被过滤的漏洞不应发出消息")
	}
}

func TestNotifyDigestBatchesMultipleFindingsIntoOneMessage(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "汇总推送",
		"kind":   notify.KindDingTalk,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	for i := 0; i < 3; i++ {
		f.record(t, fmt.Sprintf("汇总漏洞%d", i+1), "high")
	}
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)

	// Not yet due: nothing is sent.
	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count() != 0 {
		t.Fatal("汇总批次未到期就发了")
	}

	// After aging the batch: the three are combined into one message.
	f.agePendingBatch(t, chID)
	f.n.stepDigest(ctx, ch, 50, "")
	if got := hook.count(); got != 1 {
		t.Fatalf("三条应汇总成一条消息，实际发了 %d 条", got)
	}
	text := markdownText(t, hook.last(t))
	// A digest header with a time window is rendered by markdown.go as "**최근 N분간 신규 취약점
	// N건**" ("N new vulnerabilities in the last N minutes") (digestInterval defaults to 30
	// minutes, so WindowMinutes>0).
	if !strings.Contains(text, "최근") || !strings.Contains(text, "신규 취약점 3건") {
		t.Fatalf("汇总消息缺少条数/时间窗文案:\n%s", text)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(text, fmt.Sprintf("汇总漏洞%d", i)) {
			t.Fatalf("汇总消息缺少第 %d 条:\n%s", i, text)
		}
	}
	// The same batch should share a batch_id.
	var distinct, total int
	if err := f.pg.QueryRow(`SELECT count(DISTINCT batch_id), count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&distinct, &total); err != nil {
		t.Fatal(err)
	}
	if total != 3 || distinct != 1 {
		t.Fatalf("三条投递应共享一个 batch_id，得到 distinct=%d total=%d", distinct, total)
	}
}

func TestNotifyDisabledChannelDoesNotSend(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":    "停用渠道",
		"kind":    notify.KindDingTalk,
		"enabled": false,
		"config":  map[string]any{"webhook": hook.URL},
	})
	f.record(t, "停用期间的漏洞", "critical")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("停用渠道不该产生投递，得到 %d 条", n)
	}
}

func TestNotifyStatusChangeDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "状态变更订阅",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"on_status_change": true},
	})
	finding := f.record(t, "状态变更用例", "high")
	r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"fixed"}`)
	if r.Code != 200 {
		t.Fatalf("改状态失败 %d: %s", r.Code, r.Body)
	}
	f.deliver(t, chID, "")

	// There should be two: the fixed one is the status change; the finding_created one may also
	// be sent in the same round. The status change is actually created later, but don't rely on
	// the order; search all of them.
	found := false
	for i := 0; i < hook.count(); i++ {
		text := markdownText(t, hook.body(t, i))
		// The status change card is rendered by markdown.go as "**상태 변경**: %s → %s" ("status
		// change"), and the status value by notify.StatusLabel (fixed → "수정됨", "fixed").
		if strings.Contains(text, "상태 변경") && strings.Contains(text, "수정됨") {
			found = true
		}
	}
	if !found {
		t.Fatalf("没有收到含「状态变更 → 已修复」的消息（共 %d 条）", hook.count())
	}
}

func TestNotifyStatusChangeSuppressedByDefault(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "不订阅状态变更",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "不订阅变更", "high")
	if r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"false_positive"}`); r.Code != 200 {
		t.Fatalf("改状态失败 %d: %s", r.Code, r.Body)
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
		t.Fatalf("未订阅状态变更的渠道不该收到状态变更投递，得到 %d 条", n)
	}
}

func TestNotifyTestMessageEndpoint(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "测试发送",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", chID), ""); r.Code != 200 {
		t.Fatalf("测试发送失败 %d: %s", r.Code, r.Body)
	}
	if hook.count() != 1 {
		t.Fatalf("假接收端应收到 1 条测试消息，得到 %d", hook.count())
	}
	// The test message must be recognizable as a test at a glance and must not be mistaken for a
	// real vulnerability. The test message title is notify_api.go's notifyTestName ("테스트 메시지
	// · 채널 설정 정상", "test message · channel configured correctly").
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "테스트") {
		t.Fatalf("测试消息应标明是测试: %s", text)
	}
	// When the config is broken, the channel's raw error should be returned to the user as-is.
	badID := f.createChannel(t, map[string]any{
		"name":   "坏地址",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", badID), ""); r.Code != 502 {
		t.Fatalf("投递失败应回 502，得到 %d: %s", r.Code, r.Body)
	}
}

func TestNotifyDeliveriesHistoryAndRetry(t *testing.T) {
	f := newNotifyFixture(t)
	// Point at an address that is guaranteed to fail, to produce failed deliveries.
	chID := f.createChannel(t, map[string]any{
		"name":   "失败重试",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	f.record(t, "会失败的推送", "high")
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)
	// Deliver repeatedly until the retry budget is exhausted.
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
		t.Fatalf("重试耗尽后应为 failed，得到 %s", state)
	}

	r := f.request("GET", fmt.Sprintf("/api/notify/deliveries?channel_id=%d&state=failed", chID), "")
	if r.Code != 200 {
		t.Fatalf("查历史失败 %d: %s", r.Code, r.Body)
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
		t.Fatalf("应查到 1 条失败投递，得到 total=%d len=%d", hist.Total, len(hist.Deliveries))
	}
	if hist.Deliveries[0].LastError == "" {
		t.Fatal("历史里应带上失败原因，否则用户无法排查")
	}
	if hist.Deliveries[0].Attempts < db.MaxNotifyAttempts {
		t.Fatalf("尝试次数应被记录，得到 %d", hist.Deliveries[0].Attempts)
	}
	if hist.Deliveries[0].Title != "会失败的推送" {
		t.Fatalf("历史应带出漏洞标题，得到 %q", hist.Deliveries[0].Title)
	}

	// Manual resend: should go back to pending with the counter reset to zero.
	if r := f.request("POST", fmt.Sprintf("/api/notify/deliveries/%d/retry", hist.Deliveries[0].ID), ""); r.Code != 200 {
		t.Fatalf("重发失败 %d: %s", r.Code, r.Body)
	}
	var attempts int
	if err := f.pg.QueryRow(`SELECT state, attempts FROM notification_deliveries WHERE id=$1`, hist.Deliveries[0].ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != db.NotifyStatePending || attempts != 0 {
		t.Fatalf("重发后应为 pending 且 attempts=0，得到 %s/%d", state, attempts)
	}
}

func TestNotifyMetaAndSettingsRoundTrip(t *testing.T) {
	f := newNotifyFixture(t)
	r := f.request("GET", "/api/notify/meta", "")
	if r.Code != 200 {
		t.Fatalf("meta 失败: %s", r.Body)
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
		t.Fatalf("meta 应列出全部 %d 个渠道，得到 %d", len(notify.Kinds()), len(meta.Kinds))
	}
	for _, k := range meta.Kinds {
		if len(k.SecretKeys) == 0 {
			t.Errorf("渠道 %s 未上报凭据字段", k.Kind)
		}
	}

	// Round-trip the three global settings. The trailing slash should be normalized away, otherwise the back-link would come out as "//function/...".
	if r := f.request("PUT", "/api/settings", `{"notify_public_base_url":"https://artex.example.com/","notify_digest_interval_min":15,"notify_enabled":true}`); r.Code != 200 {
		t.Fatalf("写设置失败 %d: %s", r.Code, r.Body)
	}
	t.Cleanup(func() {
		f.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, settingNotifyPublicBaseURL, settingNotifyDigestMinutes)
	})
	payload := f.s.settingsPayload()
	if payload["notify_public_base_url"] != "https://artex.example.com" {
		t.Fatalf("回链地址未规范化: %v", payload["notify_public_base_url"])
	}
	if payload["notify_digest_interval_min"] != 15 {
		t.Fatalf("汇总周期未生效: %v", payload["notify_digest_interval_min"])
	}

	// Invalid values should be rejected.
	for _, body := range []string{
		`{"notify_public_base_url":"ftp://x"}`,
		`{"notify_digest_interval_min":0}`,
		`{"notify_digest_interval_min":99999}`,
	} {
		if r := f.request("PUT", "/api/settings", body); r.Code != 400 {
			t.Errorf("%s 应返回 400，得到 %d", body, r.Code)
		}
	}
}

// TestNotifyDeepLinkUsesPublicBaseURL covers back-link construction: when public_base_url is
// configured, a single message must use an ActionCard with a button, and the link must point
// to the vulnerability details page.
func TestNotifyDeepLinkUsesPublicBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "回链",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "带回链的漏洞", "high")
	f.deliver(t, chID, "https://artex.example.com")

	body := hook.last(t)
	card, _ := body["actionCard"].(map[string]any)
	if card == nil {
		t.Fatalf("有回链时应用 ActionCard，得到 msgtype=%v", body["msgtype"])
	}
	want := fmt.Sprintf("https://artex.example.com/function/findings/detail?id=%d", finding)
	if card["singleURL"] != want {
		t.Fatalf("回链不对\n期望 %s\n得到 %v", want, card["singleURL"])
	}
}

// TestNotifyNoDeepLinkWithoutBaseURL covers the reverse: when no external address is
// configured, no broken link should be produced (e.g. pointing at localhost or a relative
// path); it should fall back to plain markdown.
func TestNotifyNoDeepLinkWithoutBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "无回链",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "无回链的漏洞", "high")
	f.deliver(t, chID, "")

	body := hook.last(t)
	if body["msgtype"] != "markdown" {
		t.Fatalf("未配外部地址时应发 markdown，得到 %v", body["msgtype"])
	}
	// The details link heading is rendered by markdown.go as "[상세 보기](URL)" ("view details") —
	// without an external address this link must not appear at all (checking for the old Chinese
	// "查看详情" would always pass now that the label has changed to Korean, and would not catch
	// a regression).
	if text := markdownText(t, body); strings.Contains(text, "상세 보기") {
		t.Fatalf("未配外部地址时不该出现详情链接:\n%s", text)
	}
}

// TestNotifyDigestSegmentsAndDefersRemainder is the end-to-end evidence for the "silent loss" fix.
//
// Digest messages are bound by the channel's length limit (WeCom: 4096 bytes), and when a
// batch doesn't fit, it must be split **by whole items**: the ones that fit into this message
// are marked delivered, and the rest go back to the queue for the next message. The old
// implementation marked the whole batch successful — the truncated items were in neither the
// message nor the failure list, the delivery history still showed success, and the
// vulnerabilities simply vanished.
//
// Asserts four things: ① only the number actually fitted was marked ② the rest are still
// pending ③ the deferred items **did not consume retry attempts** ④ another round sends the
// rest (it doesn't get stuck).
func TestNotifyDigestSegmentsAndDefersRemainder(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	// Use WeCom: its markdown limit of 4096 bytes is the tightest of the six channels.
	chID := f.createChannel(t, map[string]any{
		"name":   "分段汇总",
		"kind":   notify.KindWeCom,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	const total = 60
	// Use a longish title so 60 items far exceed 4096 bytes and are guaranteed to be split.
	longName := strings.Repeat("超长漏洞名称", 6)
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
		t.Fatalf("应只发出一条消息，得到 %d", hook.count())
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
		t.Fatal("应有条目被标记为已送达")
	}
	if pending == 0 {
		t.Fatalf("一批 %d 条不可能全装进 4096 字节，应有剩余待发；sent=%d", total, sent)
	}
	if sent+pending != total {
		t.Fatalf("条目数对不上：sent=%d pending=%d total=%d（既没送达也没待发=丢失）", sent, pending, total)
	}
	// The message body must honestly state how many items were not included in this message.
	// The segmentation notice is rendered by markdown.go as "(이 메시지에는 앞 N건만 … 나머지 N건은
	// 다음 메시지에서 …)" ("this message contains only the first N … the remaining N in the next
	// message …").
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "나머지") {
		t.Fatalf("消息应说明还有条目未包含在本条:\n%.400s", text)
	}

	// Deferred items must not consume retry budget: attempts was optimistically +1'd at claim time, and must be decremented back on deferral.
	var maxAttempts int
	if err := f.pg.QueryRow(`SELECT COALESCE(max(attempts),0) FROM notification_deliveries
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending).Scan(&maxAttempts); err != nil {
		t.Fatal(err)
	}
	if maxAttempts > 0 {
		t.Fatalf("被推迟的条目不该消耗重试次数（否则几条之后就会被判失败），得到 attempts=%d", maxAttempts)
	}

	// Run repeatedly until it converges. What is asserted is that **everything is eventually
	// delivered** and that it really took multiple rounds — this is stronger than "done by the
	// second round": it proves segmentation neither gets stuck nor drops remaining items.
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
			t.Fatalf("分段投递不收敛：跑了 %d 轮仍有 %d 条悬而未决", rounds, undelivered)
		}
		before := hook.count()
		f.n.stepDigest(ctx, ch, 50, "")
		if hook.count() == before {
			t.Fatalf("第 %d 轮没有任何进展，剩余 %d 条会永久卡住", rounds, undelivered)
		}
	}
	if rounds < 2 {
		t.Fatalf("一条 4096 字节的消息装不下 %d 条长标题漏洞，应分多轮发出，实际只用了 %d 轮", total, rounds)
	}
	// Every round after the first should be **pure continuation**, with no items rejected by the channel.
	var failed int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state=$2`,
		chID, db.NotifyStateFailed).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("假接收端始终返回成功，不该有失败条目，得到 %d", failed)
	}
}

// TestNotifyBackoffTableMatchesAttemptBudget is an anti-drift assertion.
//
// The retry budget (db.MaxNotifyAttempts) and the backoff table (notifyBackoff) live in two
// different packages: the former is the state machine's policy, the latter the engine's
// execution cadence. If only one of them is changed — e.g. raising the budget to 5 but
// forgetting to add backoff tiers — the code won't complain; the 4th and 5th retries will just
// reuse the last tier's interval, showing up as "retries mysteriously slowing down", which is
// hard to trace back to here during debugging.
// Asserting that their lengths match exposes such drift in CI.
func TestNotifyBackoffTableMatchesAttemptBudget(t *testing.T) {
	if len(notifyBackoff) != db.MaxNotifyAttempts {
		t.Fatalf("退避档位数(%d)与最大尝试次数(%d)不一致——改一个必须同时改另一个",
			len(notifyBackoff), db.MaxNotifyAttempts)
	}
	// Backoff intervals must be monotonically non-decreasing, otherwise retries would get more and more aggressive and worsen rate limiting.
	for i := 1; i < len(notifyBackoff); i++ {
		if notifyBackoff[i] < notifyBackoff[i-1] {
			t.Fatalf("退避间隔必须单调不减：第 %d 档 %v < 第 %d 档 %v",
				i, notifyBackoff[i], i-1, notifyBackoff[i-1])
		}
	}
}

// TestNotifyRateLimitDoesNotConsumeRetryBudget locks in the "take tokens first, then claim"
// order. If it were reversed (claim first, then drop), deliveries blocked by rate limiting
// would already have counted one attempt, the budget would be used up purely by waiting, and
// they'd end up in failed.
func TestNotifyRateLimitDoesNotConsumeRetryBudget(t *testing.T) {
	// Tests only the token bucket itself; no Server is needed (and none should be built for it).
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// 1 per minute: at most 1 when the bucket is full.
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now); got != 1 {
		t.Fatalf("满桶时每分钟 1 条应取 1 个令牌，得到 %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("令牌耗尽后应立即返回 0，得到 %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(30*time.Second)); got != 0 {
		t.Fatalf("半程不应补满一个令牌，得到 %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Minute)); got != 1 {
		t.Fatalf("满一个周期应补回 1 个令牌，得到 %d", got)
	}
	// A channel with no rate limit gets a finite cap, so a single round isn't held up by an unbounded backlog.
	if got := n.takeTokens(2, 0, notifyUnlimitedBurstPerTick+10, now); got != notifyUnlimitedBurstPerTick {
		t.Fatalf("不限流应返回每轮上限 %d，得到 %d", notifyUnlimitedBurstPerTick, got)
	}
	// Token buckets of different channels are independent of each other.
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("渠道 1 的桶应仍然为空，得到 %d", got)
	}
}

// TestNotifyTakeTokensKeepsUnusedTokens locks in the "take only want" semantics.
//
// The old implementation drained the whole bucket and only then let the caller truncate, so
// a rate=100/min channel would fill its bucket, use only 5 in a round, and throw away the
// remaining 95 tokens; it was charged the same even when the channel had no pending
// deliveries in that round. As a result, what the comment claimed — "when backlogged, it can
// burst rate_per_min items at once" — could never happen under any circumstances.
func TestNotifyTakeTokensKeepsUnusedTokens(t *testing.T) {
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// The bucket starts full (100); this round wants only 5.
	if got := n.takeTokens(1, 100, 5, now); got != 5 {
		t.Fatalf("want=5 时应恰好取 5 个令牌，得到 %d", got)
	}
	// Key assertion: the remaining 95 must still be in the bucket, not drained and discarded.
	// Time is not advanced, ensuring whatever is taken can only come from the stock, not a refill.
	if got := n.takeTokens(1, 100, 95, now); got != 95 {
		t.Fatalf("剩余令牌应仍可取用（期望 95），得到 %d——桶被整轮抽空了", got)
	}
	if got := n.takeTokens(1, 100, 1, now); got != 0 {
		t.Fatalf("桶已取尽，应返回 0，得到 %d", got)
	}
	// want<=0 should not deduct any tokens (empty rounds are free).
	n2 := &Notifier{buckets: map[int64]*notifyBucket{}}
	if got := n2.takeTokens(1, 20, 0, now); got != 0 {
		t.Fatalf("want=0 应返回 0，得到 %d", got)
	}
	if got := n2.takeTokens(1, 20, 20, now); got != 20 {
		t.Fatalf("want=0 的那次不该消耗令牌，应仍可取满 20，得到 %d", got)
	}
}

// TestDigestTickPlanDecouplesBatchSizeFromSendBudget pins the two dimensions of digest mode.
//
// Once the digest batch size is tied to the per-round request budget, a channel with
// rate_per_min=20 refills only 1 token per 3-second tick, so each digest message holds only
// 1 vulnerability — functionally equivalent to having no digest at all, while the message
// header still says "1 new vulnerability in the last 30 minutes". This degradation raises no
// error, and the existing end-to-end cases can't see it either (they manually pass a large
// enough limit to stepDigest, bypassing the allowance calculation in step), so the decision
// itself is asserted directly here.
func TestDigestTickPlanDecouplesBatchSizeFromSendBudget(t *testing.T) {
	tokens, claimLimit := digestTickPlan()
	// One batch = one message = one request = one token. The token's unit is messages, not vulnerabilities.
	if tokens != 1 {
		t.Fatalf("汇总一批只发一条消息，应恰好消耗 1 个令牌，得到 %d", tokens)
	}
	if claimLimit != db.MaxDigestBatchSize {
		t.Fatalf("汇总批次大小应为内存上界 db.MaxDigestBatchSize=%d，得到 %d",
			db.MaxDigestBatchSize, claimLimit)
	}
	// Key relationship: the batch size must be much larger than the per-round request budget.
	// If the two are ever of the same magnitude, "how many messages to send" and "how many
	// vulnerabilities a batch holds" have been conflated into one number again.
	if claimLimit <= notifyMaxSendsPerChannelPerTick {
		t.Fatalf("汇总批次大小 %d 不应受每轮请求预算 %d 约束——"+
			"请求预算是由租约倒推的「发几次请求」，与「一批装几条漏洞」是两个量纲",
			claimLimit, notifyMaxSendsPerChannelPerTick)
	}
}

// TestNotifyTickBudgetFitsWithinLease is another anti-drift assertion.
//
// The per-channel per-round delivery cap (notifyMaxSendsPerChannelPerTick) is derived
// backwards from the lease duration: the worst-case duration of serial deliveries in one
// round must be < the lease, otherwise the leases of the last few expire before they are
// sent, and in multi-instance deployments the other side re-claims them and sends them
// again. These three constants live in different places, and changing any one of them can
// break the relationship without any error — so it is pinned here.
func TestNotifyTickBudgetFitsWithinLease(t *testing.T) {
	worst := time.Duration(notifyMaxSendsPerChannelPerTick) * notifySendTimeout
	if worst >= notifyLease {
		t.Fatalf("单渠道一轮的最坏耗时 %v 不应达到或超过租约 %v"+
			"（notifyMaxSendsPerChannelPerTick=%d × notifySendTimeout=%v）——"+
			"改这三个常量中的任意一个都要同步检查另外两个",
			worst, notifyLease, notifyMaxSendsPerChannelPerTick, notifySendTimeout)
	}
}
