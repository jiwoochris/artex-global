package agent

import (
	"encoding/json"
	"fmt"

	actool "github.com/Autumn-27/norma/tool"
	"github.com/yukiteruamano/artex/db"
)

const findingIDGuidance = "\n\n**Finding id convention**: finding_id is the standalone vulnerability record id; finding_node_id is the exploration node id. The id returned by list_findings / list_task_findings / node_detail / get_task_node_detail remains the exploration node id; read the standalone id from the finding_id in the same response. get_finding_traffic / bind_finding_traffic use the standalone finding_id. The legacy update_finding_report finding_id argument still takes finding_node_id. Do not use the number on report_finding's first line for evidence tools, and do not guess other numbers after an id error."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default the reporter agent verifies and binds traffic before writing the report. The reporter keeps the verification commands, key output, existing real traffic ids and their purpose in evidence so the reporter agent can check them against the execution records; no extra traffic lookup is needed for binding. Explicit immediate binding is still supported: traffic_refs or evidence_hint_id may submit already-verified references, the latter reading the structured references of the hint named in this task; if either is invalid the whole report fails. TCP/no-packet cases do not need these optional arguments. The returned finding_id and finding_node_id mean the standalone record and the exploration node respectively."
		case "add_hint", "add_task_hint":
			note = "\nWhen handing over a confirmed vulnerability, keep the ids, purpose, description and order of the verified traffic in the corresponding hint's traffic_refs (top level for a single entry, in the matching hints element for a batch), and state in text which specific vulnerability it proves. The caller must not hand over text only and drop existing traffic references. Unverified candidates must not be passed as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Traffic evidence hand-off (optional)**: automatic binding is by default done by the reporter agent after the vulnerability is recorded and before the report is written. The reporter should keep the verification commands, key output, existing real traffic ids and their purpose in evidence, with intent_id in the task, so the reporter agent can trace them; there is no need to look up extra traffic for binding. When Auto / the Planner reports on someone's behalf, do not drop references the executor already made. add_hint / add_task_hint may hand over traffic via traffic_refs; explicit immediate binding is still supported through report_finding's traffic_refs / evidence_hint_id. Register TCP or no-packet cases normally, never guess ids, and never re-probe merely to complete the bundle."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nWhen the platform chat has no task context, do not call report_finding directly; hand over to the matching existing task with add_task_hint so the task agent records it, and check the result with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore judging a goal complete, finish reporting/handing over the evidence you already have. Do not end the task and cancel the Worker merely because the written vulnerabilities are recorded while the evidence hand-off is still pending; a no-packet case requires neither waiting nor forcing a capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Automatic traffic association before the report (enabled)**: you are responsible for verifying and binding the traffic for the vulnerability that triggered this run, then writing the report. First get the explicit finding_id and finding_node_id from the report_finding return JSON or from get_task_node_detail / list_task_findings. Read the vulnerability detail, the execution records of the corresponding intent and the existing evidence list, and prefer the real ids the reporter handed over. If this round's verification was HTTP and the traffic tools are available, filter candidates with traffic_search, then verify request by request with traffic_get that they really support the vulnerability; domain and time are only filters and do not prove ownership. Associate the confirmed evidence in reproduction order with bind_finding_traffic(finding_id, traffic_refs), choosing baseline / proof / verification / supporting and stating the purpose. Act only on this vulnerability; do not create duplicate vulnerabilities or re-probe the target. After a successful bind, call get_finding_traffic again for the latest version, read the bodies you need, then pass the version you actually read as evidence_version to update_finding_report (its finding_id argument still takes finding_node_id). Existing bindings need not be appended again. For TCP, uncaptured, unavailable-tool or no-exact-match cases, skip automatic binding, write the report normally from the textual/command evidence and explain why, and never guess to make the traffic look complete. Do not claim a failed bind succeeded; keep the existing evidence and explain in the report why nothing was bound."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: already-verified traffic references that correspond to the specific vulnerability in this hint, in order; after the hand-off report_finding can pass evidence_hint_id to carry these references.", "items": obj(map[string]any{"traffic_id": str("real traffic id"), "role": str("baseline / proof / verification / supporting"), "note": str("what conclusion this traffic supports")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must be a hint node of this task (inherited hints cannot be used for binding directly)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
