package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"

	actool "github.com/Autumn-27/norma/tool"
	"github.com/yukiteruamano/artex-global/agent"
	"github.com/yukiteruamano/artex-global/db"
	"github.com/yukiteruamano/artex-global/traffic"
)

// Localization decision (artex-global): every string in this file is model input and
// none of them is ever returned in an HTTP response to the user. They are written in
// English — the source language — and are not localized further. This file has zero
// writeErr calls, so there is no user-facing path out of it. Three sinks, named by
// symbol rather than line number so later edits can't invalidate the note:
//
//	(1) tool descriptions and schema descriptions: report_finding's evidence_hint_id and
//	    hints.text, bind_finding_traffic's wrTool description and finding_id strParam.
//	    These are tool definitions the LLM reads, i.e. model input.
//	(2) the migration match string: the legacy variable in seedFindingWorkflowTools (the
//	    old traffic_search description) is the comparison value of
//	    UPDATE … WHERE description=$2, which promotes the row to
//	    traffic.TrafficSearchDescription. It is DELIBERATELY LEFT IN CHINESE: it must
//	    match rows written by older versions, so translating it would make the upgrade
//	    stop matching.
//	(3) actool.Errorf tool results: the errors from agentFindingTrafficAccess are wrapped
//	    with actool.Errorf(err.Error()) by both of their only callers —
//	    toolBindFindingTraffic (this file) and get_finding_traffic (finding_traffic.go) —
//	    and returned to the agent; toolBindFindingTraffic's binding-disabled and empty
//	    traffic_refs messages go straight into actool.Errorf too.
func (s *Server) seedFindingWorkflowTools() {
	const hostSearchDescriptionFlag = "finding_workflow_tools_v3_host_search_description"
	if value, _, _ := s.m.pg.GetSetting(hostSearchDescriptionFlag); value != "true" {
		// Only replace the original built-in text. A user-edited description is
		// authoritative and must survive upgrades.
		// Deliberately Chinese: this is a DB match value for rows written by older
		// versions (see (2) above), not display text.
		legacy := "Query the target traffic the agent has already captured (host is required, and it can be filtered further by URL substring or body keyword). body_contains runs a full-text search over the captured request/response headers and bodies, supports any substring and non-ASCII text (at least 3 characters), and can be used to find passwords, keys, errors and internal addresses in responses. It returns only a very light index (id/method/url/status/resp_len) and no response content. By default it returns 3 rows and at most 10 per page; when there are more results, page through with page (starting at page=0); to see the raw request/response of one row use traffic_get(id). Reach for this first when revisiting resources already visited or looking for endpoints, instead of curling the same URL again."
		if _, err := s.m.pg.Exec(`UPDATE tools SET description=$1,updated_at=now() WHERE key='traffic_search' AND system AND description=$2`, traffic.TrafficSearchDescription, legacy); err != nil {
			// Log and leave the flag unset so the next startup retries; do not
			// return, or a transient error here would also skip the reporter
			// migration below — the two are independent.
			log.Printf("[evidence] upgrade traffic_search description: %v", err)
		} else {
			_ = s.m.pg.SetSetting(hostSearchDescriptionFlag, "true")
		}
	}
	const flag = "finding_workflow_tools_v2_reporter"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	for _, key := range []string{"report_finding", "add_hint", "add_task_hint"} {
		row, err := s.m.pg.GetTool(key)
		if err != nil {
			log.Printf("[evidence] load %s: %v", key, err)
			return
		}
		if row == nil || !row.System {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(row.Schema, &schema); err != nil {
			log.Printf("[evidence] invalid schema for %s: %v", key, err)
			return
		}
		if schema == nil {
			log.Printf("[evidence] missing object schema for %s", key)
			return
		}
		props := objectProperty(schema, "properties")
		if key == "report_finding" {
			if _, exists := props["evidence_hint_id"]; !exists {
				props["evidence_hint_id"] = map[string]any{"type": "integer", "description": "Optional: the hint ID in this task that corresponds to this finding; the traffic_refs saved on that hint are bound along with it. Omit when there is no hint"}
			}
		} else {
			if _, exists := props["traffic_refs"]; !exists {
				props["traffic_refs"] = agent.HintTrafficSchema()
			}
			hints := objectProperty(props, "hints")
			if _, exists := hints["type"]; !exists {
				hints["type"] = "array"
			}
			items := objectProperty(hints, "items")
			if _, ok := items["type"]; !ok {
				items["type"] = "object"
			}
			itemProps := objectProperty(items, "properties")
			for name, value := range map[string]any{"text": strParam("hint text"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()} {
				if _, exists := itemProps[name]; !exists {
					itemProps[name] = value
				}
			}
		}
		raw, _ := json.Marshal(schema)
		result, err := s.m.pg.Exec(`UPDATE tools SET schema=$2::jsonb,updated_at=now() WHERE key=$1 AND system AND schema=$3::jsonb`, key, string(raw), string(row.Schema))
		if err != nil {
			log.Printf("[evidence] upgrade %s: %v", key, err)
			return
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return
		} // preserve concurrent user edits
	}
	// Upgrade only the original default binding. Customized lists and enabled
	// flags survive; the one-time flag also preserves future user unbinding.
	readers := `["worker","reporter"]`
	for _, key := range []string{"traffic_search", "traffic_get", "traffic_blob"} {
		if _, err := s.m.pg.Exec(`UPDATE tools SET agents=$2::jsonb WHERE key=$1 AND system AND (agents='["worker"]'::jsonb OR (agents @> '["worker","planner","mainagent","auto","pentest"]'::jsonb AND jsonb_array_length(agents)=5))`, key, readers); err != nil {
			return
		}
	}
	if _, err := s.m.pg.Exec(`UPDATE tools SET agents=$1::jsonb WHERE key='get_finding_traffic' AND system AND agents @> '["auto","reporter"]'::jsonb AND jsonb_array_length(agents)=2`, `["auto","reporter","worker","planner","mainagent","pentest"]`); err != nil {
		return
	}
	// Replace the previous code default only; preserve customized binding lists.
	if _, err := s.m.pg.Exec(`UPDATE tools SET agents='["reporter"]'::jsonb WHERE key='bind_finding_traffic' AND system AND agents @> '["worker","planner","mainagent","auto","pentest"]'::jsonb AND jsonb_array_length(agents)=5`); err != nil {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{"bind_finding_traffic"}); err != nil {
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

func objectProperty(parent map[string]any, key string) map[string]any {
	value, ok := parent[key].(map[string]any)
	if !ok {
		value = map[string]any{}
		parent[key] = value
	}
	return value
}

func (s *Server) agentFindingTrafficAccess(ctx context.Context, id int64, write bool) error {
	if id <= 0 {
		return errors.New("finding_id must be a standalone finding-record ID, not an exploration-node ID")
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("%w: finding_id=%d. Evidence tools use the standalone finding-record ID; read it from the finding_id field of list_task_findings / get_task_node_detail. Do not pass id / finding_node_id", db.ErrFindingNotFound, id)
	}
	if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 {
		task := s.m.ResolveTask(strconv.FormatInt(ri.TaskID, 10))
		if task == nil {
			return errors.New("the task does not exist")
		}
		_, inherited, allowed := findingProvenanceInTask(task, f.TaskID)
		if !allowed {
			return errors.New("this task cannot read that finding")
		}
		if write && inherited {
			return errors.New("traffic evidence of an inherited finding is read-only; edit it in the source task")
		}
	}
	return nil
}

func (s *Server) toolBindFindingTraffic() actool.CoreTool {
	return wrTool("bind_finding_traffic", "Attach verified real HTTP traffic to an already-registered finding. finding_id is the standalone finding-record ID; do not pass the exploration-node ID. All refs in one batch either succeed or fail together, and re-referencing does not overwrite an existing note. Attaching marks an existing report as needing an update; do not re-probe to backfill packets or create the finding again.",
		objSchema(map[string]any{"finding_id": strParam("standalone finding-record ID, read from the finding_id field of list_task_findings / get_task_node_detail"), "traffic_refs": agent.HintTrafficSchema()}, "finding_id", "traffic_refs"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			if !s.m.pg.GetBool(settingAgentTrafficBinding, false) {
				return actool.Errorf("Agent automatic traffic binding is off; enable it in system settings, or bind manually on the page."), nil
			}
			var args struct {
				FindingID json.RawMessage `json:"finding_id"`
				Refs      []db.TrafficRef `json:"traffic_refs"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := parseProfileID(args.FindingID)
			if err := s.agentFindingTrafficAccess(ctx, id, true); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(args.Refs) == 0 {
				return actool.Errorf("Attaching requires at least one verified traffic_refs entry; with no traffic there is no need to call this tool"), nil
			}
			list, err := s.evidenceStore().Bind(ctx, id, args.Refs)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(trafficSummary(list))
		})
}
