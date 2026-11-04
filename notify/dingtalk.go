package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel implements the DingTalk custom robot.
//
// Platform traits (which drive the implementation choices here):
//   - A single robot is rate limited to 20 messages/minute and over-sends are
//     silently dropped (the HTTP status may still be 200), so rate limiting must
//     happen on the client — see DefaultRatePerMin.
//   - The security setting is one of three: signed requests / custom keyword / IP
//     allowlist. Signing is the only option that does not depend on message
//     content, so only signing is supported (plus a bare webhook with none of the
//     three enabled).
//   - Success and failure both return HTTP 200 and are distinguished by the
//     errcode in the body — not checking errcode would record a failed delivery as
//     a success.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// A DingTalk webhook URL carries the access_token and is itself a credential, so
// the whole value is masked.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// The destination is the DingTalk webhook URL itself; changing it must be done
// together with re-confirming the signing secret for the new address.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("The webhook URL is missing")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("The webhook URL is invalid: %w", err)
	}
	return nil
}

// Send delivers one message. It uses ActionCard (with a button) when there is a
// back-link and only one item, and markdown otherwise.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// DingTalk markdown has no documented byte limit, but a cap is still applied so
	// an abnormally inflated evidence field cannot blow up the request.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "View details",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// DingTalk hides business errors inside a 200 response.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Failed to parse the DingTalk response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000 is a signature verification failure and 310000 a keyword mismatch —
		// both are configuration errors that retrying cannot fix.
		return 0, Permanent(fmt.Errorf("DingTalk returned an error (%d): %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL appends the timestamp and sign parameters to a webhook
// according to the official signing rule.
//
// Rule: the string to sign is timestamp + "\n" + secret, the HMAC-SHA256 **key is
// also the secret**, and the result is base64-encoded then URL-encoded. The
// timestamp is in milliseconds. An empty secret returns the URL unchanged, to
// support robots without signing enabled.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// err is not passed through: url.Parse's error text contains the full URL
		// (including the access_token).
		return "", fmt.Errorf("Failed to parse the webhook URL: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL checks that the address is usable and the scheme supported, and
// applies the internal-address test to literal IP targets.
//
// Two things matter here:
//
//  1. **Error messages must be redacted.** url.Parse itself returns a *url.Error
//     whose Error() carries the **full original address**, and for the platforms
//     this feature talks to the address embeds credentials (DingTalk access_token,
//     WeCom key, Telegram bot token, Feishu hook id). This used to `return err`
//     directly, so the message "invalid address format" leaked the credential into
//     the 400 response of the test endpoint, the per-delivery last_error column,
//     the server log, and the delivery history API.
//
//  2. **A literal IP is checked for being internal right away**, while domain
//     names are left to the dial phase (blockInternalDial is the final gate and
//     also covers DNS rebinding). Doing it here means the misconfiguration is
//     reported when the config is saved, instead of only when the first delivery
//     fails.
//
// Restricting the scheme is defensive: file:// or gopher:// would make
// http.Client behave in unexpected ways (already blocked by the scheme check, but
// there is no reason to open that surface).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("Could not parse the address (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Only http or https is supported. Received: %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("The host name is missing")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("Not sending to the local or link-local address %s (to target a local service, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
