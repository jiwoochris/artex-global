package intercept

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yukiteruamano/artex-global/config"
)

// wantEnglish asserts that a user-visible literal is exactly its English source text.
func wantEnglish(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: want %q, got %q", label, want, got)
	}
}

// TestInterceptMessagesLocalized pins the user-facing judge/approval messages to their exact
// English source text. These surface in the approval record reason, the activity stream, and
// the 409 response for an already-decided request.
func TestInterceptMessagesLocalized(t *testing.T) {
	for _, c := range []struct{ name, got, want string }{
		{"msgReviewContextIncomplete", msgReviewContextIncomplete, "The review context is incomplete, so human confirmation is required: "},
		{"msgModelApprovalFailed", msgModelApprovalFailed, "Model approval failed; handled according to the failure policy: "},
		{"msgModelOutputUnparsable", msgModelOutputUnparsable, "The model output could not be parsed; handled according to the failure policy"},
		{"reasonWorkCanceled", reasonWorkCanceled, "The task was cancelled"},
		{"reasonWorkCanceledPreExec", reasonWorkCanceledPreExec, "The task was cancelled before execution"},
		{"reasonApprovalTimeout", reasonApprovalTimeout, "Approval timed out; handled according to the timeout policy"},
		{"reasonManualDeny", reasonManualDeny, "A human denied the execution"},
		{"reasonManualAllow", reasonManualAllow, "A human allowed the execution"},
		{"ErrAlreadyDecided", ErrAlreadyDecided.Error(), "This approval has already been handled or does not exist. Refresh the records and try again"},
	} {
		wantEnglish(t, c.name, c.got, c.want)
	}
}

// TestJudgeActionLabelLocalized checks the three judge verdict labels: the English source
// labels for the default language and the Spanish overlay for es. An unknown action still
// passes through untranslated.
func TestJudgeActionLabelLocalized(t *testing.T) {
	t.Cleanup(func() { config.CurrentLanguage = nil })
	config.CurrentLanguage = func() string { return "en" }
	for action, want := range map[string]string{"allow": "allow", "deny": "deny", "ask": "ask"} {
		wantEnglish(t, "judgeActionLabel("+action+")/en", judgeActionLabel(action), want)
	}
	config.CurrentLanguage = func() string { return "es" }
	for action, want := range map[string]string{"allow": "permitir", "deny": "bloquear", "ask": "preguntar"} {
		wantEnglish(t, "judgeActionLabel("+action+")/es", judgeActionLabel(action), want)
	}
	if got := judgeActionLabel("weird"); got != "weird" {
		t.Errorf("judgeActionLabel(weird) = %q, want passthrough", got)
	}
}

// TestDefaultMessageLocalized checks the rule-derived deny/ask messages are English and still
// embed the rule name, while allow stays empty.
func TestDefaultMessageLocalized(t *testing.T) {
	wantEnglish(t, "defaultMessage(deny)", defaultMessage("deny", "R1"),
		"This tool cannot be executed under interception rule [R1]")
	wantEnglish(t, "defaultMessage(ask)", defaultMessage("ask", "R1"),
		"This tool requires user approval under interception rule [R1]. Please wait")
	if got := defaultMessage("allow", "R1"); got != "" {
		t.Errorf("defaultMessage(allow) = %q, want empty", got)
	}
}

// TestToolApprovalSummaryLocalized checks the activity summary is English and stays parseable
// by transcript.tsx, which extracts the pending id via /\(#(\d+)\)/ and the tool name via
// /Tool\s+(\S+)\s+approval/.
func TestToolApprovalSummaryLocalized(t *testing.T) {
	s := fmt.Sprintf(msgToolApprovalRequestFmt, "Bash", 42)
	wantEnglish(t, "msgToolApprovalRequestFmt", s, "Tool Bash approval request (#42)")
	if !strings.Contains(s, "(#42)") {
		t.Errorf("summary lost the (#N) marker (transcript.tsx pending_id regex): %q", s)
	}
	if !strings.Contains(s, "Tool Bash approval") {
		t.Errorf("summary lost the 'Tool X approval' shape (transcript.tsx toolName regex): %q", s)
	}
}
