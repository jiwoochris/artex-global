package agent

// cold-digest §4/§5/§7: the Compactor ties the pure algorithms (coldgraph.go)
// to the store (db/digest.go) and the LLM. It runs in two modes:
//
//	maintain — cheap, synchronous, once per planner round: bump round_no,
//	           recompute hot/cold, stamp/clear cold_since_round (§2.3). This is
//	           the bookkeeping the planner does anyway; it never calls the LLM.
//	minor/major — background, off the planner hot path (§7): group cold nodes
//	           and compress each ≥2 block into a digest via the LLM. minor folds
//	           only the not-yet-covered cold set (tiered append); major re-derives
//	           the whole grouping from source and merges fragments (§5.1/§5.2),
//	           reusing bodies whose signature is unchanged (§5.3).
//
// Concurrency: one compaction per task at a time (mutex), ≥cooldown between runs,
// and a commit-time liveness recheck drops any member that revived while the body
// was being generated so a digest never covers a hot node.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
	"github.com/yukiteruamano/artex-global/db"
)

// Compactor performs background cold-node compaction for many explorations.
type Compactor struct {
	prov     llm.Provider
	model    string
	params   coldParams
	n, m     int           // minor / major thresholds (§7 N=20, M=8)
	cooldown time.Duration // min gap between compactions per task (§7 60s)
	maxDur   time.Duration // hard cap on one background compaction

	mu      sync.Mutex
	running map[int64]bool
	lastRun map[int64]time.Time
}

// NewCompactor builds a compactor. prov/model are used for the §4 body LLM call
// (same model the agent runs on, per §4). A nil Compactor is a safe no-op.
func NewCompactor(prov llm.Provider, model string) *Compactor {
	return &Compactor{
		prov:     prov,
		model:    model,
		params:   defaultColdParams(),
		n:        20,
		m:        8,
		cooldown: 60 * time.Second,
		maxDur:   5 * time.Minute,
		running:  map[int64]bool{},
		lastRun:  map[int64]time.Time{},
	}
}

// OnPlannerRound is the single entry the planner calls each wake-up. It bumps the
// round, maintains the cold stamps synchronously, then (if a threshold is hit and
// no compaction is running / cooling down) launches a background compaction that
// outlives this planner round.
func (c *Compactor) OnPlannerRound(ctx context.Context, ts *db.ExplorationStore) {
	if c == nil || c.prov == nil || ts == nil {
		return
	}
	round, uncompressed, activeDigests, err := c.maintain(ts)
	if err != nil {
		log.Printf("[compaction] maintain exp=%d: %v", ts.ID(), err)
		return
	}
	needMinor := uncompressed >= c.n
	needMajor := activeDigests >= c.m
	if !needMinor && !needMajor {
		return
	}
	if !c.tryStart(ts.ID()) {
		return // already running, or within cooldown — the derived state converges eventually, compress next round
	}
	go func() {
		defer c.finish(ts.ID())
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.maxDur)
		defer cancel()
		// Compression is a bare provider call (compress calls prov.Complete directly) that bypasses agentcore's
		// session loop, so ctx carries no session id; gateways that do prompt caching / sticky
		// routing on the session-id header (opencode zen returns 400 without x-opencode-session) never see it.
		// So we add an id that is stable per exploration: every compression request of one exploration shares it,
		// which sends the header and lets llmrec attribute this call's tokens back to that exploration (it could not before).
		bg = transcript.WithSessionID(bg, fmt.Sprintf("exp%d-compactor", ts.ID()))
		if needMajor {
			c.major(bg, ts)
		} else {
			c.minor(bg, ts)
		}
	}()
	_ = round
}

// maintain bumps round_no, recomputes hot/cold over the whole graph, and applies
// the cold_since_round stamp/clear ops (§2.3). Returns the new round plus the
// counts that drive the trigger: how many eligible-cold nodes are not yet covered
// (minor) and how many active digests exist (major).
func (c *Compactor) maintain(ts *db.ExplorationStore) (round int64, uncompressed, activeDigests int, err error) {
	round, err = ts.BumpRound()
	if err != nil {
		return
	}
	g, _, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	hot := g.hotSet()
	structCold := g.structuralCold(hot)
	ops := computeStampOps(structCold, stamps, round)
	if err = ts.ApplyStampOps(toDBStampOps(ops)); err != nil {
		return
	}
	applyStampsInPlace(stamps, ops)
	elig := g.eligibleCold(structCold, stamps, round, c.params)
	covered, err := ts.CoveredMembers()
	if err != nil {
		return
	}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed++
		}
	}
	ad, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	activeDigests = len(ad)
	return
}

// minor folds the not-yet-covered eligible-cold set into new digest segments
// (tiered append, §5). Existing digests are untouched.
func (c *Compactor) minor(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return
	}
	covered, err := ts.CoveredMembers()
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	uncompressed := map[int64]bool{}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed[id] = true
		}
	}
	blocks := g.group(uncompressed, c.params)
	if len(blocks) == 0 {
		return // this batch has no ≥2 connected/shared-parent block — nothing to fold (§7)
	}
	for _, b := range blocks {
		c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, nil))
	}
	// A minor may have pushed the segment count over M → merge in the same run.
	if ad, e := ts.ActiveDigests(); e == nil && len(ad) >= c.m {
		c.major(ctx, ts)
	}
}

// major re-derives the whole grouping from source over ALL eligible-cold nodes
// (§5.1 re-derive from source), then reconciles against the active digests by signature:
// unchanged blocks keep their digest (no LLM), stale digests are superseded, and
// new/changed blocks are compressed afresh. This is where tiered fragments of one
// direction merge and where "later became connected" blocks unify (§5.2).
func (c *Compactor) major(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return
	}
	active, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	blocks := g.group(elig, c.params)

	bySig := map[string]*db.Node{}
	for _, d := range active {
		sig, _ := digestSigGen(d)
		bySig[sig] = d
	}
	desired := map[string]bool{}
	var toCreate []block
	for _, b := range blocks {
		sig := blockSignature(b, cvers)
		desired[sig] = true
		if _, ok := bySig[sig]; ok {
			continue // unchanged → reuse the existing digest, skip LLM (§5.3)
		}
		toCreate = append(toCreate, b)
	}
	// Supersede stale digests FIRST (atomic drop of their covers edges) so a member
	// is never covered by both an old and a new digest (§5.1 one-member-one-digest).
	var stale []int64
	for _, d := range active {
		sig, _ := digestSigGen(d)
		if !desired[sig] {
			stale = append(stale, d.ID)
		}
	}
	if err := ts.SupersedeDigests(stale); err != nil {
		log.Printf("[compaction] supersede exp=%d: %v", ts.ID(), err)
	}
	for _, b := range toCreate {
		c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, active))
	}
}

// foldBlock compresses one block and writes its digest — with a commit-time
// liveness recheck (§ concurrency): between grouping and write the graph may have
// changed, so any member that has since gone hot (revived) is dropped from the
// covers set. If the block dissolves below K it is skipped.
func (c *Compactor) foldBlock(ctx context.Context, ts *db.ExplorationStore, g *coldGraph, b block, nodeByID map[int64]*db.Node, cvers map[int64]int, generation int) {
	body, err := c.compress(ctx, g, b, nodeByID)
	if err != nil {
		log.Printf("[compaction] compress exp=%d block=%v: %v", ts.ID(), b.Members, err)
		return
	}
	// Re-read fresh state and drop any member that revived while we compressed.
	fresh, _, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	freshHot := fresh.hotSet()
	members := make([]int64, 0, len(b.Members))
	for _, mID := range b.Members {
		if !freshHot[mID] {
			members = append(members, mID)
		}
	}
	if len(members) < c.params.K {
		return // block revived out from under us — leave those nodes hot, don't fold
	}
	final := block{Members: members, Anchors: b.Anchors}
	payload := digestPayload(body, final, nodeByID, generation, blockSignature(final, cvers))
	if _, err := ts.AddDigest(payload, members); err != nil {
		log.Printf("[compaction] add digest exp=%d: %v", ts.ID(), err)
	}
}

// generationFor computes a digest's re-digest generation (§1): 1 for a fresh fold; for a major
// merge, max(generation) over the active digests that overlap this block's
// members, +1.
func (c *Compactor) generationFor(b block, active []*db.Node) int {
	if len(active) == 0 {
		return 1
	}
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	best := 0
	for _, d := range active {
		_, gen := digestSigGen(d)
		for _, m := range digestMemberIDs(d) {
			if memberSet[m] {
				if gen > best {
					best = gen
				}
				break
			}
		}
	}
	return best + 1
}

// tryStart acquires the per-task compaction lock, honoring the cooldown.
func (c *Compactor) tryStart(expID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[expID] {
		return false
	}
	if t, ok := c.lastRun[expID]; ok && time.Since(t) < c.cooldown {
		return false
	}
	c.running[expID] = true
	return true
}

func (c *Compactor) finish(expID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running[expID] = false
	c.lastRun[expID] = time.Now()
}

// --- helpers: db ↔ coldgraph ---

// loadColdGraph reads the exploration's nodes + edges and builds the cold-graph
// view plus an id→node index (for summaries/payload during compression).
func loadColdGraph(ts *db.ExplorationStore) (*coldGraph, map[int64]*db.Node, error) {
	// Compaction must see the WHOLE graph, not the default row caps — pass a very
	// high limit so the LIMIT clause is effectively unbounded for real task sizes.
	const allRows = 1 << 30
	nodes, err := ts.Nodes(allRows)
	if err != nil {
		return nil, nil, err
	}
	edges, err := ts.Edges(allRows)
	if err != nil {
		return nil, nil, err
	}
	cgNodes := make([]cgNode, 0, len(nodes))
	byID := make(map[int64]*db.Node, len(nodes))
	for _, n := range nodes {
		cgNodes = append(cgNodes, cgNode{ID: n.ID, Kind: n.Kind, State: n.State})
		byID[n.ID] = n
	}
	cgEdges := make([]cgEdge, 0, len(edges))
	for _, e := range edges {
		cgEdges = append(cgEdges, cgEdge{From: e.From, Rel: e.Rel, To: e.To})
	}
	return newColdGraph(cgNodes, cgEdges), byID, nil
}

func toDBStampOps(ops []stampOp) []db.StampOp {
	out := make([]db.StampOp, len(ops))
	for i, o := range ops {
		out[i] = db.StampOp{ID: o.ID, Set: o.Set, Round: o.Round}
	}
	return out
}

// applyStampsInPlace folds the just-applied ops into the in-memory stamp map so
// eligibility can be computed immediately without a re-read.
func applyStampsInPlace(stamps map[int64]*int64, ops []stampOp) {
	for _, o := range ops {
		if o.Set {
			r := o.Round
			stamps[o.ID] = &r
		} else {
			stamps[o.ID] = nil
		}
	}
}

// --- helpers: digest payload ---

// digestPayload builds the digest node payload (cold-digest §1): the body, the
// member ids split by kind (restore cache; source of truth is the covers edges),
// the anchor ids, the generation, and the change-detection signature.
func digestPayload(body string, b block, nodeByID map[int64]*db.Node, generation int, signature string) map[string]any {
	var facts, intents []int64
	for _, m := range b.Members {
		if n := nodeByID[m]; n != nil && n.Kind == db.KindIntent {
			intents = append(intents, m)
		} else {
			facts = append(facts, m)
		}
	}
	return map[string]any{
		"body":       body,
		"member_ids": map[string]any{"facts": facts, "intents": intents},
		"anchor_ids": b.Anchors,
		"generation": generation,
		"signature":  signature,
	}
}

func digestSigGen(n *db.Node) (string, int) {
	var p struct {
		Signature  string `json:"signature"`
		Generation int    `json:"generation"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Signature, p.Generation
}

func digestMemberIDs(n *db.Node) []int64 {
	var p struct {
		MemberIDs struct {
			Facts   []int64 `json:"facts"`
			Intents []int64 `json:"intents"`
		} `json:"member_ids"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return append(append([]int64{}, p.MemberIDs.Facts...), p.MemberIDs.Intents...)
}

// --- helpers: compression input + LLM (§4) ---

func nodeSummary(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
		if t, ok := p["text"].(string); ok {
			return t
		}
	}
	return ""
}

func nodeConfidence(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if c, ok := p["confidence"].(string); ok {
			return c
		}
	}
	return ""
}

// buildCompressionInput renders the connected sub-graph for the §4 prompt:
// member nodes (summary + id + kind + state + confidence), the internal blood
// edges among members, and — for a §3.1 shared-parent group — the anchor parents
// as context ("common parent #p"), which are NOT members.
func buildCompressionInput(g *coldGraph, b block, nodeByID map[int64]*db.Node) string {
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	var sb strings.Builder
	sb.WriteString("[Member nodes (to be compressed)]:\n")
	for _, m := range b.Members {
		n := nodeByID[m]
		kind := "fact"
		if n != nil && n.Kind == db.KindIntent {
			kind = "intent"
		}
		state := ""
		if n != nil {
			state = n.State
		}
		line := fmt.Sprintf("- #%d [%s/%s] %s", m, kind, state, nodeSummary(n))
		if conf := nodeConfidence(n); conf != "" {
			line += fmt.Sprintf(" (confidence=%s)", conf)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	// internal edges among members
	var edgeLines []string
	for _, m := range b.Members {
		for _, to := range g.children[m] {
			if memberSet[to] {
				edgeLines = append(edgeLines, fmt.Sprintf("- #%d yields/derives→ #%d", m, to))
			}
		}
	}
	if len(edgeLines) > 0 {
		sb.WriteString("\n[Blood edges among members (parent→child)]:\n")
		sort.Strings(edgeLines)
		sb.WriteString(strings.Join(edgeLines, "\n"))
		sb.WriteByte('\n')
	}
	if len(b.Anchors) > 0 {
		sb.WriteString("\n[Common parent / context anchor (not a member; only to show which intent these results were explored from)]:\n")
		for _, a := range b.Anchors {
			n := nodeByID[a]
			state := ""
			if n != nil {
				state = n.State
			}
			fmt.Fprintf(&sb, "- #%d [%s] %s\n", a, state, nodeSummary(n))
		}
	}
	return sb.String()
}

// compress runs the §4 body LLM call on one block. Uses the same model the agent
// runs on; thinking disabled (a pure summarization step).
func (c *Compactor) compress(ctx context.Context, g *coldGraph, b block, nodeByID map[int64]*db.Node) (string, error) {
	req := llm.CompletionRequest{
		System:    []string{compressionSystemPrompt},
		Messages:  []llm.Message{llm.UserText(buildCompressionInput(g, b, nodeByID))},
		MaxTokens: 1500,
		Thinking:  "disabled",
	}
	msg, _, _, err := c.prov.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(msg.Text())
	if body == "" {
		return "", fmt.Errorf("empty body from model")
	}
	return body, nil
}

// compressionSystemPrompt is the §4 body prompt.
const compressionSystemPrompt = `You are compressing a group of [mutually related] exploration nodes and producing one synthesized conclusion (body), so the planner can quickly grasp "what this slice has already established".

The input is one connected sub-graph:
- Nodes: each entry is the summary of an intent or a fact (one sentence), with id, kind (intent/fact), state, and confidence (if any).
- Relations: the blood edges between nodes (A derived from B / A yields B), showing how they chain together.
- If nodes have no direct blood edge between them but belong to one upstream intent (that upstream intent is given separately as the "common parent #p"), synthesize them as "what this intent (#p) established" — the common parent is only a context anchor, not a member to compress.

Based on that, write one body:
1. Synthesize, do not enumerate: follow the relations and chain the causality together (which fact gave rise to which intent, which intent yielded which conclusion), telling it as "what this slice of exploration concluded"; do not copy each summary out again.
2. Keep the distinctions: conclusions that differ from each other are each stated clearly; do not mash them into one vague sentence.
3. Preserve evidence strength: mark conclusions that carry a confidence as observed / inferred; for inferred negative or doubtful conclusions, make clear that it is only an inference and can be re-checked; do not write it as settled fact.
4. Include ids: after each conclusion note the source node id (e.g. "…(#12,#28)"), so the planner can restore the original nodes by id.
5. State it positively, write only what is in the input: do not invent, do not introduce judgments that are not in the input.
6. Length follows the content: short when there are few conclusions, long enough when there are many and they differ — but overall clearly shorter than the sum of all input summaries.

Output only the body text itself.`
