package sidequestion

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
)

// wantEnglish asserts that a user-visible literal is exactly its English source text.
func wantEnglish(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: want %q, got %q", label, want, got)
	}
}

// hasUnsupportedScript reports leftover Han (Chinese) or Hangul (Korean) characters. Only
// English and Spanish are supported, so either one means a stale translation came back.
func hasUnsupportedScript(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// TestSideQuestionOutputsLocalized guards F21: every user-facing side-question
// (side question) error and answer-text literal produced by this package must be its exact
// English source text. These surface through /api/.../side-questions into the chat
// side-question panel, so any other wording is a user-visible regression and must fail the build.
func TestSideQuestionOutputsLocalized(t *testing.T) {
	wantEnglish(t, "ErrContextBudget", ErrContextBudget.Error(),
		"The side-question context still exceeds the model budget after compaction. Narrow the question or adjust the model context settings.")
	wantEnglish(t, "errSideModelInterrupted", errSideModelInterrupted.Error(),
		"The model response was interrupted. Please ask again.")
	wantEnglish(t, "errSideNoAnswer", errSideNoAnswer.Error(),
		"The model returned no answer.")
	wantEnglish(t, "msgSideToolUnavailable", msgSideToolUnavailable,
		"Tools cannot be run in a side question. Send the work request in the main conversation.")
	wantEnglish(t, "errSideSummaryCallCap", errSideSummaryCallCap.Error(),
		"Side-question context pruning reached its per-request limit. Narrow the question and try again.")
	wantEnglish(t, "sideSummaryFailedPrefix", sideSummaryFailedPrefix,
		"The side-question summary failed")
	wantEnglish(t, "errSideSummaryIncomplete", errSideSummaryIncomplete.Error(),
		"The side-question summary was not fully generated. Please try again.")
	wantEnglish(t, "errSideSummaryOverBudget", errSideSummaryOverBudget.Error(),
		"The side-question summary did not shrink within budget. Please try again.")
	wantEnglish(t, "errSideHistoryCursor", errSideHistoryCursor.Error(),
		"The side-question history cursor is invalid.")
	wantEnglish(t, "errSideCompactionStalled", errSideCompactionStalled.Error(),
		"Side-question compaction could not shrink the context any further, so the retry was stopped.")
}

// TestSideQuestionBudgetSentinelPreserved: localizing the message must not break
// callers that branch on the ErrContextBudget sentinel with errors.Is (see
// context_test.go, which relies on it after the recovery path gives up).
func TestSideQuestionBudgetSentinelPreserved(t *testing.T) {
	if !errors.Is(fmt.Errorf("prepare: %w", ErrContextBudget), ErrContextBudget) {
		t.Fatal("ErrContextBudget sentinel identification (errors.Is) is broken")
	}
}

// TestSideQuestionBrainPreserved: the agent-brain prompts (the answering instruction and the
// summarizer instruction) are English by decision. They are model input, not UI text, so pinning
// their opening sentence keeps a silent rewrite — or a revert to another language — from drifting
// the benchmark unnoticed.
func TestSideQuestionBrainPreserved(t *testing.T) {
	for _, c := range []struct{ label, text, opensWith string }{
		{"instruction", instruction, "This is a standalone side question."},
		{"summaryInstruction", summaryInstruction, "You only produce a short summary for the standalone side question chat:"},
	} {
		if !strings.HasPrefix(c.text, c.opensWith) {
			t.Errorf("%s: the prompt no longer opens with %q: %q", c.label, c.opensWith, c.text)
		}
		if hasUnsupportedScript(c.text) {
			t.Errorf("%s: the prompt is no longer English (Hangul or Han characters remain): %q", c.label, c.text)
		}
	}
}
