package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestMaskedValueHidesBodyButKeepsTailHint(t *testing.T) {
	const secret = "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456"
	got := MaskedValue(secret)
	if strings.Contains(got, "abcdef123456") {
		t.Fatalf("masked value leaks the whole credential: %q", got)
	}
	if strings.Contains(got, "oapi.dingtalk.com") {
		t.Fatalf("masked value must not expose the address body: %q", got)
	}
	// Keep the last 6 characters so the user can tell which bot it is.
	if !strings.HasSuffix(got, "123456") {
		t.Fatalf("the last 6 characters must survive as the identifying hint: %q", got)
	}
	if !IsMasked(got) {
		t.Fatalf("masked value must be recognized by IsMasked: %q", got)
	}
}

func TestMaskedValueShortSecretGivesNoHint(t *testing.T) {
	// Exposing the last 6 characters of a short secret would expose the whole secret.
	for _, s := range []string{"abc", "abcdef", ""} {
		got := MaskedValue(s)
		if got != MaskedPrefix {
			t.Fatalf("a %d-byte credential must not get a tail hint, got %q", len(s), got)
		}
		if s != "" && strings.Contains(got, s) {
			t.Fatalf("masked value contains the original value: %q", got)
		}
	}
}

func TestMaskConfigMasksOnlySecrets(t *testing.T) {
	cfg := map[string]any{
		"webhook": "https://example.com/hook?token=SECRETVALUE",
		"secret":  "SECtest123456",
		"port":    float64(587),
		"host":    "smtp.example.com",
	}
	masked := MaskConfig(KindDingTalk, cfg)
	for _, k := range []string{"webhook", "secret"} {
		s, _ := masked[k].(string)
		if !IsMasked(s) {
			t.Errorf("%s should be masked, got %q", k, s)
		}
	}
	// Non-credential fields must be returned unchanged, otherwise the UI cannot render them.
	if masked["port"] != float64(587) {
		t.Errorf("non-credential field port must not change: %v", masked["port"])
	}
}

func TestMaskConfigUnknownKindReturnsEmpty(t *testing.T) {
	// With an unrecognized channel kind, showing an empty config beats echoing back content that may hold credentials.
	got := MaskConfig("nope", map[string]any{"webhook": "https://x/y?token=LEAK"})
	if len(got) != 0 {
		t.Fatalf("unknown channel kind must yield an empty config, got %v", got)
	}
}

func TestMaskConfigDoesNotMutateInput(t *testing.T) {
	// Masking is a presentation-layer behaviour; it must never write masked values back over the stored ones.
	cfg := map[string]any{"webhook": "https://example.com/hook", "secret": "SECtest123456"}
	_ = MaskConfig(KindDingTalk, cfg)
	if IsMasked(cfg["secret"].(string)) {
		t.Fatal("MaskConfig mutated its input, which would overwrite the real credential with the masked value")
	}
}

func TestMergeConfigKeepsStoredOnMaskedIncoming(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET", "method": "POST"}
	// The user only changed method, so the browser submits the masked value plus the new method.
	incoming := map[string]any{
		"webhook": MaskedValue("https://real/hook"),
		"secret":  MaskedValue("REALSECRET"),
		"method":  "PUT",
	}
	got := MergeConfig(stored, incoming)
	if got["webhook"] != "https://real/hook" || got["secret"] != "REALSECRET" {
		t.Fatalf("masked field must keep the stored value, got %v", got)
	}
	if got["method"] != "PUT" {
		t.Fatalf("the changed field must take effect, got %v", got["method"])
	}
}

func TestMergeConfigEmptyStringClears(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET"}
	got := MergeConfig(stored, map[string]any{"secret": ""})
	if _, ok := got["secret"]; ok {
		t.Fatalf("an empty string must clear that field, got %v", got)
	}
	// Fields not mentioned are kept (partial-update semantics).
	if got["webhook"] != "https://real/hook" {
		t.Fatalf("unmentioned field must be preserved, got %v", got)
	}
}

func TestMergeConfigKeepsUnmentionedStoredKeys(t *testing.T) {
	stored := map[string]any{"host": "smtp.example.com", "port": float64(587), "password": "pw"}
	got := MergeConfig(stored, map[string]any{"port": float64(465)})
	if got["host"] != "smtp.example.com" || got["password"] != "pw" {
		t.Fatalf("unmentioned field must be preserved, got %v", got)
	}
	if got["port"] != float64(465) {
		t.Fatalf("the mentioned field must be updated, got %v", got["port"])
	}
}

// TestPrepareConfigUpdateBlocksDestinationSwap pins this package's most important security invariant:
// **changing the destination must not carry the old credential over**.
//
// These cases use attacker-shaped input (change only the address, say nothing about the credential)
// rather than "the correct input for the guard" - testing only the latter stays green even if the guard is broken.
func TestPrepareConfigUpdateBlocksDestinationSwap(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
		// wantMissing is the credential key expected to be named.
		wantMissing string
	}{
		{
			name: "generic webhook changes address, hoping to keep the Authorization header",
			kind: KindWebhook,
			stored: map[string]any{
				"url":     "https://legit.example.com/hook",
				"headers": map[string]any{"Authorization": "Bearer REAL-TOKEN"},
			},
			incoming:    map[string]any{"url": "https://attacker.tld/c"},
			wantMissing: "headers",
		},
		{
			name:        "Telegram changes base_url, hoping to send the Bot Token to its own endpoint",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld"},
			wantMissing: "bot_token",
		},
		{
			name:        "email changes the SMTP host, hoping to hand over the password",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"host": "smtp.attacker.tld"},
			wantMissing: "password",
		},
		{
			name:        "email turning TLS off must still re-declare the password",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "tls": false, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"tls": true},
			wantMissing: "password",
		},
		{
			// A masked value means "reuse the old credential", which must be rejected just as firmly when the address changed.
			name:        "masked credential sent back with a new address",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld", "bot_token": MaskedValue("123456:REAL")},
			wantMissing: "bot_token",
		},
		{
			name:        "DingTalk changes the webhook, hoping to keep the signing secret",
			kind:        KindDingTalk,
			stored:      map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=OLD", "secret": "REALSEC"},
			incoming:    map[string]any{"webhook": "https://attacker.tld/hook"},
			wantMissing: "secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err == nil {
				t.Fatalf("address changed without re-declaring the credential must be rejected; got config %v", merged)
			}
			var target *ErrDestinationChangedWithoutCredentials
			if !errors.As(err, &target) {
				t.Fatalf("want a dedicated error type so the API can give an actionable hint, got %T: %v", err, err)
			}
			found := false
			for _, m := range target.Missing {
				if m == tc.wantMissing {
					found = true
				}
			}
			if !found {
				t.Fatalf("must name the missing credential key %q, got %v", tc.wantMissing, target.Missing)
			}
			// The error message must tell the operator how to fix it.
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("error message must mention %q: %v", tc.wantMissing, err)
			}
		})
	}
}

// TestPrepareConfigUpdateAllowsLegitimateEdits is the reverse case: ordinary edits must not be blocked,
// otherwise the guard gets bypassed or deleted for being too annoying.
func TestPrepareConfigUpdateAllowsLegitimateEdits(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
	}{
		{
			name:     "only the name changes (config echoed back unchanged)",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "headers": map[string]any{"Authorization": "Bearer REAL"}},
			incoming: map[string]any{"url": MaskedValue("https://legit.example.com/hook")},
		},
		{
			name:     "only the request method changes, address and credential untouched",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "method": "POST"},
			incoming: map[string]any{"method": "PUT"},
		},
		{
			name:     "address swapped **together with** a new credential",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": map[string]any{"Authorization": "Bearer NEW"}},
		},
		{
			name:     "address swapped with an explicit statement that no credential is needed anymore",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": ""},
		},
		{
			name:     "Telegram changes chat_id (not the destination)",
			kind:     KindTelegram,
			stored:   map[string]any{"bot_token": "t", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"chat_id": "-100200"},
		},
		{
			name:     "email changes the recipient (not the destination)",
			kind:     KindEmail,
			stored:   map[string]any{"host": "smtp.corp.com", "port": 587, "password": "PW", "from": "a@b.c", "to": []any{"x@y.z"}},
			incoming: map[string]any{"to": []any{"new@y.z"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err != nil {
				t.Fatalf("legitimate edit was blocked: %v", err)
			}
			if merged == nil {
				t.Fatal("want a merged result")
			}
		})
	}
}

// TestPrepareConfigUpdatePortTypeTolerance covers an easy slip: the port the frontend submits
// is a JSON number (float64) and the stored one reads back as float64, but the two values may
// differ in type (int vs float64). Comparing with == reports "changed" for "unchanged", which
// pops "please re-enter the password" at a user who only renamed the channel - false alarms erode trust in the guard.
func TestPrepareConfigUpdatePortTypeTolerance(t *testing.T) {
	stored := map[string]any{"host": "smtp.corp.com", "port": float64(587), "password": "PW"}
	// The same port, submitted as an int.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 587}); err != nil {
		t.Fatalf("equal port values (differing only in type) must not count as a destination change: %v", err)
	}
	// A genuinely different port must still be blocked.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 25}); err == nil {
		t.Fatal("a port change must be blocked")
	}
}

// TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination covers the "destination field that
// may be left blank" path: an empty Telegram base_url means the official API address.
//
// This used to make the channel unsavable from the second save on:
//
//	creation stores base_url:"" (the create path stores the submitted config verbatim, without MergeConfig)
//	→ first save, MergeConfig treats the empty string as an explicit clear and deletes the key
//	→ second save, incoming is still "" while stored no longer has the key, so it counts as "address changed"
//	→ bot_token is the masked echo → 400 "destination address changed, re-enter the credential fields"
//
// The user changed nothing and could never save again without pasting the Bot Token afresh.
func TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination(t *testing.T) {
	stored := map[string]any{"bot_token": "123:ABC", "chat_id": "-100", "base_url": ""}

	// The frontend's buildConfig() submits a value for every field defined for the channel: credentials as the masked echo,
	// empty text boxes as empty strings. This reproduces its output exactly instead of submitting only the changed keys.
	submit := func() map[string]any {
		return map[string]any{
			"bot_token": MaskedValue("123:ABC"),
			"chat_id":   "-100",
			"base_url":  "",
		}
	}

	// First save: only the channel name changed, config echoed back unchanged.
	merged, err := PrepareConfigUpdate(KindTelegram, stored, submit())
	if err != nil {
		t.Fatalf("first save was blocked: %v", err)
	}
	if _, ok := merged["base_url"]; ok {
		t.Fatal("premise changed: the empty string should have been deleted by MergeConfig - this case is about that key being gone")
	}

	// Second save: identical payload, the user changed nothing.
	merged2, err := PrepareConfigUpdate(KindTelegram, merged, submit())
	if err != nil {
		t.Fatalf("second save was blocked (the user changed nothing): %v", err)
	}
	// Third save, to confirm it stays savable rather than failing just once.
	if _, err := PrepareConfigUpdate(KindTelegram, merged2, submit()); err != nil {
		t.Fatalf("third save was blocked: %v", err)
	}
	// The credential must survive all the way, not be wiped by the empty-string handling.
	if got := merged2["bot_token"]; got != "123:ABC" {
		t.Fatalf("Bot Token must keep its stored value, got %v", got)
	}
}

// TestPrepareConfigUpdateStillGuardsBlankDestinationChanges is the counterweight to the case above:
// treating an empty string as equivalent to "key absent" must **not** also wave through a real address change.
// Both directions are real credential-exfiltration paths - Telegram's Bot Token travels in the URL path, so
// swapping base_url hands the Token to the new address.
func TestPrepareConfigUpdateStillGuardsBlankDestinationChanges(t *testing.T) {
	// Direction one: from "empty" (official address) to a self-hosted address.
	official := map[string]any{"bot_token": "123:ABC", "chat_id": "-100"}
	if _, err := PrepareConfigUpdate(KindTelegram, official, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "https://tg-proxy.attacker.tld",
	}); err == nil {
		t.Fatal("switching from the official to a self-hosted address must require re-entering the Token")
	}

	// Direction two: clearing the self-hosted address (= back to the official API) is a destination change too.
	proxied := map[string]any{"bot_token": "123:ABC", "base_url": "https://proxy.internal/bot"}
	if _, err := PrepareConfigUpdate(KindTelegram, proxied, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "",
	}); err == nil {
		t.Fatal("clearing the self-hosted address (back to the official API) is a destination change and must require re-entering the Token")
	}
}

func TestDestinationKeysDeclaredForEveryKind(t *testing.T) {
	// Same as SecretKeys: if a channel forgets to declare its destination keys, PrepareConfigUpdate cannot protect it.
	for kind, ch := range registry {
		if len(ch.DestinationKeys()) == 0 {
			t.Errorf("channel %s declares no destination key, so the address-change credential guard does not cover it", kind)
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("channel %s declares no credential key", kind)
		}
	}
}

func TestSecretKeysDeclaredForEveryKind(t *testing.T) {
	// The compiler already forces every channel to implement SecretKeys; this re-checks that none of them
	// leaves masking blank - a channel returning an empty slice echoes its credentials in clear text to the browser.
	expect := map[string]bool{
		KindDingTalk: true, KindFeishu: true, KindWeCom: true,
		KindWebhook: true, KindTelegram: true, KindEmail: true,
	}
	for kind, ch := range registry {
		if !expect[kind] {
			t.Errorf("channel %s has no masked-value expectation registered in this test", kind)
			continue
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("channel %s declares no credential field, so its config is echoed in clear text", kind)
		}
	}
}

// TestPrepareConfigUpdateRejectsMaskedInContainer covers a hole found in audit: when the masked
// sentinel is smuggled inside a **non-string** structure (e.g. webhook.headers is an object),
// MergeConfig only recognizes "string with the prefix" as masked, so the literal "__masked__"
// gets stored as a real header value - authentication then breaks silently with no error at all.
func TestPrepareConfigUpdateRejectsMaskedInContainer(t *testing.T) {
	stored := map[string]any{
		"url":     "https://legit.example.com/hook",
		"headers": map[string]any{"Authorization": "Bearer REAL"},
	}
	// The masked sentinel smuggled inside an object.
	incoming := map[string]any{
		"headers": map[string]any{"Authorization": MaskedPrefix},
	}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, incoming); err == nil {
		t.Fatal("a masked sentinel smuggled inside a structure must be rejected (otherwise the literal is stored)")
	}
	// A whole object submitted as a real new value is accepted as usual.
	ok := map[string]any{"headers": map[string]any{"Authorization": "Bearer NEW"}}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, ok); err != nil {
		t.Fatalf("a normal new header submission must not be blocked: %v", err)
	}
}
