package notify

import (
	"errors"
	"strings"
	"testing"
)

// F4 (6): pins the user-visible wording of notify/mask.go and filter.go — the validation
// errors that server/notify_api.go returns verbatim through writeErr(400) when a notification
// channel config is saved or updated — as English with no Han ideographs or Hangul.
// channel.go has no user-visible wording (comments only), so it is out of scope.
// hasHan / hasHangul / assertNoUnsupportedScript are defined in notify_localized_test.go.

// TestFilterValidateLocalized checks the validation error for a bad min_severity. That error
// is exposed as a 400 by the save/update paths (server/notify_api.go:264, 369).
func TestFilterValidateLocalized(t *testing.T) {
	err := Filter{MinSeverity: "hgih"}.Validate()
	if err == nil {
		t.Fatal("a bad minimum severity produced no error")
	}
	assertNoUnsupportedScript(t, "Filter.Validate", err.Error())
	// low/medium/high/critical are config enum values and must survive verbatim.
	for _, tok := range []string{"low", "medium", "high", "critical"} {
		if !strings.Contains(err.Error(), tok) {
			t.Errorf("severity token %q disappeared: %q", tok, err.Error())
		}
	}
	// Valid values and an empty value must pass.
	if err := (Filter{MinSeverity: "high"}).Validate(); err != nil {
		t.Errorf("high is valid but raised an error: %v", err)
	}
	if err := (Filter{}).Validate(); err != nil {
		t.Errorf("an empty minimum severity is valid but raised an error: %v", err)
	}
}

// TestPrepareConfigUpdateUnknownKindLocalized checks the error for updating a config with an
// unregistered channel kind (400 from server/notify_api.go:326).
func TestPrepareConfigUpdateUnknownKindLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("definitely-not-a-channel", map[string]any{}, map[string]any{})
	if err == nil {
		t.Fatal("an unregistered channel kind produced no error")
	}
	assertNoUnsupportedScript(t, "PrepareConfigUpdate unknown kind", err.Error())
}

// TestDestinationChangedErrorLocalized triggers ErrDestinationChangedWithoutCredentials through
// the real path: a PATCH that changes only the webhook destination (url) and leaves the
// credentials (headers) untouched. The server returns it as a 400.
func TestDestinationChangedErrorLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("webhook",
		map[string]any{
			"url":     "https://old.example.com/hook",
			"headers": map[string]any{"Authorization": "Bearer real-token"},
		},
		map[string]any{"url": "https://attacker.example.com/hook"},
	)
	if err == nil {
		t.Fatal("changing the destination without touching the credentials produced no error")
	}
	var de *ErrDestinationChangedWithoutCredentials
	if !errors.As(err, &de) {
		t.Fatalf("unexpected error type: %T", err)
	}
	assertNoUnsupportedScript(t, "ErrDestinationChangedWithoutCredentials", err.Error())
	// The changed destination key and the missing credential key are config field names, so they
	// must appear in the message verbatim.
	if !strings.Contains(err.Error(), "url") || !strings.Contains(err.Error(), "headers") {
		t.Errorf("field key names are missing: %q", err.Error())
	}
}

// TestRejectMaskedInContainersLocalized triggers the rejection of a masking sentinel nested in a
// structure through the real path: a PATCH that stuffs a masked sentinel value into headers (an
// object and a credential field).
func TestRejectMaskedInContainersLocalized(t *testing.T) {
	_, err := PrepareConfigUpdate("webhook",
		map[string]any{},
		map[string]any{"headers": map[string]any{"Authorization": MaskedPrefix + ":…abc123"}},
	)
	if err == nil {
		t.Fatal("nesting a masking sentinel inside a structure produced no error")
	}
	assertNoUnsupportedScript(t, "rejectMaskedInContainers", err.Error())
	// The sentinel literal (__masked__) is shown in the message so an operator can tell which
	// value is at fault.
	if !strings.Contains(err.Error(), MaskedPrefix) {
		t.Errorf("the masking sentinel is missing from the message: %q", err.Error())
	}
}
