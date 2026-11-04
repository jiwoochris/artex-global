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

// allowLocalTargets decides whether messages may be delivered to loopback or
// link-local addresses.
//
// Default is deny. Those ranges are not where IM bots or public mail servers live,
// and what they can reach is sensitive: the admin port of another service on the
// same host, or the cloud metadata endpoint (169.254.169.254, which hands out
// instance credentials). The delivery address is set by an administrator, but a
// management session hijacked through XSS/CSRF, or a second person sharing the
// same JWT, can change the config and read the response back: doJSON writes the
// first 200 bytes of a 4xx/5xx body into last_error and the delivery history
// endpoint echoes it, which is a semi-blind read primitive.
//
// A local SMTP relay (postfix on 127.0.0.1:25) is a common self-hosted mail setup
// though, so rather than hard-coding an allow there is an explicit escape hatch:
// set ARTEX_NOTIFY_ALLOW_LOCAL=1 to permit it.
//
// It is exported as AllowLocalTargetsEnv so tests can turn it on explicitly - the
// cases in this package and in server use httptest receivers on 127.0.0.1
// extensively and would all be blocked by the guard otherwise.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP reports whether the target IP is in a range that is not
// deliverable by default.
//
// It rejects only loopback, link-local (including the cloud metadata address
// 169.254.169.254), unspecified and multicast. RFC1918 private networks are **not**
// rejected: self-hosted Mattermost or an SMTP relay on the internal network is a
// common legitimate deployment, and blocking those would make the feature unusable
// in real environments. The tradeoff is deliberate - the guard must stop genuinely
// sensitive targets without breaking normal deployments.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) must be reduced to IPv4 before the check,
	// otherwise the guard is bypassed.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial is the Control hook of the http.Transport dialer; it checks
// the target address **when the connection is established**.
//
// The check lives at dial time rather than only when the config is saved because
// this is where it actually takes effect. It also covers the two ways around
// config validation - DNS rebinding (a public IP at validation time, an internal
// one at connect time) and redirects (cross-host hops are already rejected, but a
// same-host hop can still point the path elsewhere).
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
		return fmt.Errorf("could not resolve the target address: %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("not sending to local or link-local address %s (set %s=1 to allow a local service)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport adds a dial guard to the default Transport.
// Clone keeps all the default tuning (connection pool, HTTP/2, timeouts, proxy),
// so adding one check changes nothing else.
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
// on the server side): that proxy carries traffic to penetration targets, is often
// an unstable tunnel, and notification availability must not be hostage to the
// target network's flakiness. IM pushes go out directly. The timeout is 15s - a
// peer slower than that is effectively broken.
//
// Cross-host redirects are rejected: every delivery address in this feature is a
// single fixed endpoint and should never redirect to another host, while these
// vendors' credentials (DingTalk access_token, WeCom key, Telegram bot token) are
// **in the URL**, so following a cross-host hop hands the credential to the
// redirect target. Same-host hops (such as adding a trailing slash) are allowed.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("redirects to a different host are not allowed (%s -> %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit caps how much of the response body is read. A misbehaving peer can
// return an enormous payload, while all we need is the status code and a short
// error description for the delivery history.
const respBodyLimit = 8 << 10

// doJSON sends one request and returns the response body (already length-capped).
//
// A nil payload sends an empty body (for GET, or platforms that do not require a
// body). Headers are attached verbatim, which generic webhooks use for custom
// headers.
//
// Error classification is this function's core responsibility: network-layer
// failures and 5xx/408/429 are "retryable"; other 4xx are "permanent" - retrying a
// 403 only writes the same error to the log three times.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// A marshal failure is a local bug (wrong config field type); retrying will
			// not help.
			return nil, Permanent(fmt.Errorf("could not build the request body: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// The URL is invalid - almost always the user mistyped the address, which is a
		// permanent failure. err must not be passed through here either: url.Parse's
		// error text contains the full address.
		return nil, Permanent(fmt.Errorf("the request URL is not valid: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection refused, DNS failure, timeout - mostly transient, so leave it to
		// the backoff retry.
		//
		// The error text must be redacted before it leaves this function. Reason:
		// http.Client.Do returns a *url.Error whose Error() is `Op "full URL": cause`,
		// and these vendors' credentials are **in the URL** (DingTalk access_token,
		// WeCom key, Feishu hook id, Telegram /bot<token>/). Unredacted, the credential
		// flows through this error string to four places: notification_deliveries
		// last_error (stored in plain text), the delivery history endpoint response
		// (**bypassing the channel config masking**), the server log, and the 502 text
		// the test-send endpoint returns to the frontend.
		return nil, fmt.Errorf("the request failed: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("could not read the response: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (rate limited) and 408 (timeout) are worth retrying; other 4xx are config
	// or permission problems where a retry is pointless.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("the remote server rate limited the request or responded too slowly (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("the remote server returned an error (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("the remote server rejected the request (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet flattens the response body into one short line for error messages. A
// response can carry newlines and lots of whitespace, and dumping it into
// last_error breaks the layout of the delivery history page.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget reduces a delivery address to "scheme://host/..." for error
// messages.
//
// This is the single redaction convention of this package and is deliberately
// **blunt**: everything but scheme and host is dropped. There is no generic and
// safe way to tell which part of a URL is a credential:
//
//	DingTalk  credential in query       /robot/send?access_token=xxx
//	WeCom     credential in query       /cgi-bin/webhook/send?key=xxx
//	Feishu    credential in **last path segment** /open-apis/bot/v2/hook/<hook_id>
//	Telegram  credential in **middle path segment** /bot<token>/sendMessage
//
// Keeping "only the useful part" would mean per-channel patches, and missing any
// one vendor is one credential leak. Keeping the host is enough for diagnosis (DNS
// resolution, connection and certificate problems are all locatable); which bot it
// is can be told from the masked tail shown in the channel config.
//
// On a parse failure a fixed placeholder is returned - the raw string is never
// echoed back.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(address could not be parsed)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError strips the address from a transport error, keeping only the
// underlying cause.
//
// *url.Error is {Op, URL, Err} and its Error() prints the URL as well. Reading the
// Err field explicitly sidesteps that Error() - more reliable than string
// replacement afterwards, which has to handle every URL-encoded/escaped variant and
// tends to miss some.
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
		return fmt.Sprintf("%s %s: unknown error", uerr.Op, host)
	}
	// Errors that are not *url.Error (such as the redirect policy error) can also
	// carry an address, so they all go through redaction.
	return redactURLsInText(err.Error())
}

// redactURLsInText replaces every http(s) address in a text with its redacted form.
//
// It backs up the errors that expose no structured fields (redirect policy errors,
// custom errors from third-party libraries). Only the http/https prefix is
// recognised and splitting is on whitespace and quotes - an address cannot contain
// either.
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
