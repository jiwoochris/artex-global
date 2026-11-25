package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/internal/oob"
)

// fakeURLClient is a urlClient test double that returns preset hostnames.
type fakeURLClient struct {
	urls []string
	i    int
}

func (f *fakeURLClient) URL() string {
	if f.i >= len(f.urls) {
		return ""
	}
	u := f.urls[f.i]
	f.i++
	return u
}

func (f *fakeURLClient) Close() error { return nil }

// fakePollClient is a pollSource test double: it replays a fixed interaction
// list through StartPolling and reports a healthy poll window.
type fakePollClient struct {
	fakeURLClient
	interactions []oob.Interaction
}

func (f *fakePollClient) StartPolling(interval time.Duration, cb func(oob.Interaction)) error {
	for _, in := range f.interactions {
		cb(in)
	}
	return nil
}

func (f *fakePollClient) PollOnce(cb func(oob.Interaction)) error { return nil }

func (f *fakePollClient) Health() (int64, error, time.Time, time.Time) {
	return 0, nil, time.Time{}, time.Now()
}

func TestInteractorGenerateUniqueProbes(t *testing.T) {
	f := &fakeURLClient{urls: []string{"u1.xxxx.oast.me", "u2.xxxx.oast.me", "u3.xxxx.oast.me"}}
	it := &interactor{client: f}
	id1, host1, err := it.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id2, host2, err := it.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("两次 Generate 应产出不同 probe_id: %s vs %s", id1, id2)
	}
	if host1 != "u1.xxxx.oast.me" || host2 != "u2.xxxx.oast.me" {
		t.Fatalf("探测地址透传失败: %s %s", host1, host2)
	}
	if got := strings.SplitN(host1, ".", 2)[0]; got != id1 {
		t.Fatalf("probe_id 应为主机名首段: id=%s host=%s", id1, host1)
	}
}

func TestInteractorNilSafety(t *testing.T) {
	var it *interactor
	if id, host, err := it.Generate(); err == nil || id != "" || host != "" {
		t.Fatalf("nil interactor Generate 应返回空: %q %q", id, host)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("nil interactor Close 应安全: %v", err)
	}
}

func TestOOBDisabledByConfig(t *testing.T) {
	if tools := oobTools(nil); len(tools) != 0 {
		t.Fatalf("无会话时应返回空工具集，got %d", len(tools))
	}
}

func TestProbeIDUsesFullCallbackHost(t *testing.T) {
	full := "c0ffeesessionnonce.example.test"
	if got := probeIDForInteraction(full, "A? unrelated.example.test", ""); got != "c0ffeesessionnonce" {
		t.Fatalf("应以完整回调主机名的首段关联探测，got %q", got)
	}
	if got := probeIDForInteraction("", "GET / HTTP/1.1\r\nHost: c0ffeesessionnonce.example.test\r\n\r\n", ""); got != "c0ffeesessionnonce" {
		t.Fatalf("FullId 缺失时应从原始请求回退取回调主机名，got %q", got)
	}
	if got := probeIDForInteraction("", "", ""); got != "" {
		t.Fatalf("无主机名时不得伪造 probe_id，got %q", got)
	}
}

// A DNS-exfiltration payload prefixes the probe label (data.<probe>.<server>);
// the probe label is the one before the server hostname, not the first label.
func TestProbeIDFindsPrefixedProbeLabel(t *testing.T) {
	if got := probeIDForInteraction("data.c0ffeesessionnonce.oast.me", "", "oast.me"); got != "c0ffeesessionnonce" {
		t.Fatalf("应以服务器域名前的标签作为 probe_id，got %q", got)
	}
	if got := probeIDForInteraction("", "GET / HTTP/1.1\r\nHost: data.c0ffeesessionnonce.oast.me\r\n\r\n", "oast.me"); got != "c0ffeesessionnonce" {
		t.Fatalf("原始请求回退也应找到前置标签，got %q", got)
	}
}

func TestLiveSessionDeduplicatesRedelivery(t *testing.T) {
	redelivered := oob.Interaction{
		Protocol:  "dns",
		FullID:    "probe.oast.me",
		Remote:    "203.0.113.7",
		Timestamp: time.Now().UTC(),
	}
	src := &fakePollClient{interactions: []oob.Interaction{redelivered, redelivered}}
	ls, err := newLiveSessionFrom(src, src, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	its, err := ls.Poll(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(its) != 1 {
		t.Fatalf("同一事件重复投递只应保留一次，got %d", len(its))
	}
}

func TestLiveSessionTruncatesRawCallback(t *testing.T) {
	ls := &liveSession{seen: make(map[string]struct{})}
	long := strings.Repeat("a", maxRawCallback+100)
	ls.handle(oob.Interaction{Protocol: "http", FullID: "probe.oast.me", Raw: long})
	if len(ls.buf) != 1 {
		t.Fatalf("expected one buffered callback, got %d", len(ls.buf))
	}
	if want := maxRawCallback + len("…"); len(ls.buf[0].Raw) != want {
		t.Fatalf("raw callback must be bounded: got %d want %d", len(ls.buf[0].Raw), want)
	}
}

func TestLiveSessionPollReportsServiceFailure(t *testing.T) {
	want := errors.New("poll failed")
	ls := &liveSession{
		it: interactor{client: &fakePollClient{}},
		health: func() (int64, error, time.Time, time.Time) {
			return 1, want, time.Now(), time.Time{}
		},
	}
	if _, got := ls.Poll(10 * time.Millisecond); !errors.Is(got, want) {
		t.Fatalf("轮询故障应返回错误而不是健康空窗口，got %v", got)
	}
}

func TestLiveSessionPollRequiresCompletedObservation(t *testing.T) {
	ls := &liveSession{
		it: interactor{client: &fakePollClient{}},
		health: func() (int64, error, time.Time, time.Time) {
			return 0, nil, time.Time{}, time.Time{}
		},
	}
	if _, err := ls.Poll(10 * time.Millisecond); !errors.Is(err, errNoCompletedPoll) {
		t.Fatalf("尚未完成观察必须明确为不可判断，got %v", err)
	}
	if err := ls.pollWindowHealth(time.Now().Add(-time.Second)); !errors.Is(err, errNoCompletedPoll) {
		t.Fatalf("没有完成轮询的窗口必须标识为未观察，got %v", err)
	}
}

func TestLiveSessionPollHealthyEmptyWindow(t *testing.T) {
	ls := &liveSession{
		it: interactor{client: &fakePollClient{}},
		health: func() (int64, error, time.Time, time.Time) {
			return 0, nil, time.Time{}, time.Now()
		},
	}
	its, err := ls.Poll(20 * time.Millisecond)
	if err != nil {
		t.Fatalf("健康空窗口不应报错: %v", err)
	}
	if len(its) != 0 {
		t.Fatalf("健康空窗口应返回空交互: %v", its)
	}
}

func TestLiveSessionPollIgnoresHistoricalFailure(t *testing.T) {
	historical := time.Now().Add(-time.Minute)
	ls := &liveSession{
		it: interactor{client: &fakePollClient{}},
		health: func() (int64, error, time.Time, time.Time) {
			return 3, errors.New("old failure"), historical, time.Now()
		},
	}
	if _, err := ls.Poll(10 * time.Millisecond); err != nil {
		t.Fatalf("历史错误不应污染当前健康窗口: %v", err)
	}
}

func TestLiveSessionPollReturnsCurrentWindowFailure(t *testing.T) {
	want := errors.New("poll failed")
	ls := &liveSession{
		it: interactor{client: &fakePollClient{}},
		health: func() (int64, error, time.Time, time.Time) {
			return 1, want, time.Now(), time.Now()
		},
	}
	if _, err := ls.Poll(10 * time.Millisecond); !errors.Is(err, want) {
		t.Fatalf("窗口内故障应报服务不可用，got %v", err)
	}
}

func TestLazySessionCloseBeforeUse(t *testing.T) {
	ls := &lazySession{new: func() (*oob.Session, error) {
		t.Fatal("关闭后的惰性会话不应再创建真实会话")
		return nil, nil
	}}
	if err := ls.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ls.Generate(); err == nil {
		t.Fatal("关闭后 Generate 应报错")
	}
}
