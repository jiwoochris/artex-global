package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets decides whether messages may be delivered to loopback /
// link-local addresses.
//
// Denied by default. These address ranges are not where IM bots or public mail
// servers live, and what they can reach is sensitive: the admin port of another
// service on the same host, and the cloud environment's metadata endpoint
// (169.254.169.254, which can expose instance credentials). The delivery address is
// configured by an admin, but an admin session borrowed via XSS/CSRF, or a second
// person sharing the same JWT, could read response content back by changing the
// configuration — doJSON writes the first 200 bytes of a 4xx/5xx response body into
// last_error, and the delivery history API echoes it back, which makes a semi-blind
// read primitive.
//
// But a "local SMTP relay" (postfix on 127.0.0.1:25) is a common setup for
// self-hosted mail, and a blanket ban would leave people stuck. So an explicit escape
// hatch is provided instead of a hard-coded allowance: setting
// ARTEX_NOTIFY_ALLOW_LOCAL=1 allows it.
//
// It is exported as AllowLocalTargetsEnv so tests can turn it on explicitly — test
// cases in this package and the server package make heavy use of httptest fake
// receivers on 127.0.0.1, and without turning it on they would all be blocked by the
// guard.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP reports whether the target IP belongs to the address ranges that
// "are not allowed as delivery targets by default".
//
// Only loopback, link-local (including cloud metadata 169.254.169.254), unspecified,
// and multicast are rejected. RFC1918 private networks are **not rejected**:
// self-hosted Mattermost / SMTP relays on an internal network are a very common
// legitimate use, and blocking them as well would make the feature outright unusable
// in real environments. This trade-off is deliberate — the protection must block the
// truly sensitive targets without crippling normal deployments along with them.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// An IPv4-mapped IPv6 address (::ffff:127.0.0.1) must be converted back to IPv4
	// before checking, otherwise the check is bypassed.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial is the Control hook of the http.Transport dialer; it checks the
// target address **at connection time**.
//
// Why it sits at the dial stage rather than only validating when the configuration is
// saved: this is where it ultimately takes effect. It covers both ways of bypassing
// configuration validation — DNS rebinding (resolving to a public IP during
// validation and to an internal one at actual connection time) and redirects
// (although we already refuse cross-host redirects, a same-host redirect could still
// point the path elsewhere).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("대상 주소를 해석할 수 없습니다: %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("로컬 또는 링크 로컬 주소 %s 로는 전송하지 않습니다 (로컬 서비스로 보내야 하면 %s=1 로 설정하세요)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport adds only a dial guard on top of the default Transport.
// It uses Clone to keep all of the default tuning (connection pool, HTTP/2, timeouts,
// proxy, etc.), avoiding changing any other behavior just to add one check.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient is the client shared by all channel deliveries.
//
// It deliberately does **not** reuse the project's global egress proxy (GlobalProxy
// on the server side): that proxy is for pentest target traffic and is often an
// unstable tunnel, and notification availability should not be held hostage by
// jitter in the target network. IM pushes can connect directly. The timeout is 15
// seconds — a peer slower than that is effectively already in a failed state.
//
// Cross-host redirects are refused: the delivery addresses for this feature all have
// the shape of "one fixed endpoint" and normally never redirect to another host; and
// the credentials of these providers (DingTalk's access_token, WeCom's key, Telegram's
// bot token) **are in the URL**, so following a cross-host redirect amounts to handing
// the credential to the redirect target. Same-host redirects (e.g. appending a
// trailing slash) are still allowed.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("리다이렉트가 너무 많습니다")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("다른 호스트로의 리다이렉트는 허용하지 않습니다 (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit caps how much of the response body is read. A misbehaving peer may
// spit back huge content, while we only need the error code and a short error
// description to show in the delivery history.
const respBodyLimit = 8 << 10

// doJSON sends one request and returns the response body (length-capped).
//
// When payload is nil an empty body is sent (for GET, or when the platform does not
// require a body).
// The key/values in headers are attached as-is, for the generic Webhook's custom
// headers.
//
// Error classification is this function's core responsibility: network-layer
// failures and 5xx/408/429 are classified as "retryable", and other 4xx as "permanent
// failure" — retrying a 403 just writes the same error to the logs 3 times.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// A serialization failure is a local bug (wrong config field type); retrying will not
			// make it better.
			return nil, Permanent(fmt.Errorf("요청 본문을 만들지 못했습니다: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// Invalid URL — most likely the user entered the address wrong, which is a permanent
		// failure.
		// err must not be passed through here either: url.Parse's error text contains the
		// full URL.
		return nil, Permanent(fmt.Errorf("요청 주소가 올바르지 않습니다: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection refused, DNS failure, timeout — mostly transient faults, left to
		// backoff retry.
		//
		// The error text must be redacted before it is passed on. Reason: http.Client.Do
		// returns a *url.Error whose Error() is `Op "full URL": underlying error`, and the
		// credentials of these providers **are in the URL** (DingTalk access_token, WeCom
		// key, Feishu hook id, Telegram /bot<token>/). Without redaction, the credential
		// would flow along this error string to four places: notification_deliveries'
		// last_error (persisted to the DB in plaintext), the delivery history API's response
		// (**bypassing the channel configuration's masking**), the server logs, and the 502
		// text the test-send endpoint returns to the frontend.
		return nil, fmt.Errorf("요청에 실패했습니다: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("응답을 읽지 못했습니다: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (rate limiting) and 408 (timeout) are worth retrying; other 4xx are
	// configuration or permission problems, and retrying is pointless.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("상대 서버가 요청을 제한하거나 응답이 지연되었습니다 (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("상대 서버에 오류가 발생했습니다 (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("상대 서버가 요청을 거부했습니다 (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet compresses the response body into one short line of text for error
// messages. Responses may contain newlines and lots of whitespace, and stuffing them
// straight into last_error would break the layout of the delivery history page.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget reduces a delivery address to "scheme://host/…" for error
// messages.
//
// This is the package's single address-redaction rule, and it is deliberately **blunt
// enough**: everything other than scheme and host is dropped. The reason is that there
// is no "general and safe" way to tell which part of a URL is the credential:
//
//	DingTalk  credential in the query          /robot/send?access_token=xxx
//	WeCom     credential in the query          /cgi-bin/webhook/send?key=xxx
//	Feishu    credential in the **last path segment** /open-apis/bot/v2/hook/<hook_id>
//	Telegram  credential in the **middle of the path** /bot<token>/sendMessage
//
// "Keeping only the useful parts" would require per-channel patches, and missing any
// one provider means a credential leak. Keeping the host is already enough for
// troubleshooting (DNS failures, connection failures, and bad certificates can all be
// pinpointed), and which specific bot it is can be recognized from the masked
// suffix hint in the channel configuration.
//
// On a parse failure a fixed placeholder is returned — the original string is never
// echoed back.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(주소를 해석할 수 없음)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError strips the address from a transport-layer error, keeping only
// the underlying cause.
//
// A *url.Error is structured as {Op, URL, Err}, and its Error() prints the URL along
// with it. Here the Err field is taken explicitly, bypassing its Error() — more
// reliable than string replacement after the fact, because replacement has to cope
// correctly with all the URL-encoded/escaped variants and is easy to get wrong.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: 알 수 없는 오류", uerr.Op, host)
	}
	// Errors that are not *url.Error (such as those returned by the redirect policy) may
	// also carry an address, so they all go through redaction.
	return redactURLsInText(err.Error())
}

// redactURLsInText replaces http(s) URLs appearing in a piece of text with their
// redacted form.
//
// Used as a fallback for errors where structured fields are not available (redirect
// policy errors, custom errors from third-party libraries). Only the http/https
// prefixes are recognized, splitting on whitespace and quotes — URLs never contain
// those two kinds of characters.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
