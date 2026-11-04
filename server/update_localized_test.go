package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// F3b(update.go): regression test guarding that the self-update endpoint's user-facing messages are
// their exact English source text. The frontend (the update-card in system/settings) renders
// progress.message, reason and the writeErr body verbatim, so if these messages change language the
// update screen shows the wrong language again. The assertEnglishMessage and decodeErrorField helpers
// are reused from the earlier F3b test files (same package server).

func TestUpdateMessageConstantsLocalized(t *testing.T) {
	assertEnglishMessage(t, "updateMsgPreparing", updateMsgPreparing, "Preparing…")
	assertEnglishMessage(t, "updateMsgFailed", updateMsgFailed, "Update failed")
	assertEnglishMessage(t, "updateMsgStaged", updateMsgStaged, "The new version is ready. Restarting…")
	assertEnglishMessage(t, "updateErrInProgress", updateErrInProgress, "An update is already in progress")
	assertEnglishMessage(t, "updateErrRollbackInProgress", updateErrRollbackInProgress,
		"Cannot roll back because an update is in progress")

	// Format-string constants are checked after filling in the placeholders (%q/%s substituted).
	notRelease := fmt.Sprintf(updateErrNotReleaseFmt, "v0.0.0-dev")
	assertEnglishMessage(t, "updateErrNotReleaseFmt", notRelease,
		`The current version "v0.0.0-dev" is not an official release version, so one-click update is not available`)
	if !strings.Contains(notRelease, "v0.0.0-dev") {
		t.Fatalf("updateErrNotReleaseFmt: the version placeholder was not substituted: %q", notRelease)
	}
	latest := fmt.Sprintf(updateErrAlreadyLatestFmt, "v1.2.3")
	assertEnglishMessage(t, "updateErrAlreadyLatestFmt", latest, "Already on the latest version (v1.2.3)")
	if !strings.Contains(latest, "v1.2.3") {
		t.Fatalf("updateErrAlreadyLatestFmt: the version placeholder was not substituted: %q", latest)
	}
}

// Verifies through the real code path that the progress messages begin/finish emit over SSE are set from the English constants.
// Uses a local instance to avoid polluting the global updHub (no DB or network needed).
func TestUpdateProgressMessagesLocalized(t *testing.T) {
	h := &updateHub{subs: map[chan updateProgress]struct{}{}}

	if !h.begin("v1.2.3") {
		t.Fatal("begin must return true on the first call")
	}
	cur, running := h.snapshot()
	if !running {
		t.Fatal("running must be set after begin")
	}
	if cur.Message != updateMsgPreparing {
		t.Fatalf("preparing message mismatch: %q", cur.Message)
	}
	if h.begin("v1.2.4") {
		t.Fatal("begin must return false while an update is in progress")
	}

	h.finish(errors.New("download failed"))
	cur, running = h.snapshot()
	if running {
		t.Fatal("running must be cleared after finish")
	}
	if cur.Message != updateMsgFailed {
		t.Fatalf("failure message mismatch: %q", cur.Message)
	}

	h.finish(nil)
	cur, _ = h.snapshot()
	if cur.Message != updateMsgStaged {
		t.Fatalf("ready message mismatch: %q", cur.Message)
	}
}

// Verifies over real HTTP, without a DB, that updateRollback's 409 "cannot roll back while an update is in progress" response is the English source text.
func TestUpdateRollbackInProgressLocalized(t *testing.T) {
	updHub.mu.Lock()
	prev := updHub.running
	updHub.running = true
	updHub.mu.Unlock()
	defer func() {
		updHub.mu.Lock()
		updHub.running = prev
		updHub.mu.Unlock()
	}()

	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/update/rollback", nil)
	rec := httptest.NewRecorder()
	s.updateRollback(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("want status code 409, got %d (body %s)", rec.Code, rec.Body.Bytes())
	}
	msg := decodeErrorField(t, rec.Body.Bytes())
	assertEnglishMessage(t, "updateRollback 409 while in progress", msg,
		"Cannot roll back because an update is in progress")
}
