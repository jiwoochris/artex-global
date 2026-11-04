package agent

import (
	"context"

	"github.com/yukiteruamano/artex-global/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**Vulnerability traffic evidence (optional)**: when calling report_finding to report a vulnerability and you have HTTP request/response pairs you have already reviewed and confirmed support the conclusion, you may bind their real ids in reproduction order with traffic_refs; domain and time only filter candidates and do not imply association. For non-HTTP vulnerabilities such as TCP, when nothing was captured or no exact match exists, omit them or pass [] and keep other verifiable evidence (command output, logs, etc.) in evidence, preferably stating why nothing was bound. Do not guess ids, and do not re-probe merely to complete the bundle."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
