package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit is the limit on the text field of Telegram sendMessage (in
// characters).
const telegramTextLimit = 4096

// telegramChannel implements the Telegram Bot API.
//
// Platform traits:
//   - Authentication is entirely in the URL path (/bot<token>/sendMessage); no
//     signing is needed.
//   - The HTML parse mode is used instead of MarkdownV2: MarkdownV2 requires
//     escaping 18 characters (`_*[]()~`>#+-=|{}.!`) and one miss rejects the whole
//     message, while HTML only needs & < >.
//   - Business errors are likewise buried in an HTTP 200 and detected by the ok
//     field.
type telegramChannel struct{}

func (telegramChannel) Kind() string { return KindTelegram }

// Telegram allows about 1 message/second in a private chat and 20/minute in a
// group. The conservative value is used.
func (telegramChannel) DefaultRatePerMin() int { return 20 }

// The bot token is the full credential; chat_id is only the recipient and is not a
// secret (without the token it cannot send anything).
func (telegramChannel) SecretKeys() []string { return []string{"bot_token"} }

// base_url decides which API endpoint the token is sent to (for example a
// self-hosted reverse proxy), so changing it requires re-confirming the token.
func (telegramChannel) DestinationKeys() []string { return []string{"base_url"} }

func (telegramChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("The bot token is missing")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("The chat ID is missing")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("The API address is invalid: %w", err)
		}
	}
	return nil
}

func (c telegramChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := telegramEndpoint(cfg)
	if err != nil {
		return 0, Permanent(err)
	}
	text, kept := telegramHTML(m)
	payload := map[string]any{
		"chat_id":                  cfgString(cfg, "chat_id"),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Failed to parse the Telegram response: %w (%s)", err, snippet(raw))
	}
	if res.OK {
		return kept, nil
	}
	// 429 is rate limiting and a backed-off retry works; everything else (400 bad
	// parameters, 401 bad token, 403 blocked, 404 chat not found) is a
	// configuration problem that retrying cannot fix.
	if res.ErrorCode == 429 {
		return 0, fmt.Errorf("Telegram rate limited the request: %s", res.Description)
	}
	return 0, Permanent(fmt.Errorf("Telegram response error %d: %s", res.ErrorCode, res.Description))
}

// telegramEndpoint assembles the sendMessage address. An empty base_url uses the
// official API; a non-empty one targets a self-hosted Bot API reverse proxy (a
// common need on restricted networks).
func telegramEndpoint(cfg map[string]any) (string, error) {
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token := cfgString(cfg, "bot_token")
	raw := base + "/bot" + token + "/sendMessage"
	u, err := url.Parse(raw)
	if err != nil {
		// err is not passed through: the address contains the bot token, and at this
		// point not even the addr should be echoed back.
		return "", fmt.Errorf("Failed to assemble the API address (API address: %s)", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML renders the HTML body and returns it plus the number of items
// actually written (see Channel.Send).
func telegramHTML(m Message) (string, int) {
	var b strings.Builder
	b.WriteString("<b>" + telegramEscape(markdownTitle(m)) + "</b>\n")
	if m.Batch {
		// Telegram's limit is a **character count**, so packing is measured in runes
		// too (runeSize).
		footer := ""
		if m.HomeURL != "" {
			footer = fmt.Sprintf("\n\n<a href=\"%s\">View everything in the platform</a>", telegramEscapeAttr(m.HomeURL))
		}
		kept := packItemCount(m.Items, telegramTextLimit, telegramReservedRunes, footer, runeSize, func(it Item, idx int) string {
			return telegramBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		b.Reset()
		b.WriteString("<b>" + telegramEscape(telegramBatchTitle(m, items, len(m.Items))) + "</b>")
		for i, it := range items {
			b.WriteString("\n" + telegramEscape(telegramBatchLine(it, i+1)))
		}
		b.WriteString(footer)
		return TruncateHTML(b.String(), telegramTextLimit), kept
	}
	if len(m.Items) == 0 {
		return b.String(), 0
	}
	it := m.Items[0]
	if it.IsStatusChange() {
		b.WriteString(fmt.Sprintf("\n<b>Status change</b>: %s → %s",
			telegramEscape(StatusLabel(it.FromStatus)), telegramEscape(StatusLabel(it.ToStatus))))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		b.WriteString("\n<b>Type</b>: " + telegramEscape(it.VulnClass))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		b.WriteString("\n<b>Assets</b>: " + telegramEscape(a))
	}
	if s := OneLine(it.Summary, maxSummaryRunes); s != "" {
		b.WriteString("\n<b>Summary</b>: " + telegramEscape(s))
	}
	if it.DetailURL != "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">View details</a>", telegramEscapeAttr(it.DetailURL)))
	}
	return TruncateHTML(b.String(), telegramTextLimit), 1
}

// telegramReservedRunes is reserved for the message title and any truncation note
// (measured in characters).
const telegramReservedRunes = 160

// telegramBatchLine renders one entry in a digest (unescaped; the caller escapes
// uniformly).
func telegramBatchLine(it Item, idx int) string {
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		return fmt.Sprintf("%d. %s · %s — %s", idx, SeverityLabel(it.Severity), it.Title(), a)
	}
	return fmt.Sprintf("%d. %s · %s", idx, SeverityLabel(it.Severity), it.Title())
}

// telegramBatchTitle renders the digest title line. The count shown is the number
// of items **this message actually contains**, not the batch total — otherwise the
// reader assumes the number in the header is everything.
func telegramBatchTitle(m Message, items []Item, total int) string {
	title := fmt.Sprintf("Vulnerability digest · %d total", total)
	if extra := total - len(items); extra > 0 {
		title += fmt.Sprintf(" (showing the first %d; the remaining %d follow in the next message)", len(items), extra)
	}
	if m.WindowMinutes > 0 {
		title = fmt.Sprintf("Last %d minutes · %s", m.WindowMinutes, title)
	}
	return title
}

// telegramEscape escapes HTML text content.
// Telegram only understands these three entities, and escaping double-escapes
// pre-existing entities such as &amp; — which is the intended behavior: we want to
// display the original characters, not let the user inject HTML.
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr escapes an HTML attribute value. On top of the text escaping
// it handles quotes — a quote inside a URL would close the href attribute early and
// turn the rest into an injection point.
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
