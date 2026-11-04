package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// singleMsg builds a single-item message carrying a quote and a newline. The title/summary deliberately contain `"` and `\n`:
// that is exactly the input where template interpolation most easily produces invalid JSON.
func singleMsg() Message {
	return Message{
		Items: []Item{{
			FindingID: 42,
			Name:      `Login page "SQL injection" risk`,
			VulnClass: "SQL injection",
			Severity:  "high",
			Summary:   "parameter id\nnot filtered, leading to injection",
			Assets:    []string{"a.example.com", "b.example.com"},
			DetailURL: "https://artex.local/function/findings/detail?id=42",
		}},
	}
}

// batchMsg builds a batch of digest items. The names stay CJK on purpose: several tests
// use this fixture to check byte budgets, where multi-byte characters are the point.
func batchMsg(n int) Message {
	m := Message{Batch: true, WindowMinutes: 30, HomeURL: "https://artex.local/function/findings"}
	for i := 0; i < n; i++ {
		m.Items = append(m.Items, Item{
			FindingID: int64(i + 1),
			Name:      "漏洞" + itoa(i+1),
			VulnClass: "XSS",
			Severity:  "medium",
			Summary:   "反射型跨站脚本",
			Assets:    []string{"target.example.com"},
		})
	}
	return m
}

// capturePost starts a fake receiver that hands the received body and headers back to the assertion func.
func capturePost(t *testing.T, respBody string, assert func(t *testing.T, body map[string]any, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body is not valid JSON: %v\nraw: %s", err, raw)
			}
		}
		if assert != nil {
			assert(t, body, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDingTalkSendsActionCardWhenLinkPresent(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "actionCard" {
			t.Fatalf("with a link back it must send actionCard, got %v", body["msgtype"])
		}
		card, _ := body["actionCard"].(map[string]any)
		if card["singleURL"] != "https://artex.local/function/findings/detail?id=42" {
			t.Errorf("link back lost: %v", card["singleURL"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
}

func TestDingTalkFallsBackToMarkdownForBatch(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "markdown" {
			t.Fatalf("a digest message must send markdown, got %v", body["msgtype"])
		}
		md, _ := body["markdown"].(map[string]any)
		if !strings.Contains(md["text"].(string), "new vulnerabilities in the last 30 minutes") {
			t.Errorf("digest body is missing the time window: %v", md["text"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, batchMsg(3)); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
}

// TestDingTalkBusinessErrorIsPermanent pins the "HTTP 200 but non-zero errcode" verdict.
// Not checking errcode would record a delivery failure as a success — a pitfall shared by Chinese IM platforms.
func TestDingTalkBusinessErrorIsPermanent(t *testing.T) {
	srv := capturePost(t, `{"errcode":310000,"errmsg":"keywords not in content"}`, nil)
	_, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
	if err == nil {
		t.Fatal("a non-zero errcode must be an error")
	}
	if !IsPermanent(err) {
		t.Fatalf("a keyword mismatch is a configuration error and must be marked permanent, got %v", err)
	}
	if !strings.Contains(err.Error(), "310000") {
		t.Errorf("the error must carry the platform error code, got %v", err)
	}
}

func TestWeComTruncatesCJKWithinByteLimit(t *testing.T) {
	var contentLen int
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		md, _ := body["markdown"].(map[string]any)
		content, _ := md["content"].(string)
		contentLen = len(content)
		if !utf8.ValidString(content) {
			t.Fatal("the truncated text is not valid UTF-8 — WeCom rejects the whole message")
		}
	})
	// Build a batch of Chinese digests long enough to definitely exceed 4096 bytes.
	m := batchMsg(200)
	if _, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if contentLen > weComMarkdownLimit {
		t.Fatalf("body is %d bytes, over the WeCom limit of %d", contentLen, weComMarkdownLimit)
	}
	if contentLen == 0 {
		t.Fatal("body is empty")
	}
}

func TestWeComRateLimitIsRetryableButKeyErrorIsPermanent(t *testing.T) {
	limited := capturePost(t, `{"errcode":45009,"errmsg":"api freq out of limit"}`, nil)
	_, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": limited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("45009 is the rolling-window rate limit and must be retryable, got %v", err)
	}

	badKey := capturePost(t, `{"errcode":93000,"errmsg":"invalid webhook url"}`, nil)
	_, err = (weComChannel{}).Send(context.Background(), map[string]any{"webhook": badKey.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("93000 means the key is invalid and a retry cannot heal it, so it must be permanent, got %v", err)
	}
}

func TestFeishuCardStructureAndSign(t *testing.T) {
	const secret = "SECtest123"
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msg_type"] != "interactive" {
			t.Fatalf("must send an interactive card, got %v", body["msg_type"])
		}
		card, _ := body["card"].(map[string]any)
		header, _ := card["header"].(map[string]any)
		if header["template"] != "orange" {
			t.Errorf("high severity must use the orange colour, got %v", header["template"])
		}
		// With a secret configured the sign parameters are mandatory, otherwise Feishu rejects with 19021.
		if body["sign"] == nil || body["timestamp"] == nil {
			t.Fatalf("missing sign parameters: %v", body)
		}
		// The card elements must contain a button whose url points at the vulnerability detail.
		elements, _ := card["elements"].([]any)
		foundButton := false
		for _, e := range elements {
			em, _ := e.(map[string]any)
			if em["tag"] != "action" {
				continue
			}
			actions, _ := em["actions"].([]any)
			for _, a := range actions {
				am, _ := a.(map[string]any)
				if am["url"] == "https://artex.local/function/findings/detail?id=42" {
					foundButton = true
				}
			}
		}
		if !foundButton {
			t.Fatal("the card has no button pointing at the detail page")
		}
	})
	cfg := map[string]any{"webhook": srv.URL, "secret": secret}
	if _, err := (feishuChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
}

func TestFeishuWithoutSecretOmitsSign(t *testing.T) {
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["sign"] != nil || body["timestamp"] != nil {
			t.Fatalf("without a secret the sign parameters must be absent: %v", body)
		}
	})
	if _, err := (feishuChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
}

func TestTelegramEscapesHTMLInUntrustedContent(t *testing.T) {
	var text string
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		text, _ = body["text"].(string)
		if body["parse_mode"] != "HTML" {
			t.Fatalf("must use HTML parse mode, got %v", body["parse_mode"])
		}
	})
	m := Message{Items: []Item{{
		Severity: "high",
		// The title and summary come from the target under test / model output and are untrusted content.
		Name:    `<script>alert(1)</script>`,
		Summary: "a & b < c",
	}}}
	if _, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": srv.URL}, m); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if strings.Contains(text, "<script>") {
		t.Fatalf("unescaped HTML, injection is possible: %q", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("expected the escaped entity, got %q", text)
	}
	if !strings.Contains(text, "a &amp; b") {
		t.Fatalf("& was not escaped, got %q", text)
	}
}

func TestTelegramErrorClassification(t *testing.T) {
	rateLimited := capturePost(t, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`, nil)
	_, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": rateLimited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("429 must be retryable, got %v", err)
	}

	forbidden := capturePost(t, `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`, nil)
	_, err = (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": forbidden.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("403 is a configuration problem and must be permanent, got %v", err)
	}
}

func TestWebhookDefaultTemplateProducesValidJSON(t *testing.T) {
	// This is why the default template exists: with a quote and a newline in the title, any naive
	// `"title": "{{.Title}}"` form produces invalid JSON. {{json .}} does not.
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["title"] != `[🟠 High] Login page "SQL injection" risk` {
			t.Errorf("title was not restored correctly: %v", body["title"])
		}
		items, _ := body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items must be 1, got %d", len(items))
		}
		it, _ := items[0].(map[string]any)
		if it["summary"] != "parameter id\nnot filtered, leading to injection" {
			t.Errorf("summary was not restored correctly: %v", it["summary"])
		}
		// Numbers must come out as JSON numbers, not strings (a json:"...,string" tag would hit this pitfall).
		if _, ok := it["finding_id"].(float64); !ok {
			t.Errorf("finding_id should be a number, got %T", it["finding_id"])
		}
	})
	if _, err := (webhookChannel{}).Send(context.Background(), map[string]any{"url": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
}

func TestWebhookCustomTemplateAndHeaders(t *testing.T) {
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, r *http.Request) {
		if r.Header.Get("X-Token") != "s3cret" {
			t.Errorf("custom header lost: %v", r.Header)
		}
		if body["msg"] != "3 items" {
			t.Errorf("custom template rendered incorrectly: %v", body["msg"])
		}
		if body["first"] != "漏洞1" {
			t.Errorf("range extraction is wrong: %v", body["first"])
		}
	})
	cfg := map[string]any{
		"url":           srv.URL,
		"headers":       map[string]any{"X-Token": "s3cret"},
		"body_template": `{"msg": {{json (printf "%d items" .Count)}}, "first": {{json (index .Items 0).Name}}}`,
	}
	if _, err := (webhookChannel{}).Send(context.Background(), cfg, batchMsg(3)); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
}

func TestWebhookRejectsNonJSONRenderResult(t *testing.T) {
	cfg := map[string]any{"url": "https://example.com/hook", "body_template": `not json at all`}
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("a non-JSON render result must be a permanent failure (the template is wrong, retrying will not help), got %v", err)
	}
}

func TestWebhookValidateCatchesBadConfigEarly(t *testing.T) {
	bad := []map[string]any{
		{},
		{"url": "file:///etc/passwd"},
		{"url": "https://example.com", "method": "DELETE"},
		{"url": "https://example.com", "body_template": `{{.Items.`},
	}
	for i, cfg := range bad {
		if err := (webhookChannel{}).Validate(cfg); err == nil {
			t.Errorf("config case %d should be rejected: %v", i, cfg)
		}
	}
}

func TestEmailMessageIsWellFormed(t *testing.T) {
	msg, err := buildEmailMessage("artex@example.com", []string{"a@example.com", "b@example.com"}, singleMsg())
	if err != nil {
		t.Fatalf("failed to build the email message: %v", err)
	}
	if !strings.HasPrefix(msg, "From: artex@example.com\r\n") {
		t.Fatalf("wrong From header:\n%s", msg)
	}
	if !strings.Contains(msg, "To: a@example.com, b@example.com\r\n") {
		t.Fatalf("wrong To header:\n%s", msg)
	}
	// A non-ASCII subject must be RFC 2047 encoded, otherwise clients show mojibake.
	if !strings.Contains(msg, "Subject: =?utf-8?") {
		t.Fatalf("the subject was not RFC 2047 encoded:\n%s", msg)
	}
	if dec, err := new(mime.WordDecoder).DecodeHeader(mustExtractHeader(t, msg, "Subject")); err != nil {
		t.Fatalf("cannot decode the subject: %v", err)
	} else if !strings.Contains(dec, "SQL injection") {
		t.Fatalf("the decoded subject has the wrong content: %q", dec)
	}

	// The body is base64; decoded it must be valid HTML.
	parts := strings.SplitN(msg, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("the message is missing the header/body separator")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(parts[1]), "\r\n", ""))
	if err != nil {
		t.Fatalf("failed to base64-decode the body: %v", err)
	}
	html := string(decoded)
	if !strings.HasPrefix(html, "<div") {
		t.Fatalf("the body is not HTML: %.80s", html)
	}
	// The title appears verbatim in a text position: double quotes are legal characters in HTML text content, so no escaping is needed.
	// The "kept verbatim" assertion guards against a future extra layer of quote escaping
	// that would render the quotes as &quot;.
	if !strings.Contains(html, `"SQL injection"`) {
		t.Fatalf("quotes in the title must be kept as-is in a text position: %.200s", html)
	}
}

// TestEmailEscapesStructuralInjection covers the injection the email body really must prevent:
// vulnerability titles and summaries come from the target under test and model output, so they are untrusted. Text positions must escape
// & < > (or tags could be injected), and attribute positions must also escape quotes (or an href could be closed).
func TestEmailEscapesStructuralInjection(t *testing.T) {
	m := Message{
		Items: []Item{{
			Severity:  "high",
			Name:      `<script>alert(1)</script>`,
			Summary:   "a & b > c",
			DetailURL: `https://artex.local/x?a="onmouseover=alert(1)`,
		}},
	}
	html := htmlBody(m, 0)
	if strings.Contains(html, "<script>") {
		t.Fatalf("the title was not escaped, tags can be injected: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("want the escaped entity: %s", html)
	}
	if !strings.Contains(html, "a &amp; b &gt; c") {
		t.Fatalf("& and > were not escaped: %s", html)
	}
	// The back link is the admin-configurable public_base_url, so it is fairly trustworthy, but an attribute position must still
	// escape quotes — otherwise an address containing a quote closes the href and injects an event handler.
	if strings.Contains(html, `onmouseover=alert(1)">`) {
		t.Fatalf("the href attribute was not escaped correctly: %s", html)
	}
	if !strings.Contains(html, "&quot;") {
		t.Fatalf("quotes in an attribute position must be escaped: %s", html)
	}
}

func mustExtractHeader(t *testing.T, msg, name string) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, name+": ") {
			return strings.TrimPrefix(line, name+": ")
		}
	}
	t.Fatalf("%s header not found", name)
	return ""
}

func TestChannelValidateReportsMissingFields(t *testing.T) {
	// Validation errors are shown straight to the person configuring the channel, so they must say what is missing instead of a vague "invalid config".
	cases := []struct {
		kind   string
		cfg    map[string]any
		substr string
	}{
		{KindDingTalk, map[string]any{}, "The webhook URL is missing"},
		{KindFeishu, map[string]any{}, "The webhook URL is missing"},
		{KindWeCom, map[string]any{}, "The webhook URL is missing"},
		{KindTelegram, map[string]any{}, "The bot token is missing"},
		{KindTelegram, map[string]any{"bot_token": "t"}, "The chat ID is missing"},
		{KindEmail, map[string]any{}, "SMTP server address is missing"},
		{KindEmail, map[string]any{"host": "h"}, "SMTP port is invalid"},
		{KindEmail, map[string]any{"host": "h", "port": 587, "from": "f"}, "At least one recipient address is required"},
	}
	for _, tc := range cases {
		ch, ok := Get(tc.kind)
		if !ok {
			t.Fatalf("channel %s is not registered", tc.kind)
		}
		err := ch.Validate(tc.cfg)
		if err == nil {
			t.Errorf("%s config %v should fail validation", tc.kind, tc.cfg)
			continue
		}
		if !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("the %s error message should mention %q, got %q", tc.kind, tc.substr, err.Error())
		}
	}
}

// TestEmailSMTPErrorClassification pins the semantic split between SMTP 4xx and 5xx.
// If 4xx were also treated as permanent, a mail server with greylisting enabled would make every push land in
// failed after the first attempt — and greylisting is exactly where automatic retries matter most.
func TestEmailSMTPErrorClassification(t *testing.T) {
	cases := []struct {
		reply     string
		permanent bool
	}{
		{"450 4.7.1 Greylisting in action, please come back later", false},
		{"451 4.3.0 Temporary system failure", false},
		{"452 4.2.2 Mailbox full", false},
		{"550 5.1.1 User unknown", true},
		{"553 5.1.3 Bad address syntax", true},
		{"554 5.7.1 Relay access denied", true},
		// When no reply code can be parsed, treat it as retryable: rather one extra attempt than
		// declaring a possibly transient failure dead.
		{"unexpected EOF", false},
		{"", false},
	}
	for _, tc := range cases {
		err := smtpStageError("the recipient address was rejected", errors.New(tc.reply))
		if got := IsPermanent(err); got != tc.permanent {
			t.Errorf("reply %q: want permanent=%v, got %v", tc.reply, tc.permanent, got)
		}
		// However it is classified, the original text must be kept for users to investigate.
		if tc.reply != "" && !strings.Contains(err.Error(), tc.reply) {
			t.Errorf("the original text of reply %q was dropped: %v", tc.reply, err)
		}
	}
}

func TestRegistryCoversAllKinds(t *testing.T) {
	// None of the six channels may go missing — one short and it silently disappears from the UI dropdown.
	want := []string{KindDingTalk, KindEmail, KindFeishu, KindTelegram, KindWebhook, KindWeCom}
	got := Kinds()
	if len(got) != len(want) {
		t.Fatalf("want %d channels, got %d: %v", len(want), len(got), got)
	}
	for _, k := range want {
		if !ValidKind(k) {
			t.Errorf("channel %s is not registered", k)
		}
		if ch, ok := Get(k); !ok || ch.Kind() != k {
			t.Errorf("channel %s has a Kind() that does not match its registry key", k)
		}
	}
	if ValidKind("nope") {
		t.Error("an unregistered kind must not pass validation")
	}
}

func TestPermanentErrorUnwrap(t *testing.T) {
	base := &permanentSentinel{}
	err := Permanent(base)
	if !IsPermanent(err) {
		t.Fatal("it should be recognised as a permanent failure")
	}
	if !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("the error message should pass the underlying error through: %v", err)
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must return nil")
	}
	if IsPermanent(nil) {
		t.Fatal("nil is not a permanent failure")
	}
}

type permanentSentinel struct{}

func (*permanentSentinel) Error() string { return "sentinel" }
