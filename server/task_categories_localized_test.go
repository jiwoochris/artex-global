package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// decodeErrorField pulls the "error" string out of a writeErr JSON body so the
// user-facing message can be compared with its English source text.
func decodeErrorField(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to parse the response JSON: %v (body %s)", err, body)
	}
	return resp.Error
}

// TestTaskCategoryErrorConstantsLocalized pins that every user-facing error
// literal in task_categories.go is its exact English source text. If anyone
// reverts one to another language, this fails.
func TestTaskCategoryErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"errTaskCatRequestTooLarge", errTaskCatRequestTooLarge, "The request body is too large"},
		{"errTaskCatNameEmpty", errTaskCatNameEmpty, "The category name cannot be empty"},
		{"errTaskCatNameTooLong", errTaskCatNameTooLong, "The category name can be at most 80 characters"},
		{"errTaskCatNameConflict", errTaskCatNameConflict, "The category name already exists"},
		{"errTaskCatInvalidID", errTaskCatInvalidID, "The task category id is invalid"},
		{"errTaskCatBatchSizeFmt", fmt.Sprintf(errTaskCatBatchSizeFmt, db.MaxTaskCategoryBatchSize),
			fmt.Sprintf("The number of task_ids must be between 1 and %d", db.MaxTaskCategoryBatchSize)},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestTaskCategoryResponsesLocalized drives the validators that never touch the
// database and confirms the English message actually lands in the HTTP body. The
// request decoder and id parser work on the body alone, writeTaskCategoryError
// maps a db sentinel, and the batch-size guard returns before s.m is read.
func TestTaskCategoryResponsesLocalized(t *testing.T) {
	// decodeTaskCategoryRequest — body-only validation, no DB.
	decodeCase := func(name, body string) string {
		req := httptest.NewRequest(http.MethodPost, "/api/task-categories", strings.NewReader(body))
		rec := httptest.NewRecorder()
		if _, ok := decodeTaskCategoryRequest(rec, req); ok {
			t.Fatalf("%s: validation must not pass (status=%d)", name, rec.Code)
		}
		return decodeErrorField(t, rec.Body.Bytes())
	}
	assertEnglishMessage(t, "name-empty", decodeCase("name-empty", `{"name":"   "}`),
		"The category name cannot be empty")
	assertEnglishMessage(t, "name-too-long",
		decodeCase("name-too-long", `{"name":"`+strings.Repeat("a", db.MaxTaskCategoryNameRunes+1)+`"}`),
		"The category name can be at most 80 characters")
	// The body exceeds maxTaskCategoryRequestBytes, so MaxBytesReader errors mid-decode.
	assertEnglishMessage(t, "too-large",
		decodeCase("too-large", `{"name":"`+strings.Repeat("a", maxTaskCategoryRequestBytes+1)+`"}`),
		"The request body is too large")

	// parseCategoryIDField — a zero/negative category_id is rejected.
	rec := httptest.NewRecorder()
	if _, ok := parseCategoryIDField(rec, json.RawMessage("0")); ok {
		t.Fatal("an invalid id must not pass")
	}
	assertEnglishMessage(t, "invalid-id", decodeErrorField(t, rec.Body.Bytes()), "The task category id is invalid")

	// writeTaskCategoryError — the name-conflict sentinel maps to the English 409.
	rec = httptest.NewRecorder()
	writeTaskCategoryError(rec, db.ErrTaskCategoryNameConflict)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d, want 409", rec.Code)
	}
	assertEnglishMessage(t, "name-conflict", decodeErrorField(t, rec.Body.Bytes()), "The category name already exists")

	// Batch move — an empty selection is rejected before any DB access, so a
	// zero-value Server reaches the guard without dereferencing s.m.
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/category/batch",
		strings.NewReader(`{"task_ids":[],"category_id":null}`))
	rec = httptest.NewRecorder()
	(&Server{}).updateTasksCategoryBatch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty batch status=%d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	assertEnglishMessage(t, "batch-size", decodeErrorField(t, rec.Body.Bytes()),
		fmt.Sprintf("The number of task_ids must be between 1 and %d", db.MaxTaskCategoryBatchSize))
}
