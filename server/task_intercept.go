package server

import (
	"errors"
	"net/http"

	"github.com/yukiteruamano/artex/db"
)

// Action validation error of validateTaskInterceptRuleReq. Its only callers are this
// validator and buildTaskInterceptRules, reached from the task-level rule CRUD handlers
// (taskInterceptCreateRule, taskInterceptUpdateRule) and the task/template create and
// update handlers (createTask, pgCreateTaskTemplate, pgUpdateTaskTemplate) — all
// user-facing paths that return HTTP 400 via writeErr and never go through agent tools.
// The action enum values (block, allow) are wire identifiers echoed by clients, so they
// stay verbatim.
const errTaskInterceptInvalidAction = "action must be block or allow"

// CRUD for task-level asset block/allow rules. A rule belongs to a task_id and only
// applies to that task: action=block blocks testing, action=allow whitelists it.
// Enforcement lives in db.EvaluateAssetGate.

type taskInterceptRuleReq struct {
	Enabled bool   `json:"enabled"`
	Action  string `json:"action"` // block | allow
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
	Note    string `json:"note"`
}

// validateTaskInterceptRuleReq normalizes and validates; it reuses the global rule's kind/pattern validator.
func validateTaskInterceptRuleReq(req *taskInterceptRuleReq) error {
	if req.Action == "" {
		req.Action = "block"
	}
	if req.Action != "block" && req.Action != "allow" {
		return errors.New(errTaskInterceptInvalidAction)
	}
	v := assetInterceptRuleReq{Enabled: req.Enabled, Kind: req.Kind, Pattern: req.Pattern, Note: req.Note}
	if err := validateAssetInterceptRuleReq(&v); err != nil {
		return err
	}
	req.Pattern = v.Pattern // already trimmed
	return nil
}

// buildTaskInterceptRules validates the task-level rules supplied at task creation and converts them to db inputs.
func buildTaskInterceptRules(reqs []taskInterceptRuleReq) ([]db.TaskInterceptRuleInput, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	out := make([]db.TaskInterceptRuleInput, 0, len(reqs))
	for i := range reqs {
		rq := reqs[i]
		if err := validateTaskInterceptRuleReq(&rq); err != nil {
			return nil, err
		}
		out = append(out, db.TaskInterceptRuleInput{
			Enabled: rq.Enabled,
			Action:  rq.Action,
			Kind:    rq.Kind,
			Pattern: rq.Pattern,
			Note:    rq.Note,
		})
	}
	return out, nil
}

func (s *Server) taskInterceptListRules(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	rules, err := pg.Assets().ListTaskInterceptRules(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rules == nil {
		rules = []db.AssetInterceptRule{}
	}
	writeJSON(w, 200, map[string]any{"rules": rules})
}

func (s *Server) taskInterceptCreateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	var req taskInterceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateTaskInterceptRuleReq(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.Assets().CreateTaskInterceptRule(taskID, req.Action, req.Kind, req.Pattern, req.Note, req.Enabled)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) taskInterceptUpdateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	var req taskInterceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateTaskInterceptRuleReq(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.Assets().UpdateTaskInterceptRule(taskID, ruleID, req.Action, req.Kind, req.Pattern, req.Note, req.Enabled)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) taskInterceptDeleteRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	deleted, err := pg.Assets().DeleteTaskInterceptRule(taskID, ruleID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

func (s *Server) taskInterceptToggleRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.Assets().ToggleTaskInterceptRule(taskID, ruleID, req.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": req.Enabled})
}
