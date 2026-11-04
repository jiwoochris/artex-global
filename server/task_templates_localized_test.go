package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yukiteruamano/artex-global/db"
)

// TestTaskTemplateErrorConstantsLocalized guards the F3b task-template bundle:
// every user-facing string the task-template API returns is its exact English
// source text. The format constant is checked after formatting so the %s/%d verbs
// resolve to a concrete message.
func TestTaskTemplateErrorConstantsLocalized(t *testing.T) {
	assertEnglishMessage(t, "request-too-large", errTaskTemplateRequestTooLarge,
		"The request body is too large")
	assertEnglishMessage(t, "name-conflict", errTaskTemplateNameConflict,
		"This template name already exists")
	assertEnglishMessage(t, "rule-invalid", errTaskTemplateRuleInvalid,
		"The interception/allow rule is invalid: ")
	assertEnglishMessage(t, "no-fields", errTaskTemplateNoFields,
		"You must provide at least one of name, description, goal, category_id, or intercept_rules")
	assertEnglishMessage(t, "field-too-long",
		fmt.Sprintf(errTaskTemplateFieldTooLongFmt, "name", db.MaxTaskTemplateNameRunes),
		fmt.Sprintf("name can be at most %d characters", db.MaxTaskTemplateNameRunes))
}

// TestTaskTemplateResponsesLocalized drives the real response paths that do not
// touch the database: the body decoder rejects an oversized request, the field
// validator rejects an over-length name, and writeTaskTemplateErr maps the
// name-conflict sentinel to the English 409 body.
func TestTaskTemplateResponsesLocalized(t *testing.T) {
	// decodeTaskTemplateRequest — MaxBytesReader errors on an oversized body
	// before any DB access, so the 413 message is produced on its own.
	body := `{"name":"` + strings.Repeat("a", maxTaskTemplateRequestBytes+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/task-templates", strings.NewReader(body))
	rec := httptest.NewRecorder()
	var decoded taskTemplateRequest
	if _, ok := decodeTaskTemplateRequest(rec, req, &decoded); ok {
		t.Fatalf("too-large: decoding must not pass (status=%d)", rec.Code)
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too-large status=%d, want 413", rec.Code)
	}
	assertEnglishMessage(t, "too-large", decodeErrorField(t, rec.Body.Bytes()),
		"The request body is too large")

	// validateTaskTemplateRequest — a name past the rune limit returns the
	// English field-too-long error (surfaced via writeErr in the handlers).
	longName := strings.Repeat("a", db.MaxTaskTemplateNameRunes+1)
	err := validateTaskTemplateRequest(taskTemplateRequest{Name: &longName})
	if err == nil {
		t.Fatal("name-too-long: validation must not pass")
	}
	assertEnglishMessage(t, "name-too-long", err.Error(),
		fmt.Sprintf("name can be at most %d characters", db.MaxTaskTemplateNameRunes))

	// writeTaskTemplateErr — the name-conflict sentinel maps to the English 409.
	rec = httptest.NewRecorder()
	writeTaskTemplateErr(rec, db.ErrTaskTemplateNameConflict)
	if rec.Code != http.StatusConflict {
		t.Fatalf("name-conflict status=%d, want 409", rec.Code)
	}
	assertEnglishMessage(t, "name-conflict-response", decodeErrorField(t, rec.Body.Bytes()),
		"This template name already exists")
}
