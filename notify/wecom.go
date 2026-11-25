package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit is the hard limit (in bytes, not characters) on the markdown
// content of a WeCom group bot. It is the tightest limit of all six channels and the
// main reason TruncateBytes exists.
const weComMarkdownLimit = 4096

// weComChannel implements the WeCom (WeChat Work) group bot.
//
// Platform characteristics:
//   - Authenticates solely via the key in the URL and does not support signing —
//     so the webhook URL itself is the entire credential.
//   - markdown content is limited to 4096 **bytes**, and an over-long message is
//     rejected outright (not truncated). Chinese takes 3 bytes per character, which
//     means only a little over a thousand characters of body text fit, so it must be
//     truncated client-side.
//   - Rate limit of 20 messages/minute, likewise absorbed by client-side rate
//     limiting.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom has only one credential, the Webhook (the key in the URL), and it does not
// support signing — the whole URL is the entire credential, and no other field
// needs masking.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeCom has only the single Webhook field, which is both the destination and the
// credential, so there is no such thing as "credentials left behind after changing
// the destination".
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Webhook 주소가 없습니다")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 주소가 올바르지 않습니다: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// A digest batch can be long (50 items × one line each + a prefix), so 4096 bytes is
	// easily exceeded. Truncation is done here rather than relying on a platform error:
	// a rejection means the whole batch is lost, while truncation at least delivers the
	// first several items.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("WeCom 응답을 해석하지 못했습니다: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 means the API call limit was exceeded — the platform's rate-limit window
		// rolls, so retrying after backoff is effective, and it is explicitly classified as
		// retryable. Reaching this point means the client-side rate_per_min is configured
		// too aggressively; retrying is only a fallback, and the real fix is to lower this
		// channel's rate limit.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("WeCom 요청이 제한되었습니다 (%d): %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 means the webhook key is invalid — a permanent failure that retrying will
		// not fix.
		return 0, Permanent(fmt.Errorf("WeCom에서 오류가 발생했습니다 (%d): %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
