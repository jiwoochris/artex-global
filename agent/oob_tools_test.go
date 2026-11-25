package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeSession is a test double for the oobSession seam. It never contacts a
// real Interactsh server.
type fakeSession struct {
	url          string
	generated    []string
	interactions []OOBInteraction
	err          error
	generateErr  error
}

func (f *fakeSession) Generate() (id, host string, err error) {
	if f.generateErr != nil {
		return "", "", f.generateErr
	}
	id = "u" + strings.Repeat("n", len(f.generated)+1)
	f.generated = append(f.generated, id)
	return id, f.url, nil
}

func (f *fakeSession) Poll(wait time.Duration) ([]OOBInteraction, error) {
	return f.interactions, f.err
}

func (f *fakeSession) Close() error { return nil }

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("工具输出不是合法 JSON: %v\n%s", err, raw)
	}
	return m
}

func TestOOBGenerateDefaultOneAddress(t *testing.T) {
	fs := &fakeSession{url: "xxxx.oast.me"}
	tool := (&ToolSet{}).oobGenerate(fs)
	res, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("oob_generate 失败: %v", err)
	}
	if len(fs.generated) != 1 {
		t.Fatalf("默认应生成 1 个探测地址，got %d", len(fs.generated))
	}
	out := decode(t, res.Flatten())
	probes, _ := out["probes"].([]any)
	if len(probes) != 1 {
		t.Fatalf("应返回 1 个 probe，got %v", out["probes"])
	}
	p := probes[0].(map[string]any)
	if p["domain"] != "xxxx.oast.me" || p["http_url"] != "http://xxxx.oast.me" {
		t.Fatalf("missing domain/http URL: %v", p)
	}
	if !strings.Contains(p["url"].(string), "xxxx.oast.me") {
		t.Fatalf("probe 应含规范主机名，got %v", p)
	}
}

func TestOOBGenerateMaxFive(t *testing.T) {
	fs := &fakeSession{url: "xxxx.oast.me"}
	tool := (&ToolSet{}).oobGenerate(fs)
	res, err := tool.Call(context.Background(), json.RawMessage(`{"count":6}`), nil)
	if err != nil {
		t.Fatalf("count 超限应返回错误结果而非 Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("count 超过 5 应报错（IsError）")
	}
	if len(fs.generated) != 0 {
		t.Fatal("参数非法时不应生成任何地址")
	}
}

func TestOOBGenerateServiceUnavailable(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("oob generate failure", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	worker := NewToolSet(ts, "worker-1")
	intentID, err := ts.AddIntent(map[string]any{"summary": "OOB 初始化失败"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	worker.SetOwnerNode(intentID)
	fs := &fakeSession{url: "xxxx.oast.me", generateErr: context.DeadlineExceeded}
	result, err := worker.oobGenerate(fs).Call(t.Context(), json.RawMessage(`{}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("OOB 初始化失败应作为明确结果返回: %v %s", err, result.Flatten())
	}
	out := decode(t, result.Flatten())
	if out["status"] != "service_unavailable" {
		t.Fatalf("初始化失败应报 service_unavailable，got %v", out["status"])
	}
}

func TestOOBPollReturnsInteractions(t *testing.T) {
	fs := &fakeSession{url: "xxxx.oast.me", interactions: []OOBInteraction{
		{Protocol: "dns", UniqueID: "u1", Raw: "A? u1.xxxx.oast.me", Remote: "1.2.3.4"},
	}}
	tool := (&ToolSet{}).oobPoll(fs)
	res, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("oob_poll 失败: %v", err)
	}
	out := decode(t, res.Flatten())
	got, _ := out["interactions"].([]any)
	if len(got) != 1 {
		t.Fatalf("应返回 1 条交互，got %v", out)
	}
	it := got[0].(map[string]any)
	if it["protocol"] != "dns" || it["probe_id"] != "u1" {
		t.Fatalf("交互字段不完整: %v", it)
	}
}

func TestOOBPollFiltersWithoutLosingOtherProbes(t *testing.T) {
	fs := &fakeSession{interactions: []OOBInteraction{{UniqueID: "a", Protocol: "dns"}, {UniqueID: "b", Protocol: "http"}}}
	tool := (&ToolSet{}).oobPoll(fs)
	res, err := tool.Call(t.Context(), json.RawMessage(`{"probe_id":"a"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := decode(t, res.Flatten())["interactions"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["probe_id"] != "a" {
		t.Fatal(res.Flatten())
	}
	fs.interactions = nil
	res, err = tool.Call(t.Context(), json.RawMessage(`{"probe_id":"b"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	rows = decode(t, res.Flatten())["interactions"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["probe_id"] != "b" {
		t.Fatal(res.Flatten())
	}
}

func TestOOBPollNoInteractions(t *testing.T) {
	fs := &fakeSession{url: "xxxx.oast.me"}
	tool := (&ToolSet{}).oobPoll(fs)
	res, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("oob_poll 失败: %v", err)
	}
	out := decode(t, res.Flatten())
	got, _ := out["interactions"].([]any)
	if len(got) != 0 {
		t.Fatalf("无回调时应返回空数组，got %v", out)
	}
}

func TestOOBPollIncompleteObservationIsNotNoInteraction(t *testing.T) {
	fs := &fakeSession{url: "xxxx.oast.me", err: errNoCompletedPoll}
	res, err := (&ToolSet{}).oobPoll(fs).Call(t.Context(), json.RawMessage(`{}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("未完成观察应作为明确结果返回: %v %s", err, res.Flatten())
	}
	out := decode(t, res.Flatten())
	if out["status"] != "observation_incomplete" {
		t.Fatalf("未完成观察必须单独成态，不得折为 no_interaction 或服务故障，got %v", out)
	}
}

// A probe ID from a resumed transcript belongs to an earlier session. Polling
// it against the current session must not record a healthy absence.
func TestOOBPollRejectsStaleProbeID(t *testing.T) {
	fs := &fakeSession{url: "xxxx.oast.me"}
	tracked := &trackedSession{inner: fs, issued: make(map[string]struct{})}
	gen := (&ToolSet{}).oobGenerate(tracked)
	res, err := gen.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	probes := decode(t, res.Flatten())["probes"].([]any)
	issued := probes[0].(map[string]any)["probe_id"].(string)
	if !tracked.knows(issued) {
		t.Fatal("generate must record the issued probe")
	}
	poll := (&ToolSet{}).oobPoll(tracked)
	res, err = poll.Call(context.Background(), json.RawMessage(`{"probe_id":"stale-from-transcript"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := decode(t, res.Flatten())
	if out["status"] != "observation_incomplete" {
		t.Fatalf("stale probe must be observation_incomplete, got %v", out["status"])
	}
}

func TestOOBPollDistinguishesNoInteractionFromServiceUnavailable(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("oob outcomes", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	worker := NewToolSet(ts, "worker-1")
	worker.SetTaskID(task.ID)
	intentID, err := ts.AddIntent(map[string]any{"summary": "OOB 状态区分"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	worker.SetOwnerNode(intentID)

	noInteraction := &fakeSession{url: "nonce.example.test"}
	result, err := worker.oobPoll(noInteraction).Call(t.Context(), json.RawMessage(`{"wait":1}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("无交互轮询失败: %v %s", err, result.Flatten())
	}
	noFactID := int64(decode(t, result.Flatten())["fact_id"].(float64))
	noFact, err := ts.GetNode(noFactID)
	if err != nil || noFact == nil {
		t.Fatal(err)
	}
	var noPayload map[string]any
	if err := json.Unmarshal(noFact.Payload, &noPayload); err != nil {
		t.Fatal(err)
	}
	if noPayload["outcome"] != "no_interaction" {
		t.Fatalf("健康空窗口应为 no_interaction，got %v", noPayload["outcome"])
	}

	unavailable := &fakeSession{url: "nonce.example.test", err: context.DeadlineExceeded}
	result, err = worker.oobPoll(unavailable).Call(t.Context(), json.RawMessage(`{"wait":1}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("服务不可用轮询失败: %v %s", err, result.Flatten())
	}
	out := decode(t, result.Flatten())
	if out["status"] != "service_unavailable" {
		t.Fatalf("服务故障应明确报 service_unavailable，got %v", out["status"])
	}
	unavailableFactID := int64(out["fact_id"].(float64))
	unavailableFact, err := ts.GetNode(unavailableFactID)
	if err != nil || unavailableFact == nil {
		t.Fatal(err)
	}
	var unavailablePayload map[string]any
	if err := json.Unmarshal(unavailableFact.Payload, &unavailablePayload); err != nil {
		t.Fatal(err)
	}
	if unavailablePayload["outcome"] != "service_unavailable" {
		t.Fatalf("服务故障事实状态错误: %v", unavailablePayload["outcome"])
	}
}

// OOB 工具必须入 seed 目录并默认绑 worker——已有数据库重启后才能补绑，
// 无需手工迁移。
func TestOOBToolsSeededForWorker(t *testing.T) {
	byKey := map[string]ToolSeed{}
	for _, s := range BuiltinToolSeeds() {
		byKey[s.Key] = s
	}
	for _, key := range []string{"oob_generate", "oob_poll"} {
		s, ok := byKey[key]
		if !ok {
			t.Fatalf("%s 未入 seed 目录", key)
		}
		bound := false
		for _, a := range s.Agents {
			if a == "worker" {
				bound = true
			}
		}
		if !bound {
			t.Fatalf("%s 应默认绑 worker，got %v", key, s.Agents)
		}
	}
}
