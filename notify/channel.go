package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel is the adapter for one notification channel. Implementations must be
// **stateless**: the same instance is reused concurrently by multiple channel
// configurations, and credentials are always passed in via the cfg parameter.
type Channel interface {
	// Kind returns the channel type identifier; it must match the registry key.
	Kind() string
	// Validate is called when a configuration is saved and checks required fields and
	// formats. The returned error is shown directly to whoever is configuring the
	// channel, so the message should say "which field is missing" rather than a vague
	// "invalid configuration".
	Validate(cfg map[string]any) error
	// Send delivers one message and returns **the number of items actually delivered**
	// and an error.
	//
	// Why return a count: every platform has a message length limit, and a digest
	// message that cannot fit the whole batch gets truncated. If the caller
	// unconditionally marked the whole batch as delivered, the truncated items would
	// vanish — not visible in the message, shown as successful in the delivery
	// history, with nowhere to discover that the findings were never sent. With kept
	// returned, the caller marks only the first kept items and leaves the rest for the
	// next batch.
	//
	// A returned error means delivery failed; a *PermanentError means it should not be
	// retried. On failure kept is meaningless and the caller should ignore it.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin returns the channel's officially recommended per-minute
	// delivery limit, used as the default rate limit when creating a new channel
	// instance. Returning 0 means there is no known limit.
	DefaultRatePerMin() int
	// SecretKeys returns the keys in this channel's configuration that hold
	// credentials. When the API echoes a configuration, the values of these keys are
	// masked; when an update receives a masked value, the original value in the DB is
	// kept. Only the implementation itself knows which fields count as credentials
	// (for WeCom the entire webhook URL is the credential, while for DingTalk it is
	// only the secret within it), so this knowledge must come from the channel and
	// cannot be guessed by the layer above.
	SecretKeys() []string
	// DestinationKeys returns the keys in this channel's configuration that decide
	// "where the message is sent".
	//
	// Like SecretKeys, this is security-relevant: the destination and the credentials
	// are two independent sets of fields. If "change only the destination and keep the
	// credentials as they are" were allowed, anyone able to edit the channel
	// configuration could send the real credentials stored in the DB to a server they
	// control, and masking the channel configuration would become completely
	// meaningless. See PrepareConfigUpdate.
	DestinationKeys() []string
}

// registry is the channel registry. It deliberately uses an explicit literal rather
// than init() self-registration: this way "which channels exist" can be seen in one
// place, and a missing entry for a new channel surfaces at compile time rather than
// relying on runtime side effects.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get looks up the channel implementation by type.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind reports whether kind is a supported channel type.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds returns all supported channel types in lexical order (so the UI dropdown
// is displayed stably).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError marks a delivery failure that should not be retried: bad
// credentials, rejection by the target, an invalid request body, and so on.
// Retrying only makes sense for transient faults (network jitter, rate limiting, a
// peer 5xx); repeatedly backing off and retrying a permanent failure will never
// succeed and buries the real error in the retry logs.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent marks err as a permanent failure. It returns nil when err is nil, so
// it can be written as `return Permanent(someCheck())`.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether the err chain carries a permanent-failure marker.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- Config-reading helpers ----
//
// Channel configuration comes from a JSONB column in the database; after
// encoding/json deserialization it is a map[string]any where numbers are always
// float64 and arrays are []any. The helpers below unify this conversion and
// tolerate type mismatches caused by users leaving fields blank in the UI (e.g.
// entering a port as a string).

// cfgString reads a string config item, always trimming leading and trailing
// whitespace — copy-pasting from a web form easily brings some along.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt reads an integer config item, accepting both float64 (the JSON default)
// and string sources.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool reads a boolean config item, also accepting the strings "true"/"1".
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings reads a string-array config item, trimming whitespace automatically
// and dropping empty strings.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// A single string is also accepted, which makes form submissions with only one
		// value convenient.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap reads a string-map config item (e.g. custom HTTP headers), trimming
// whitespace from both keys and values and dropping empty keys.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
