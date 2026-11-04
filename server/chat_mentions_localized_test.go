package server

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/config"
)

// TestChatMentionErrorsLocalized guards F3b chat_mentions.go: every user-facing
// @mention (citation) error response must be the exact English source message
// (and the Spanish overlay when es is active). The wire token labels
// (chatMentionPattern·chatMentionKinds), the agent-input snapshot header, and the
// truncation markers fed into that snapshot stay in the original language by
// design and are intentionally not checked here.
func TestChatMentionErrorsLocalized(t *testing.T) {
	// Fixed user-facing literals.
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"badID", errChatMentionBadID, "The citation ID is invalid. Please select again"},
		{"tooMany", errChatMentionTooMany, "A single message can cite up to 10 records"},
		{"badSearch", errChatMentionBadSearch, "The citation type is invalid or the search term exceeds 200 characters"},
		{"dataUnavail", errChatMentionDataUnavail, "Citation data is currently unavailable"},
		{"tooLarge", errChatMentionTooLarge, "The cited content is too large. Reduce the citation records and try again"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}

	// The 漏洞/字 below are wire-token and length-limit fixtures, not display text:
	// they must keep matching the Chinese mention tokens the frontend still sends.
	// parseChatMentions — a non-positive id inside a valid wire token.
	if _, err := parseChatMentions("@[漏洞#0]"); err == nil {
		t.Fatal("an invalid citation ID must not pass")
	} else {
		assertEnglishMessage(t, "parse.badID", err.Error(), "The citation ID is invalid. Please select again")
	}

	// parseChatMentions — exceeding the per-message mention cap (11 distinct ids).
	var b strings.Builder
	for i := 1; i <= 11; i++ {
		fmt.Fprintf(&b, "@[漏洞#%d] ", i)
	}
	if _, err := parseChatMentions(b.String()); err == nil {
		t.Fatal("exceeding the citation cap must not pass")
	} else {
		assertEnglishMessage(t, "parse.tooMany", err.Error(), "A single message can cite up to 10 records")
	}

	// composeChatMentionMessage — a mention is present but the database is nil.
	if _, err := composeChatMentionMessage(nil, "@[漏洞#1]"); err == nil {
		t.Fatal("citation resolution must not pass without a DB")
	} else {
		assertEnglishMessage(t, "compose.dataUnavail", err.Error(), "Citation data is currently unavailable")
	}

	// searchChatMentions — invalid kind and over-long query are rejected before
	// the database handle is touched, so &Server{} is enough to reach the branch.
	for _, q := range []string{"kind=unsupported", "q=" + url.QueryEscape(strings.Repeat("字", 201))} {
		w := httptest.NewRecorder()
		(&Server{}).searchChatMentions(w, httptest.NewRequest("GET", "/api/chat/mentions?"+q, nil))
		if w.Code != 400 {
			t.Fatalf("expected validation failure (400) but got %d (%s)", w.Code, q)
		}
		assertEnglishMessage(t, "search."+q, decodeErrorField(t, w.Body.Bytes()),
			"The citation type is invalid or the search term exceeds 200 characters")
	}

	// The display label map mirrors the UI mention kinds in both supported
	// output languages, and the not-found template resolves to a fully English
	// message for a known kind.
	for _, kind := range []string{"finding", "asset", "company", "endpoint", "ip", "app", "root_domain", "subdomain", "service"} {
		for _, lang := range config.SupportedLanguages {
			if chatMentionKindLabel[lang][kind] == "" {
				t.Fatalf("missing %s kind label: %s", lang, kind)
			}
		}
	}
	assertEnglishMessage(t, "notFound",
		fmt.Sprintf(trMsg(errChatMentionNotFoundFmt), chatMentionKindLabel[config.ActiveLanguage()]["finding"], 7),
		"The cited finding #7 record does not exist or its type does not match. Remove it and select again")

	// Spanish overlay: the label and the template switch to es.
	t.Cleanup(func() { config.CurrentLanguage = nil })
	config.CurrentLanguage = func() string { return "es" }
	if got, want := fmt.Sprintf(trMsg(errChatMentionNotFoundFmt), chatMentionKindLabel["es"]["finding"], 7),
		"El registro hallazgo #7 citado no existe o su tipo no coincide. Elimínelo y selecciónelo de nuevo"; got != want {
		t.Fatalf("es notFound = %q, want %q", got, want)
	}
}
