package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

const maxBatchControlIDs = 100

// User-facing task/intent control error copy. Identifiers, enum values (paused etc.)
// and the %w wrapping stay as-is. The applyTaskControlWithCause messages are mainly
// for the single-task (controlTask) and batch (controlTasksBatch) control responses;
// the orchestrator pause tool (orchestration.go) produces the same text when it
// re-references err.Error().
const (
	errTaskCtrlDeleting      = "Cannot control the task while it is being deleted"
	errTaskCtrlTerminalPause = "A terminated task cannot be paused"
	errTaskCtrlAlreadyPaused = "The task is already paused"
	errTaskCtrlBatchSizeFmt  = "The number of task_ids must be between 1 and %d"

	errIntentCtrlInheritedReadonly = "An inherited intent is read-only and cannot be controlled"
	errIntentCtrlOnlyRunningPause  = "Only a running intent can be paused"
	errIntentCtrlOnlyPausedResume  = "Only paused intents can be resumed"
	errIntentCtrlStateConflictFmt  = "%w: the intent is no longer in the paused state"
	errIntentCtrlOnlyDeletable     = "Only intents in the pending, running, or paused state can be deleted"
	errIntentCtrlReasonRequired    = "Enter a reason for deletion"
)

type taskControlResult struct {
	ID     string `json:"id"`
	Paused bool   `json:"paused"`
	Queued bool   `json:"queued"`
	Status string `json:"status"`
}

type intentControlResult struct {
	ID      int64             `json:"id"`
	State   string            `json:"state"`
	Deleted *db.IntentCleanup `json:"deleted,omitempty"`
}

// parsedTaskID carries one requested batch id together with whether it parsed.
// Invalid ids are kept rather than dropped so the response can name them.
type parsedTaskID struct {
	id    string
	valid bool
}

// normalizeBatchTaskIDs trims, canonicalizes and de-duplicates the ids of one
// batch request while preserving the caller's order. Shared by every batch
// endpoint so they agree on what counts as a duplicate.
func normalizeBatchTaskIDs(raw []string) []parsedTaskID {
	seen := map[string]bool{}
	seenInvalid := map[string]bool{}
	taskIDs := make([]parsedTaskID, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		id, valid := canonicalTaskID(trimmed)
		if !valid {
			if seenInvalid[trimmed] {
				continue
			}
			seenInvalid[trimmed] = true
			taskIDs = append(taskIDs, parsedTaskID{id: trimmed})
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		taskIDs = append(taskIDs, parsedTaskID{id: id, valid: true})
	}
	return taskIDs
}

type batchControlItem struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Status string `json:"status,omitempty"`
	Queued bool   `json:"queued,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) resumeAdmissionMode(t *Task) string {
	if t == nil {
		return "resume"
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.QueueMode == "bootstrap" {
		return "bootstrap"
	}
	if lifecycle.FirstRunAt == 0 {
		goals, err := t.Store.ListByKind(db.KindGoal, 1)
		if err == nil && len(goals) == 0 {
			return "bootstrap"
		}
	}
	return "resume"
}

// applyTaskControl is shared by the single and batch endpoints. Task resume is
// deliberately limited to paused tasks; reruns and finding follow-ups use
// admitTask directly when they need to revive a terminal task.
func (s *Server) applyTaskControl(t *Task, action string) (taskControlResult, error) {
	return s.applyTaskControlWithCause(t, action, agent.AbortPausedByUser)
}

func (s *Server) applyTaskControlWithCause(t *Task, action string, pauseCause error) (taskControlResult, error) {
	if t == nil {
		return taskControlResult{}, fmt.Errorf("task not found")
	}
	out := taskControlResult{ID: t.ID}
	switch action {
	case "pause":
		s.concMu.Lock()
		defer s.concMu.Unlock()
		current, exists := s.m.Task(t.ID)
		if !exists || current != t || s.engine.IsDeleting(t.ID) {
			return out, fmt.Errorf(errTaskCtrlDeleting)
		}
		if !s.engine.beginTaskOperation(t.ID) {
			return out, fmt.Errorf(errTaskCtrlDeleting)
		}
		defer s.engine.decInflight(t.ID)
		lifecycle := t.lifecycleSnapshot()
		if isTerminalStatus(lifecycle.Status) {
			return out, fmt.Errorf(errTaskCtrlTerminalPause)
		}
		if lifecycle.Paused {
			return out, fmt.Errorf(errTaskCtrlAlreadyPaused)
		}
		wasQueued := lifecycle.Queued
		wasEnginePaused := s.engine.IsPaused(t.ID)
		if pauseCause == nil {
			pauseCause = agent.AbortPausedByUser
		}
		s.engine.Pause(t.ID, pauseCause)
		if err := s.m.ApplyTaskPause(t.ID); err != nil {
			if !wasEnginePaused && !wasQueued {
				s.engine.Resume(t)
			}
			return out, err
		}
		// Main Agent is independently cancellable. Only cancel its current turn
		// after the persistent pause commits, so a failed control request is fully
		// compensated and does not lose an otherwise valid conversation turn.
		s.cancelTaskChat(t.ID, agent.AbortChatPausedWithTask)
		out.Paused, out.Status = true, "paused"
		go s.reconcileConcurrency()
	case "resume":
		queued, err := s.admitPausedTask(t)
		if err != nil {
			return out, err
		}
		out.Queued = queued
		out.Status = map[bool]string{true: "queued", false: "running"}[queued]
	default:
		return out, fmt.Errorf("action must be pause|resume")
	}
	log.Printf("[task] #%s %s", t.ID, logT(map[string]string{"pause": "paused", "resume": "resumed"}[action]))
	return out, nil
}

// intentSummaryOf pulls summary out of the intent payload, so the deletion
// notification can keep that value before a hard delete removes the intent node.
func intentSummaryOf(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
	}
	return ""
}

func (s *Server) applyIntentControl(ctx context.Context, t *Task, iid int64, action, reason, mode string) (intentControlResult, error) {
	out := intentControlResult{ID: iid}
	node, err := t.Store.GetNode(iid)
	if err != nil {
		return out, err
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			return out, fmt.Errorf(errIntentCtrlInheritedReadonly)
		}
		return out, fmt.Errorf("intent not found")
	}
	if node.Kind != db.KindIntent {
		return out, fmt.Errorf("node is not an intent")
	}
	switch action {
	case "pause":
		if node.State != "running" {
			return out, fmt.Errorf(errIntentCtrlOnlyRunningPause)
		}
		if err := s.engine.ControlWork(ctx, iid, "pause"); err != nil {
			return out, err
		}
		out.State = "paused"
	case "resume":
		if node.State != "paused" {
			return out, fmt.Errorf(errIntentCtrlOnlyPausedResume)
		}
		changed, err := t.Store.CompareAndSetIntentState(iid, "paused", "open")
		if err != nil {
			return out, err
		}
		if !changed {
			return out, fmt.Errorf(errIntentCtrlStateConflictFmt, db.ErrIntentStateConflict)
		}
		t.Notify()
		out.State = "open"
	case "cancel":
		// Deletion supports two modes:
		//   soft (default, soft delete): stop the intent at state='deleted', record the deletion
		//     reason in the delete_reason field, and preserve the intent node plus every artifact
		//     and lineage link, without adding a separate graph fact.
		//   hard (hard delete): physically delete this intent and the dedicated descendant nodes
		//     that only this intent supports (cascading to the leaves), so no orphaned data is
		//     left behind. Shared nodes, goals and the task root fact are preserved.
		// Both modes notify the planner with cancelled (intent content + deletion reason) so it
		// replans accordingly.
		if node.State != "running" && node.State != "paused" && node.State != "open" {
			return out, fmt.Errorf(errIntentCtrlOnlyDeletable)
		}
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return out, fmt.Errorf(errIntentCtrlReasonRequired)
		}
		if node.State == "running" {
			if err := s.engine.ControlWork(ctx, iid, "cancel"); err != nil {
				return out, err
			}
		}
		summary := intentSummaryOf(node)
		if mode == "hard" {
			cleanup, err := t.Store.CancelIntent(iid)
			if err != nil {
				return out, err
			}
			s.cancelWorkerSide(t.ID, t.ExpID, iid)
			t.NotifyCancelled(iid, summary, reason)
			out.Deleted = &cleanup
			out.State = "" // node is deleted; the frontend removes it from the list when it sees Deleted.
		} else {
			if _, err := t.Store.SoftDeleteIntent(iid, reason); err != nil {
				return out, err
			}
			s.cancelWorkerSide(t.ID, t.ExpID, iid)
			t.NotifyCancelled(iid, summary, reason)
			out.State = db.StateIntentDeleted
		}
	default:
		return out, fmt.Errorf("action must be pause|resume|cancel")
	}
	return out, nil
}

func (s *Server) controlTasksBatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req struct {
		TaskIDs []string `json:"task_ids"`
		Action  string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Action != "pause" && req.Action != "resume" {
		writeErr(w, 400, "action must be pause|resume")
		return
	}
	taskIDs := normalizeBatchTaskIDs(req.TaskIDs)
	if len(taskIDs) == 0 || len(taskIDs) > maxBatchControlIDs {
		writeErr(w, 400, fmt.Sprintf(trMsg(errTaskCtrlBatchSizeFmt), maxBatchControlIDs))
		return
	}
	items := make([]batchControlItem, 0, len(taskIDs))
	for _, parsed := range taskIDs {
		item := batchControlItem{ID: parsed.id}
		if !parsed.valid {
			item.Error = "bad task id"
			items = append(items, item)
			continue
		}
		t, ok := s.m.Task(parsed.id)
		if !ok {
			item.Error = "task not found"
			items = append(items, item)
			continue
		}
		result, err := s.applyTaskControl(t, req.Action)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.OK, item.Status, item.Queued = true, result.Status, result.Queued
		}
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
