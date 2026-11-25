package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// F3b (update.go): regression test guarding that the user-facing messages of the self-update endpoint are Korean.
// The frontend (the update-card in system/settings) renders progress.message, reason and the writeErr body verbatim,
// so if these messages revert to Chinese, Chinese toasts and progress messages reappear on the update screen.
// The assertKoreanError and decodeErrorField helpers are reused from the earlier F3b test files (same package server).

func TestUpdateMessageConstantsLocalized(t *testing.T) {
	assertKoreanError(t, "updateMsgPreparing", updateMsgPreparing)
	assertKoreanError(t, "updateMsgFailed", updateMsgFailed)
	assertKoreanError(t, "updateMsgStaged", updateMsgStaged)
	assertKoreanError(t, "updateErrInProgress", updateErrInProgress)
	assertKoreanError(t, "updateErrRollbackInProgress", updateErrRollbackInProgress)

	// Format string constants are checked after filling in their placeholders (%q/%s substituted, zero Han characters).
	notRelease := fmt.Sprintf(updateErrNotReleaseFmt, "v0.0.0-dev")
	assertKoreanError(t, "updateErrNotReleaseFmt", notRelease)
	if !strings.Contains(notRelease, "v0.0.0-dev") {
		t.Fatalf("updateErrNotReleaseFmt: 버전 플레이스홀더가 치환되지 않았습니다: %q", notRelease)
	}
	latest := fmt.Sprintf(updateErrAlreadyLatestFmt, "v1.2.3")
	assertKoreanError(t, "updateErrAlreadyLatestFmt", latest)
	if !strings.Contains(latest, "v1.2.3") {
		t.Fatalf("updateErrAlreadyLatestFmt: 버전 플레이스홀더가 치환되지 않았습니다: %q", latest)
	}
}

// Verifies through the real code path that the progress messages begin/finish emit over SSE are set to the Korean constants.
// Uses a local instance to avoid polluting the global updHub (no DB or network needed).
func TestUpdateProgressMessagesLocalized(t *testing.T) {
	h := &updateHub{subs: map[chan updateProgress]struct{}{}}

	if !h.begin("v1.2.3") {
		t.Fatal("begin 은 최초 호출에서 true 여야 합니다")
	}
	cur, running := h.snapshot()
	if !running {
		t.Fatal("begin 후 running 이어야 합니다")
	}
	if cur.Message != updateMsgPreparing {
		t.Fatalf("준비 중 메시지 불일치: %q", cur.Message)
	}
	if h.begin("v1.2.4") {
		t.Fatal("진행 중이면 begin 은 false 여야 합니다")
	}

	h.finish(errors.New("다운로드 실패"))
	cur, running = h.snapshot()
	if running {
		t.Fatal("finish 후 running 이 해제돼야 합니다")
	}
	if cur.Message != updateMsgFailed {
		t.Fatalf("실패 메시지 불일치: %q", cur.Message)
	}

	h.finish(nil)
	cur, _ = h.snapshot()
	if cur.Message != updateMsgStaged {
		t.Fatalf("준비 완료 메시지 불일치: %q", cur.Message)
	}
}

// Verifies over real HTTP, without a DB, that updateRollback's "in progress, cannot roll back" 409 response is Korean.
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
		t.Fatalf("상태 코드 409 기대, 실제 %d (본문 %s)", rec.Code, rec.Body.Bytes())
	}
	msg := decodeErrorField(t, rec.Body.Bytes())
	assertKoreanError(t, "updateRollback 진행 중 409", msg)
	if msg != updateErrRollbackInProgress {
		t.Fatalf("롤백 거부 문구 불일치: %q", msg)
	}
}
