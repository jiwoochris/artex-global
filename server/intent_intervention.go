package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/yukiteruamano/artex-global/db"
)

const maxWorkerMessageBytes = 64 << 10

// User-facing error responses of the Worker intervention API (injecting a human-written
// message into a paused Worker intent). They are shown verbatim as toasts in the Korean UI,
// so they stay Korean. Worker is an engine role name, so it keeps its Latin spelling (glossary), and request_id is a request field name and stays verbatim.
const (
	errIntentRequestTooLarge   = "The request body is too large"
	errIntentMessageEmpty      = "The message cannot be empty"
	errIntentMessageTooLong    = "A message cannot exceed 4000 characters"
	errIntentBadRequestID      = "request_id may contain only 1–128 characters: letters, digits, -, _, ., and :"
	errIntentTaskDeleting      = "The task is being deleted, so no message can be sent to the Worker"
	errIntentTaskPaused        = "The task is paused. Resume the task first, then send a message to the Worker"
	errIntentTaskQueued        = "Cannot send a message to the Worker for a pending task"
	errIntentTaskTerminal      = "Messages cannot be sent to the Worker for a terminated task"
	errIntentTaskSettling      = "Cannot send a message to the Worker while the task is wrapping up"
	errIntentInheritedReadonly = "Inherited intents are read-only, so no message can be sent to the Worker"
	errIntentNotPaused         = "Messages can only be sent to a paused Worker. Pause it first"
)

func validWorkerMessageRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// sendWorkerMessage continues a paused Worker intent with a human-authored message.
// The message is injected as the next turn's input through the same
// resume-from-transcript path the worker uses on a normal resume (ExecuteWithMessage),
// so this reuses the existing pause/resume machinery rather than a bespoke protocol.
// The run happens in a dedicated goroutine outside the worker pool (runDetachedIntent),
// so the message is picked up immediately even when every pool slot is busy — the same
// way the main-agent chat handler starts its run directly. In-memory only: a process
// restart re-runs the intent from its transcript without the message, which is
// acceptable for this rare interrupt-then-continue action.
func (s *Server) sendWorkerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad intent id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, errIntentRequestTooLarge)
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	requestID := strings.TrimSpace(req.RequestID)
	if message == "" {
		writeErr(w, http.StatusBadRequest, errIntentMessageEmpty)
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, errIntentMessageTooLong)
		return
	}
	if !validWorkerMessageRequestID(requestID) {
		writeErr(w, http.StatusBadRequest, errIntentBadRequestID)
		return
	}

	// Reject non-runnable task lifecycles up front so the caller gets a clear reason
	// instead of a silent no-op. The intent itself must be paused: the UI flow is
	// interrupt (pause) first, then send.
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, errIntentTaskDeleting)
		return
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		writeErr(w, http.StatusConflict, errIntentTaskPaused)
		return
	case lifecycle.Queued:
		writeErr(w, http.StatusConflict, errIntentTaskQueued)
		return
	case isTerminalStatus(lifecycle.Status):
		writeErr(w, http.StatusConflict, errIntentTaskTerminal)
		return
	case s.engine.isSettling(t.ID):
		writeErr(w, http.StatusConflict, errIntentTaskSettling)
		return
	}

	node, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			writeErr(w, http.StatusConflict, errIntentInheritedReadonly)
			return
		}
		writeErr(w, http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		writeErr(w, http.StatusConflict, "node is not an intent")
		return
	}
	if node.State != "paused" {
		writeErr(w, http.StatusConflict, errIntentNotPaused)
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, message)
	if !ok {
		return
	}

	// runDetachedIntent transitions paused->running, emits the user turn and starts a
	// dedicated run. Root the run at s.ctx so a disconnected browser cannot strand it
	// while task pause/delete/shutdown still stop it.
	if err := s.engine.runDetachedIntent(s.ctx, t, iid, requestID, message, agentMessage); err != nil {
		switch {
		case errors.Is(err, db.ErrIntentStateConflict):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         iid,
		"state":      "running",
		"accepted":   true,
		"request_id": requestID,
	})
}
