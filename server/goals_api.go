package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// Error response text goals_api.go returns to the user. Command · request field names (text ·
// vulnclass · gid) · enums stay as-is, and only human-readable messages are in Korean. The
// English writeErr messages (task not found · invalid JSON · bad goal id) are outside the
// F3b Chinese-only scope and are preserved.
const (
	errGoalTaskDeletingAdd    = "작업을 삭제하는 중이라 목표를 추가할 수 없습니다"
	errGoalTaskDeletingEdit   = "작업을 삭제하는 중이라 목표를 수정할 수 없습니다"
	errGoalTaskDeletingDelete = "작업을 삭제하는 중이라 목표를 삭제할 수 없습니다"
	errGoalTextEmpty          = "목표 내용은 비워 둘 수 없습니다"
	errGoalNotFound           = "목표를 찾을 수 없습니다"
	errGoalReadAfterAdd       = "목표를 저장한 뒤 읽지 못했습니다"
	errGoalReadAfterEdit      = "목표를 수정한 뒤 읽지 못했습니다"
)

// Manual CRUD endpoints for the Overview "目标管理" (goal management). They write the same goal
// nodes as the agent-side set_goals tool, but the entry point is humans adding/editing/deleting
// directly in the UI; after an add/edit they reuse the "revive task" logic (admitTask resume:
// terminal → running, lift the pause, queue if needed), while a delete does not revive (per
// product decision). Every mutating handler goes through beginTaskOperation/decInflight to
// avoid racing with task deletion (consistent with the intent CRUD).

// listGoals returns all goals of this task (with text/vulnclass/state split out) for rendering the goal management cards.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal manually adds a goal: persist to the DB (attached under the task root's spawns) →
// record a "goal added" trigger that wakes the planner → revive the task, so the planner
// re-judges whether it is met against the new goal.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, errGoalTaskDeletingAdd)
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, errGoalTextEmpty)
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // record a "human added a goal: …" trigger and wake the planner
	s.reviveTask(t)              // pull a completed/paused task back into the running state to keep going
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, errGoalReadAfterAdd)
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal manually edits a goal's text (and vulnclass): update the DB → record a "user changed
// the goal from old to new" trigger that wakes the planner → revive the task, so the planner
// adjusts its direction to the new goal.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, errGoalTaskDeletingEdit)
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, errGoalTextEmpty)
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, errGoalNotFound)
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // record a "human changed the goal from old to new" trigger and wake the planner
	s.reviveTask(t)                   // same as add: revive the task to re-judge against the new goal
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, errGoalReadAfterEdit)
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal manually deletes a goal (hard delete, cascading to edges/anchors): delete from the
// DB → record a "user deleted goal X" trigger that wakes the planner to re-judge the remaining
// goals. Per product decision, a delete does NOT revive the task.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, errGoalTaskDeletingDelete)
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, errGoalNotFound)
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // record a "human deleted the goal: …" trigger and wake the planner (does not revive the task)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
