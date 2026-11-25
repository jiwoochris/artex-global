package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/Autumn-27/artex/db"
)

// intentRegisterMu serializes the read-then-insert in addOneIntent. The
// duplicate check and AddIntent are two statements; without this lock two
// planning rounds finishing together could both observe an empty queue and
// insert the same direction. The server runs one process, so a process-wide
// mutex covers every planner ToolSet.
var intentRegisterMu sync.Mutex

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
// Floors: 0.75 when no target comparison is possible (unanchored pairs can
// describe different hosts), 0.6 for short anchored summaries, and 0.25 when
// both anchored summaries carry at least eight word tokens. CJK character
// bigrams feed similarity but not the word count, so a CJK summary cannot
// silently lower its own short-summary floor by producing many bigrams.
const (
	dupAnchoredSimilarityFloor   = 0.25
	dupUnanchoredSimilarityFloor = 0.75
	dupShortSummaryFloor         = 0.6
	dupCJKShortFloor             = 0.35
	dupCJKLongFloor              = 0.25
	dupCJKLongRunes              = 16
	dupLongSummaryMinTokens      = 8
)

var intentNonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// intentURL matches a target address. It is context, not direction: the same
// host appears in every check against it and would inflate similarity.
var intentURL = regexp.MustCompile(`https?://\S+`)

// intentStopwords are short function words that carry no direction. Short
// domain tokens (xss, rce, sqli, ssrf, lfi, xxe) are kept on purpose: dropping
// every token of three runes or fewer collapses "Test DOM XSS" and "Test RPC
// RCE" to the same token set.
var intentStopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "to": {}, "of": {}, "on": {}, "in": {},
	"at": {}, "for": {}, "and": {}, "or": {}, "vs": {}, "by": {}, "with": {},
	"from": {}, "as": {}, "is": {}, "are": {}, "be": {}, "do": {}, "no": {},
	"not": {}, "into": {}, "via": {},
	// Generic verbs carry no direction: two different checks on one host
	// otherwise share them and look like the same direction.
	"test": {}, "check": {}, "verify": {}, "probe": {}, "scan": {},
	"проверить": {}, "проверка": {}, "проверки": {}, "проверять": {},
	"исследование": {}, "исследовать": {}, "выполнить": {},
	"инъекции": {}, "инъекция": {}, "injection": {},
	// Russian function words: short enough to survive the length cutoff but
	// carrying no direction.
	"а": {}, "без": {}, "более": {}, "бы": {}, "в": {}, "во": {}, "вот": {},
	"все": {}, "всё": {}, "вы": {}, "для": {}, "до": {}, "его": {}, "ее": {},
	"за": {}, "здесь": {}, "из": {}, "или": {}, "им": {}, "их": {}, "к": {},
	"как": {}, "ко": {}, "ли": {}, "либо": {}, "мне": {}, "мы": {}, "на": {},
	"над": {}, "наш": {}, "не": {}, "нет": {}, "ни": {}, "них": {}, "но": {},
	"ну": {}, "о": {}, "об": {}, "однако": {}, "он": {}, "она": {}, "они": {},
	"оно": {}, "от": {}, "перед": {}, "по": {}, "под": {}, "при": {}, "про": {},
	"с": {}, "со": {}, "так": {}, "также": {}, "тем": {}, "то": {}, "тот": {},
	"у": {}, "уж": {}, "хотя": {}, "чего": {}, "чем": {}, "что": {}, "чтобы": {},
	"эта": {}, "эти": {}, "это": {}, "этот": {}, "я": {},
}

func intentTokens(s string) map[string]struct{} {
	return intentTokensMode(s, false, true)
}

// intentTokensWithHost keeps the hostname as a token. It is used when no asset
// anchors are available: the host is then the only target signal, and two
// identical summaries about different hosts must not collapse.
func intentTokensWithHost(s string) map[string]struct{} {
	return intentTokensMode(s, true, true)
}

// intentWordCount counts only whitespace-delimited word tokens (no CJK
// bigrams). It decides whether a summary is long enough for the low anchored
// floor; using the bigram-expanded set would make every CJK summary look long
// and disable the short-summary protection.
func intentWordCount(s string) int {
	return len(intentTokensMode(s, false, false))
}

func intentTokensMode(s string, keepHost, withBigrams bool) map[string]struct{} {
	toks := map[string]struct{}{}
	var hosts []string
	if keepHost {
		s = intentURL.ReplaceAllStringFunc(s, func(m string) string {
			if u, err := url.Parse(m); err == nil && u.Hostname() != "" {
				hosts = append(hosts, u.Hostname())
				return " "
			}
			return m
		})
	} else {
		s = intentURL.ReplaceAllString(s, " ")
	}
	for _, w := range intentNonWord.Split(strings.ToLower(s), -1) {
		if len([]rune(w)) < 2 {
			continue
		}
		if _, stop := intentStopwords[w]; stop {
			continue
		}
		toks[w] = struct{}{}
		if withBigrams && containsCJK(w) {
			for _, g := range cjkBigrams(w) {
				toks[g] = struct{}{}
			}
		}
	}
	for _, h := range hosts {
		toks["host:"+h] = struct{}{}
	}
	return toks
}

// containsCJK reports whether a token needs character n-grams: CJK text has no
// spaces, so word splitting alone makes every summary a single opaque token.
func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return true
		}
	}
	return false
}

func cjkBigrams(s string) []string {
	rs := []rune(s)
	out := make([]string, 0, len(rs))
	for i := 0; i+1 < len(rs); i++ {
		if unicode.Is(unicode.Han, rs[i]) || unicode.Is(unicode.Hiragana, rs[i]) || unicode.Is(unicode.Katakana, rs[i]) {
			out = append(out, string(rs[i:i+2]))
		}
	}
	return out
}

func cjkRuneCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			n++
		}
	}
	return n
}

// sharesDifferentCJKTail reports a long common prefix followed by different
// endings. Two checks on one asset often start with the same generic object
// ("check the login API...") and diverge into different techniques; those are
// separate directions, even though character bigrams make them look similar.
// A paraphrase usually reorders the wording instead of sharing a long prefix.
func sharesDifferentCJKTail(a, b string) bool {
	ar, br := []rune(a), []rune(b)
	limit := min(len(ar), len(br))
	prefix := 0
	for prefix < limit && ar[prefix] == br[prefix] {
		prefix++
	}
	if prefix < 4 || prefix >= limit {
		return false
	}
	return float64(prefix)/float64(limit) >= 0.5
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
		Summary   string  `json:"summary"`
		AssetIDs  []int64 `json:"asset_ids"`
		TargetIDs []int64 `json:"target_ids"`
		TargetID  *int64  `json:"target_id"`
	}
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return "", nil
	}
	anchors := make([]int64, 0, len(p.AssetIDs)+len(p.TargetIDs)+1)
	anchors = append(anchors, p.AssetIDs...)
	anchors = append(anchors, p.TargetIDs...)
	if p.TargetID != nil {
		anchors = append(anchors, *p.TargetID)
	}
	if len(anchors) == 0 {
		return p.Summary, n.Anchors
	}
	return p.Summary, anchors
}

// duplicateOpenIntent returns the queued intent the new summary duplicates.
func duplicateOpenIntent(nodes []*db.Node, summary string, anchors []int64) (int64, string, bool) {
	ns := intentTokens(summary)
	nsHost := intentTokensWithHost(summary)
	if len(ns) == 0 && len(nsHost) == 0 {
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
		if n.State != "open" && n.State != "running" && n.State != "paused" {
			continue
		}
		existing, exAnchors := intentSummaryAndAnchors(n)
		if existing == "" {
			continue
		}
		// Both intents target known assets: disjoint target sets are never the
		// same direction. Overlapping sets are evaluated by similarity, so an
		// expanded target set cannot bypass the check by adding one asset.
		bothAnchored := len(newAnchors) > 0 && len(exAnchors) > 0
		if bothAnchored && !anchorsOverlap(newAnchors, exAnchors) {
			continue
		}
		newTokens := ns
		existingTokens := intentTokens(existing)
		if !bothAnchored {
			newTokens, existingTokens = nsHost, intentTokensWithHost(existing)
		}
		sim := jaccard(newTokens, existingTokens)
		if !bothAnchored {
			if sim >= dupUnanchoredSimilarityFloor {
				return n.ID, existing, true
			}
			continue
		}
		isCJK := containsCJK(summary) || containsCJK(existing)
		if isCJK && sharesDifferentCJKTail(summary, existing) {
			continue
		}
		// Long summaries carry enough distinguishing detail for a low floor;
		// short ones need a high one so "stored XSS" and "reflected XSS" on one
		// asset do not collapse into a single direction.
		floor := dupShortSummaryFloor
		switch {
		case isCJK && min(cjkRuneCount(summary), cjkRuneCount(existing)) >= dupCJKLongRunes:
			floor = dupCJKLongFloor
		case isCJK:
			floor = dupCJKShortFloor
		case min(intentWordCount(summary), intentWordCount(existing)) >= dupLongSummaryMinTokens:
			floor = dupAnchoredSimilarityFloor
		}
		if sim >= floor {
			return n.ID, existing, true
		}
	}
	return 0, "", false
}

// anchorsOverlap reports whether the two target sets share at least one asset.
func anchorsOverlap(set map[int64]struct{}, b []int64) bool {
	for _, v := range b {
		if _, ok := set[v]; ok {
			return true
		}
	}
	return false
}
