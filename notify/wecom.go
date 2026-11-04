package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit is the hard limit on the markdown content of a WeCom group
// robot (bytes, not characters). This is the tightest limit of the six channels
// and the main reason TruncateBytes exists.
const weComMarkdownLimit = 4096

// weComChannel implements the WeCom group robot.
//
// Platform traits:
//   - Authentication is only the key in the URL; signing is not supported — so the
//     webhook URL is the entire credential.
//   - The markdown content limit is 4096 **bytes** and an over-length message is
//     rejected outright (not truncated). A CJK character is 3 bytes, so the body
//     only has a bit over a thousand characters available and must be truncated
//     client-side.
//   - Rate limited to 20 messages/minute, again absorbed by client-side limiting.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom has exactly one credential, the key in the webhook URL, and it does not
// support signing — the whole URL is the credential, so no other field needs
// masking.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeCom has only the webhook field, which is both the destination and the
// credential, so there is no "credential left over after changing the address".
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("The webhook URL is missing")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("The webhook URL is invalid: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// A digest batch can be long (50 items × one line each plus the prefix) and
	// overflows 4096 bytes easily. Truncating here instead of relying on the
	// platform's error: a rejection loses the whole batch, while truncation at least
	// delivers the first few items.
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
		return 0, fmt.Errorf("Failed to parse the WeCom response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 means the API call limit was exceeded — the platform's rate window
		// scrolls, so backing off and retrying works, hence it is explicitly treated
		// as retryable. Reaching this point means the channel's rate_per_min is set
		// too aggressively; the retry is only a safety net and the real fix is to
		// lower that channel's rate limit.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("The WeCom request was rate limited (%d): %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 means the webhook key is invalid — a permanent failure that retrying
		// cannot fix.
		return 0, Permanent(fmt.Errorf("WeCom returned an error (%d): %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
