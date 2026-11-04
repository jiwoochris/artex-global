package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// This file is an invariant test: **no** error text surfacing from a channel implementation may carry credentials.
//
// Why a separate file: the original channel cases covered only the success path and platform business errors,
// never transport-layer failures. And transport-layer errors (connection refused / DNS failure / timeout) are exactly the most dangerous --
// the *url.Error returned by http.Client.Do puts the **full URL** into the error text, and for this feature
// the credentials of these providers live in the URL. The credential flowed along this path to four exits:
//
//	notification_deliveries.last_error  -> stored in plaintext in the DB
//	GET /api/notify/deliveries response -> bypasses the channel config mask and is echoed to the browser
//	server logs                         -> often shipped offsite and retained
//	test-send endpoint 502 response     -> popped up directly in the frontend
//
// So this does not test a single function; it really sends one request that must fail per channel and asserts the error text
// does not contain that credential.

// credentialCases covers every channel shape where "the credential is in the URL":
// DingTalk/WeCom in the query, Feishu in the last path segment, Telegram in a middle path segment.
var credentialCases = []struct {
	name   string
	ch     Channel
	cfg    map[string]any
	secret string
}{
	{
		name:   "DingTalk access_token in query",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send?access_token=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "WeCom key in query",
		ch:     weComChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/cgi-bin/webhook/send?key=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "Feishu hook id in last path segment",
		ch:     feishuChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/open-apis/bot/v2/hook/" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "Telegram bot token in middle path segment",
		ch:     telegramChannel{},
		cfg:    map[string]any{"bot_token": leakProbeToken, "chat_id": "1", "base_url": "http://127.0.0.1:1"},
		secret: leakProbeToken,
	},
	{
		name:   "DingTalk sign secret",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send", "secret": leakProbeToken},
		secret: leakProbeToken,
	},
}

// leakProbeToken is a sentinel value that can never be a real credential, searched for in the error text.
const leakProbeToken = "LEAKPROBE0123456789abcdef"

// TestChannelErrorsNeverLeakCredentials is the core invariant.
func TestChannelErrorsNeverLeakCredentials(t *testing.T) {
	for _, tc := range credentialCases {
		t.Run(tc.name, func(t *testing.T) {
			// A peer that must fail: nobody listens on 127.0.0.1:1, so this takes the connection-refused path.
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{
				Items: []Item{{FindingID: 1, Severity: "high", Name: "leak probe"}},
			})
			if err == nil {
				t.Fatal("an unreachable address should return an error")
			}
			assertNoSecret(t, err.Error(), tc.secret)
		})
	}
}

// TestChannelErrorsNeverLeakCredentialsInPermanentPath covers the permanent-failure branches:
// URL validation failures, platform business errors and the like also surface error text, and must not carry credentials either.
func TestChannelErrorsNeverLeakCredentialsInPermanentPath(t *testing.T) {
	cases := []struct {
		name string
		ch   Channel
		cfg  map[string]any
	}{
		// The address carries a credential but is malformed -> triggers the validateHTTPURL / url.Parse branch.
		{"invalid DingTalk address", dingTalkChannel{}, map[string]any{"webhook": "file:///" + leakProbeToken}},
		{"invalid WeCom address", weComChannel{}, map[string]any{"webhook": "gopher://" + leakProbeToken}},
		{"invalid Feishu address", feishuChannel{}, map[string]any{"webhook": "ftp://" + leakProbeToken + "/hook"}},
		{"invalid Telegram API address", telegramChannel{}, map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": "file://" + leakProbeToken}},
		{"invalid generic Webhook address", webhookChannel{}, map[string]any{"url": "javascript:" + leakProbeToken}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{Items: []Item{{Severity: "high"}}})
			if err == nil {
				t.Fatal("invalid config should return an error")
			}
			assertNoSecret(t, err.Error(), leakProbeToken)
		})
	}
}

func assertNoSecret(t *testing.T, text, secret string) {
	t.Helper()
	if strings.Contains(text, secret) {
		t.Fatalf("the error text leaked the credential %q:\n    %s", secret, text)
	}
}

func TestRedactRequestTargetKeepsOnlySchemeAndHost(t *testing.T) {
	cases := map[string]string{
		"https://oapi.dingtalk.com/robot/send?access_token=S1":    "https://oapi.dingtalk.com/…",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=S2": "https://qyapi.weixin.qq.com/…",
		"https://open.feishu.cn/open-apis/bot/v2/hook/S3":         "https://open.feishu.cn/…",
		"https://api.telegram.org/botS4/sendMessage":              "https://api.telegram.org/…",
		"http://10.0.0.5:8080/hook":                               "http://10.0.0.5:8080/…",
	}
	for in, want := range cases {
		got := redactRequestTarget(in)
		if got != want {
			t.Errorf("redactRequestTarget(%q) = %q, want %q", in, got, want)
		}
		// The redacted result itself must not contain any path/query fragment of the original address.
		if parts := strings.SplitN(in, "://", 2); len(parts) == 2 {
			if hostAndRest := strings.SplitN(parts[1], "/", 2); len(hostAndRest) == 2 && hostAndRest[1] != "" {
				if strings.Contains(got, hostAndRest[1]) {
					t.Errorf("a path/query fragment is still present after redaction %q: %q", hostAndRest[1], got)
				}
			}
		}
	}
	// Unparsable input is never echoed back as the original string.
	for _, bad := range []string{"", "://", "not a url", "http://"} {
		if got := redactRequestTarget(bad); strings.Contains(got, bad) && bad != "" {
			t.Errorf("unparsable input %q was echoed back as %q", bad, got)
		}
	}
}

// TestRedactTransportErrorStripsURL targets the concrete *url.Error type directly:
// it is http.Client.Do's return type and the primary leak site.
func TestRedactTransportErrorStripsURL(t *testing.T) {
	inner := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	uerr := &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/bot" + leakProbeToken + "/sendMessage",
		Err: inner,
	}
	got := redactTransportError(uerr)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "api.telegram.org") {
		t.Errorf("the host should be kept for troubleshooting, got %q", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("the underlying cause should be kept for troubleshooting, got %q", got)
	}
	// Op must be kept too (POST vs GET matters for troubleshooting).
	if !strings.Contains(got, "Post") {
		t.Errorf("the operation name should be kept, got %q", got)
	}
}

// TestRedactURLsInTextHandlesFallback fallback path: the address inside a custom error that is not *url.Error
// (e.g. an error returned by the redirect policy) must be stripped as well.
func TestRedactURLsInTextHandlesFallback(t *testing.T) {
	in := fmt.Sprintf("cross-host redirect refused (a.example -> http://b.example/bot%s/send)", leakProbeToken)
	got := redactURLsInText(in)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "http://b.example/…") {
		t.Errorf("the address should be replaced with the redacted form, got %q", got)
	}
	// Text without an address is kept as-is.
	if plain := "dial tcp: connection refused"; redactURLsInText(plain) != plain {
		t.Error("text without an address must not be modified")
	}
}

// TestCrossHostRedirectRefused covers "credential in the URL + following a cross-host redirect = handing over the credential".
// The two httptest servers listen on different ports of 127.0.0.1; a different port means a different Host,
// which is exactly a cross-host redirect.
func TestCrossHostRedirectRefused(t *testing.T) {
	var hit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/robot/send?access_token="+leakProbeToken, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": redirector.URL + "/robot/send?access_token=" + leakProbeToken},
		Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("a cross-host redirect should be refused")
	}
	if hit {
		t.Fatal("the redirect target was visited -- the credential leaked with the redirect")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestSameHostRedirectAllowed is the reverse case: a same-host redirect (e.g. adding a trailing slash) must still work,
// otherwise normal workflows get blocked as well.
func TestSameHostRedirectAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robot/send" {
			// A redirect to the same host and the same port.
			http.Redirect(w, r, "/robot/send/", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"},
		Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("a same-host redirect should not be refused: %v", err)
	}
}
