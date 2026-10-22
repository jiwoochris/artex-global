package agent

import (
	"context"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
)

func attachSideCapture(ctx context.Context, opts *agentcore.Options) context.Context {
	ri := RunInfoFrom(ctx)
	p := sidequestion.Parent{TaskID: ri.TaskID, ExplorationID: ri.ExplorationID, IntentID: ri.IntentID}
	if strings.HasPrefix(ri.SessionID, "conv-") {
		p.ConversationID, _ = strconv.ParseInt(strings.TrimPrefix(ri.SessionID, "conv-"), 10, 64)
	}
	// Ownership is exclusive: a side-question session belongs to EITHER a
	// conversation OR a task, never both (chatstate.SessionRef.Validate states
	// the same rule). A run can carry both ids — a reporter attributed back to
	// its source task keeps TaskID while its session id is the conversation it
	// ran in — and writing both to side_question_sessions violates the table's
	// CHECK constraint, so every checkpoint for such a run was rejected.
	// The conversation wins: it is the session the snapshot actually belongs
	// to, and the task attribution is carried by the run, not by the session.
	if p.ConversationID > 0 {
		p.TaskID, p.ExplorationID, p.IntentID, p.MainSeg = 0, 0, 0, nil
	}
	if p.ConversationID == 0 && (p.TaskID == 0 || p.ExplorationID == 0) {
		return ctx
	}
	if b := chatRunFrom(ctx); b != nil {
		ctx = sidequestion.WithHistory(ctx, b.run.HistoryRevision, b.run.ID)
		if p.TaskID > 0 && p.IntentID == 0 {
			seg := b.run.Session.MainSeg
			p.MainSeg = &seg
		}
	}
	if projector, ok := opts.Compactor.(harness.ContextView); ok {
		ctx = sidequestion.WithWorkingSetProjector(ctx, projector.View)
	}
	ctx, opts.Deps = sidequestion.Attach(ctx, p, opts.Deps, opts.Provider)
	return ctx
}
