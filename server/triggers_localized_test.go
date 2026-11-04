package server

import "testing"

// Regression-guard test that keeps the trigger configuration validation error text in triggers.go
// as its exact English source text (assertEnglishMessage).

// TestTriggerErrorConstantsLocalized asserts that all three error constants are their exact
// English source text.
func TestTriggerErrorConstantsLocalized(t *testing.T) {
	cases := []struct {
		name        string
		msg         string
		wantEnglish string
	}{
		{"no_condition", errTriggerNoCondition, "Select at least one trigger condition (scheduled run / vulnerability found / goal achieved / task timeout / tool call / task created)"},
		{"tool_set_empty", errTriggerToolSetEmpty, "For a tool-call trigger, select at least one tool"},
		{"custom_only", errTriggerCustomOnly, "Triggers are supported only for custom agents"},
	}
	for _, c := range cases {
		assertEnglishMessage(t, c.name, c.msg, c.wantEnglish)
	}
}

// TestValidateTriggerLocalized actually calls the pure function validateTrigger to check that a missing
// condition or no tool selection returns the English constant and valid input returns the empty string. This value
// pgCreateTrigger·pgUpdateTrigger expose it to the user as-is through writeErr(400, msg).
func TestValidateTriggerLocalized(t *testing.T) {
	if msg := validateTrigger(&triggerReq{}); msg != errTriggerNoCondition {
		t.Fatalf("missing condition message = %q, want = %q", msg, errTriggerNoCondition)
	}
	if msg := validateTrigger(&triggerReq{OnToolCall: true}); msg != errTriggerToolSetEmpty {
		t.Fatalf("no tool selected message = %q, want = %q", msg, errTriggerToolSetEmpty)
	}
	if msg := validateTrigger(&triggerReq{OnFinding: true}); msg != "" {
		t.Fatalf("valid input message = %q, want = empty string", msg)
	}
	if msg := validateTrigger(&triggerReq{OnToolCall: true, ToolNames: []string{"nmap"}}); msg != "" {
		t.Fatalf("valid tool call message = %q, want = empty string", msg)
	}
}
