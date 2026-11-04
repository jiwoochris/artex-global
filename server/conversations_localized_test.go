package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard for the conversation (chat) API error responses: English is the
// source language, so every user-facing message must equal its English source text
// (assertEnglishMessage from intercept_archive_localized_test.go).

// TestConversationErrorConstantsLocalized pins that all 12 response constants are the
// English source text. The handlers pass through s.pg(w) (the database) first and cannot
// complete on a host without one, so the constants themselves are asserted (as auth.go
// does first). Format strings holding %d are filled with real arguments so the final
// wording is checked.
func TestConversationErrorConstantsLocalized(t *testing.T) {
	cases := []struct {
		name        string
		msg         string
		wantEnglish string
	}{
		{"request_too_large", convErrRequestTooLarge, "The request body is too large"},
		{"agent_key_empty", convErrAgentKeyEmpty, "agent_key cannot be empty"},
		{"agent_key_too_long", fmt.Sprintf(convErrAgentKeyTooLong, maxConversationAgentKeyRunes),
			fmt.Sprintf("agent_key can be at most %d characters", maxConversationAgentKeyRunes)},
		{"agent_not_found", convErrAgentNotFound, "Agent not found"},
		{"llm_profile", convErrLLMProfile, "The specified LLM configuration does not exist or has no API Key configured"},
		{"title_too_long", fmt.Sprintf(convErrTitleTooLong, maxConversationTitleRunes),
			fmt.Sprintf("The title can be at most %d characters", maxConversationTitleRunes)},
		{"title_or_pinned", convErrTitleOrPinned, "You must provide either title or pinned"},
		{"title_empty", convErrTitleEmpty, "The title cannot be empty"},
		{"bad_conv_id", convErrBadConvID, "The conversation id is invalid"},
		{"ids_count", fmt.Sprintf(convErrIDsCount, maxConversationDeleteBatch),
			fmt.Sprintf("The number of ids must be between 1 and %d", maxConversationDeleteBatch)},
		{"message_empty", convErrMessageEmpty, "The message cannot be empty"},
		{"busy", convErrBusy, "This conversation is still processing the previous message. Please wait a moment"},
	}
	for _, c := range cases {
		assertEnglishMessage(t, c.name, c.msg, c.wantEnglish)
	}
}

// TestDecodeConversationRequestTooLargeLocalized drives the oversized-body path all the
// way to the HTTP response body. decodeConversationRequest does not need a Server
// (database), so it runs without a DB, and this also proves the constant really lands in
// the response (body too large -> 413 plus the English message).
func TestDecodeConversationRequestTooLargeLocalized(t *testing.T) {
	// Valid JSON that exceeds the 64KB limit; MaxBytesReader reports the overflow while reading.
	body := `{"title":"` + strings.Repeat("a", maxConversationRequestBytes+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(body))
	rec := httptest.NewRecorder()
	var dst struct {
		Title string `json:"title"`
	}
	if decodeConversationRequest(rec, req, &dst) {
		t.Fatal("the body exceeded the limit but decodeConversationRequest returned true")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want = %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse the response JSON: %v (body=%q)", err, rec.Body.String())
	}
	assertEnglishMessage(t, "decode.too_large.response", resp.Error, "The request body is too large")
}

// TestConversationDefaultTitlesLocalized pins that both conversation default titles (F8)
// are their English source text and distinguishable. convDefaultTitle is both the creation default and the
// sentinel for the auto-title branch, so reverting it would show the old wording in the
// conversation list and the delete dialog.
func TestConversationDefaultTitlesLocalized(t *testing.T) {
	assertEnglishMessage(t, "default_title", convDefaultTitle, "New conversation")
	assertEnglishMessage(t, "attachment_title", convAttachmentTitle, "Message with attachment")
	if convDefaultTitle == convAttachmentTitle {
		t.Fatal("the default title and the attachment default title must differ")
	}
}

// TestIsDefaultConversationTitle pins the auto-title branch. The creation default
// (convDefaultTitle) and an empty title qualify for an auto title; a title the user chose
// does not. Because the creation default and the sentinel are the same constant, this also
// catches a regression where they drift apart and no auto title is ever applied.
func TestIsDefaultConversationTitle(t *testing.T) {
	if !isDefaultConversationTitle("") {
		t.Fatal("an empty title must qualify for an auto title")
	}
	if !isDefaultConversationTitle(convDefaultTitle) {
		t.Fatalf("the creation default %q must qualify for an auto title", convDefaultTitle)
	}
	if isDefaultConversationTitle("a title the user wrote") {
		t.Fatal("a user-chosen title must not qualify for an auto title")
	}
}

// TestConversationRetestReasonsLocalized pins that the three revalidation terminal reasons
// (F9) are their English source text. These values are stored in the finding_retests.error column and exposed
// as item.error in the revalidation panel, so the user reads them directly.
func TestConversationRetestReasonsLocalized(t *testing.T) {
	assertEnglishMessage(t, "retest_failed_to_start", convRetestFailedToStart,
		"Failed to start the revalidation")
	assertEnglishMessage(t, "retest_status_read_failed", convRetestStatusReadFailed,
		"Failed to read the revalidation status. Please start it again")
	assertEnglishMessage(t, "retest_stopped_or_closed", convRetestStoppedOrClosed,
		"The revalidation was stopped or the service shut down")
}

// TestTranscriptErrorSummaryLocalized pins the activity-transcript error wrapper (F9). The
// chat turn (no label) and the work main agent (labelled) must both produce an English
// wrapper around the unchanged inner error, with ASCII parentheses and colon only.
func TestTranscriptErrorSummaryLocalized(t *testing.T) {
	cases := []struct {
		name        string
		label       string
		errm        string
		wantEnglish string
	}{
		{"chat_turn", "", "connection reset", "(error: connection reset)"},
		{"main_agent", "main agent", "connection reset", "(main agent error: connection reset)"},
	}
	for _, c := range cases {
		got := transcriptErrorSummary(c.label, c.errm)
		if !strings.Contains(got, c.errm) {
			t.Fatalf("%s: the original error was not preserved: %q", c.name, got)
		}
		// The full-width （）： are the characters the retired format used, so their absence
		// is the assertion here.
		if strings.ContainsAny(got, "（）：") {
			t.Fatalf("%s: full-width punctuation remains: %q", c.name, got)
		}
		// The wrapper around the inner error must be the English text, for the unlabelled
		// chat turn as well as the labelled main agent.
		wrapper := strings.ReplaceAll(got, c.errm, "")
		assertEnglishMessage(t, c.name+".wrapper", wrapper,
			strings.ReplaceAll(c.wantEnglish, c.errm, ""))
		if !strings.Contains(strings.ToLower(wrapper), "error") {
			t.Fatalf("%s: the wrapper must say it is an error: %q", c.name, got)
		}
	}
}
