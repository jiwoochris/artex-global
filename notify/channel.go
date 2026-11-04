package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel is the adapter for one notification channel. Implementations must be
// **stateless**: one instance is reused concurrently by several channel
// configurations, so credentials always come in through the cfg argument.
type Channel interface {
	// Kind returns the channel type identifier and must match the registry key.
	Kind() string
	// Validate is called when the configuration is saved and checks required
	// fields and formats. The returned error is shown directly to whoever is
	// configuring the channel, so it must name the missing field rather than say
	// "invalid configuration" generically.
	Validate(cfg map[string]any) error
	// Send delivers one message and returns the number of **entries actually
	// delivered** plus an error.
	//
	// Why the count matters: every platform has a message length limit, and a
	// digest that does not fit is truncated. If the caller unconditionally marked
	// the whole batch as delivered, the dropped entries would vanish — invisible
	// in the message, reported as successful in the delivery history, leaving no
	// place where "this finding was never sent" can be discovered. With kept, the
	// caller marks only the first kept entries and leaves the rest for the next
	// batch.
	//
	// A returned error means delivery failed; *PermanentError means it must not be
	// retried. kept is meaningless on failure and the caller must ignore it.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin returns the per-minute delivery cap the platform
	// recommends, used as the default rate limit for a new channel instance. 0
	// means no known limit.
	DefaultRatePerMin() int
	// SecretKeys returns the names of the credential-bearing keys in this
	// channel's configuration. The API masks their values when echoing the
	// config, and a masked value received on update means "keep the stored
	// value". Only the implementation knows which fields are credentials (the
	// whole WeCom webhook URL is a credential, while for DingTalk only the secret
	// inside it is), so this knowledge must come from the channel rather than be
	// guessed by a caller.
	SecretKeys() []string
	// DestinationKeys returns the names of the keys in this channel's
	// configuration that decide "where the message goes".
	//
	// Like SecretKeys this is security-relevant: destination and credentials are
	// two independent fields, and allowing "change only the destination, keep the
	// credentials" would let anyone who can edit a channel configuration send the
	// real stored credential to a server they control, making the masking of
	// channel configs pointless. See PrepareConfigUpdate.
	DestinationKeys() []string
}

// registry is the channel registry. It deliberately uses an explicit literal
// rather than init() self-registration, so "which channels exist" is visible in
// one place and a missing entry fails at compile time instead of relying on
// runtime side effects.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get returns the channel implementation for a kind.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind reports whether kind is a supported channel type.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds returns every supported channel type in lexicographic order (so UI
// dropdowns render stably).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError marks a delivery failure that must not be retried: bad
// credentials, a rejected target, an invalid request body. Retrying only makes
// sense for transient faults (network jitter, rate limiting, a peer 5xx);
// backing off and retrying a permanent failure never succeeds and drowns the real
// error in retry logs.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent marks err as a permanent failure. A nil err returns nil so call sites
// can write `return Permanent(someCheck())`.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err's chain carries the permanent-failure marker.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- config access helpers ----
//
// Channel configuration comes from a JSONB column and is a map[string]any after
// encoding/json, where numbers are always float64 and arrays are []any. These
// helpers centralize that conversion and tolerate the type drift caused by empty
// UI fields (for example a port submitted as a string).

// cfgString returns a string config value with surrounding whitespace trimmed —
// copying from a web form easily carries it along.
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

// cfgInt returns an integer config value, accepting both float64 (the JSON
// default) and string sources.
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

// cfgBool returns a boolean config value, accepting the strings "true"/"1".
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

// cfgStrings returns a string-array config value, trimming whitespace and
// dropping empty strings.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// Also accept a single string, so a form with only one value still works.
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

// cfgMap returns a string-map config value (such as custom HTTP headers),
// trimming both keys and values and dropping empty keys.
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
