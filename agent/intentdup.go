package agent

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// duplicateOpenIntent guards the planner against re-issuing a direction that is
// already dispatched. An intent in state open/running is a queued task, not an
// unassigned gap: it may simply be waiting for a free worker, and re-issuing it
// only creates parallel work on the same route (observed in production: the same
// direction was issued 4 times in 38 minutes while two copies stayed open).
//
// Intents that ended without a verdict about the direction — stopped (forced),
// blocked (model/network failure), exhausted (budget cut) — are deliberately
// repeatable: they carry no conclusion, so the planner may issue the direction
// again. Only open/running intents block.
//
// Two floors: 0.45 on summary similarity alone, and 0.25 when the two intents
// also share at least one target asset (same anchor makes near-identical wording
// likelier to mean the same route, while unrelated directions on one host stay
// well below).
const (
	dupSimilarityFloor         = 0.45
	dupAnchoredSimilarityFloor = 0.25
)

var intentNonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func intentTokens(s string) map[string]struct{} {
	toks := map[string]struct{}{}
	for _, w := range intentNonWord.Split(strings.ToLower(s), -1) {
		if len([]rune(w)) <= 3 {
			continue
		}
		toks[w] = struct{}{}
	}
	return toks
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if _, ok := b[w]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// intentSummaryAndAnchors reads summary and target assets from the node payload.
// ListByKind does not select the anchors column, so the payload is the only
// source available on the add_intent path.
func intentSummaryAndAnchors(n *db.Node) (string, []int64) {
	if n == nil || len(n.Payload) == 0 {
		return "", nil
	}
	var p struct {
		Summary  string  `json:"summary"`
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return "", nil
	}
	if len(p.AssetIDs) == 0 {
		return p.Summary, n.Anchors
	}
	return p.Summary, p.AssetIDs
}

// duplicateOpenIntent returns the queued intent the new summary duplicates.
func duplicateOpenIntent(nodes []*db.Node, summary string, anchors []int64) (int64, string, bool) {
	ns := intentTokens(summary)
	if len(ns) == 0 {
		return 0, "", false
	}
	newAnchors := map[int64]struct{}{}
	for _, a := range anchors {
		newAnchors[a] = struct{}{}
	}
	for _, n := range nodes {
		if n == nil || n.Kind != db.KindIntent {
			continue
		}
		if n.State != "open" && n.State != "running" {
			continue
		}
		existing, exAnchors := intentSummaryAndAnchors(n)
		if existing == "" {
			continue
		}
		sim := jaccard(ns, intentTokens(existing))
		if sim >= dupSimilarityFloor {
			return n.ID, existing, true
		}
		if len(newAnchors) == 0 {
			continue
		}
		for _, a := range exAnchors {
			if _, ok := newAnchors[a]; !ok {
				continue
			}
			if sim >= dupAnchoredSimilarityFloor {
				return n.ID, existing, true
			}
			break
		}
	}
	return 0, "", false
}
