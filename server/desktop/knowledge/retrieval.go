//go:build desktop && cgo

package knowledge

import (
	"strings"
	"unicode"
)

// Retrieval policy: what counts as a query worth searching, what counts as a
// hit worth injecting, and how the two candidate lists become one.
//
// The starting state this replaces had none of these. `Search` returned topK
// unconditionally, and KNN always has a nearest neighbour, so every turn — a
// question, a "继续", a "ok" — came back with four chunks and pushed them into
// the prompt. There was no representable "nothing relevant" outcome, which
// meant retrieval could not be wrong, only differently confident.

const (
	// minSimilarity is the absolute floor a vector hit must clear.
	//
	// Chosen conservatively rather than fitted. all-MiniLM-L6-v2 puts
	// unrelated sentence pairs broadly in the 0.0-0.25 cosine band and related
	// ones above ~0.35, with the gap narrowing for short texts; 0.30 sits
	// inside that gap, biased towards dropping a marginal hit rather than
	// injecting an irrelevant one. That bias is deliberate: a missing memory
	// costs the model context it can ask for, an irrelevant one costs it
	// attention it cannot get back.
	//
	// It is a floor, not a fitted threshold, and it should be re-derived from
	// the real score distribution once the experiment switch above has
	// produced enough with/without data to compare against.
	minSimilarity = 0.30

	// autoRecallRatio is the relative cut applied after the floor: keep top1,
	// drop anything scoring below ratio × top1.
	//
	// The two cuts have to be chosen together or one of them is dead code.
	// Cosine similarity here is bounded by 1.0, so the strictest cut a ratio
	// can ever produce is ratio × 1.0 — and with the floor at 0.30, any ratio
	// below 0.30 is unreachable: max(ratio × top1) < floor means the floor
	// always dominates and the relative layer never fires once. (The 0.24
	// figure this was drafted with is exactly such a value; it comes from a
	// system whose scores are not normalised to 1.)
	//
	// 0.5 is the smallest round value that binds on a meaningful part of the
	// range: it starts trimming as soon as the best hit clears 0.60, which is
	// the regime where "one strong match plus a marginal tail" actually
	// happens, and leaves a weak-but-uniform result set intact.
	//
	// Automatic recall — the user did not ask to search, the turn searched on
	// their behalf — gets the strict value, because the cost of a wrong hit is
	// paid silently inside the prompt. An explicit search UI, if one is ever
	// built, should pass a looser ratio through KeepTopRelative: there the user
	// is looking at the results and can dismiss the tail themselves. No such
	// entry point exists today, so only the strict constant is wired.
	autoRecallRatio = 0.5

	// minLexicalCoverage is the admission test for a hit only the FTS index
	// found: at least this fraction of the query's distinct terms must appear
	// in the chunk. bm25 cannot serve as this gate — it is a corpus-relative
	// ranking score, measured returning 0.0 for every row when a term appears
	// in every document — so coverage is the substitute, and it answers a
	// question a user would recognise: how much of what I typed is in here.
	minLexicalCoverage = 0.5

	// rrfK is the smoothing constant of reciprocal rank fusion,
	// score = Σ 1/(k + rank) over the lists a document appears in, rank
	// starting at 1. 60 is the value from the paper that introduced RRF
	// (Cormack, Clarke & Buettcher, 2009) and the one every subsequent
	// hybrid-search implementation inherited; it is large enough that the
	// difference between rank 1 and rank 2 does not dominate agreement
	// between the two retrievers, which is the property fusion is for.
	//
	// Ranks, not scores, is the whole point: cosine similarity and bm25 have
	// no common scale, no common sign, and bm25's magnitude depends on corpus
	// statistics that change as the user indexes more. Fusing on rank needs
	// neither to be calibrated.
	rrfK = 60.0

	// candidatePoolMultiple widens each retriever's own top-K before fusion.
	// A document ranked 5th by one retriever and 1st by the other should be
	// able to win; it cannot if the pool was cut to the final K first.
	candidatePoolMultiple = 4
	minCandidatePool      = 12
)

// candidatePool sizes each half's candidate list for a requested topK.
func candidatePool(topK int) int {
	n := topK * candidatePoolMultiple
	if n < minCandidatePool {
		n = minCandidatePool
	}
	return n
}

// KeepTopRelative trims a similarity-ordered list to the hits worth using:
// everything below the absolute floor goes, then everything scoring below
// ratio × the best remaining score goes.
//
// The two cuts answer different questions. The floor asks "is this related to
// the query at all", which is a property of the hit. The ratio asks "is this
// in the same league as the best thing we found", which is a property of the
// result set — it is what turns one strong match plus three weak ones into one
// result instead of four.
//
// Input must be best-first, which is what both retrievers return.
func KeepTopRelative(scores []float64, ratio float64) int {
	if len(scores) == 0 {
		return 0
	}
	if scores[0] < minSimilarity {
		return 0
	}
	cut := scores[0] * ratio
	if cut < minSimilarity {
		cut = minSimilarity
	}
	kept := 0
	for _, s := range scores {
		if s < cut {
			break
		}
		kept++
	}
	return kept
}

// genericTurns are the utterances that carry a conversation forward without
// asking anything. Retrieval on one of these is pure noise: KNN has no null
// result, so "继续" returns the four chunks nearest to the *embedding of the
// word continue*, which is unrelated to whatever the user actually wants
// continued.
//
// Matched after normalisation (lowercased, punctuation collapsed to spaces),
// both as the whole utterance and term by term, so "好的，继续" is caught as
// well as "继续".
var genericTurns = map[string]bool{
	// Chinese
	"继续": true, "继续吧": true, "继续说": true, "接着说": true, "然后": true,
	"然后呢": true, "下一步": true, "下一个": true, "再来": true, "再来一个": true,
	"好": true, "好的": true, "好吧": true, "行": true, "行吧": true, "可以": true,
	"没问题": true, "嗯": true, "嗯嗯": true, "是": true, "是的": true, "对": true,
	"对的": true, "收到": true, "谢谢": true, "谢谢你": true, "多谢": true,
	"不用了": true, "算了": true, "开始": true, "试试": true, "改一下": true,
	// English
	"ok": true, "okay": true, "k": true, "kk": true, "yes": true, "y": true,
	"yeah": true, "yep": true, "yup": true, "no": true, "n": true, "nope": true,
	"sure": true, "fine": true, "go": true, "go on": true, "goahead": true,
	"go ahead": true, "continue": true, "next": true, "more": true,
	"again": true, "thanks": true, "thank you": true, "thx": true, "ty": true,
	"done": true, "please": true, "proceed": true, "keep going": true,
	"do it": true, "carry on": true,
}

// HasRetrievalSignal reports whether a query carries enough information to be
// worth searching with. False means skip retrieval entirely — no search, no
// injection, and no retrieval event, because announcing "I searched and found
// nothing" for the word "ok" is noise in the UI as well as in the prompt.
//
// Two independent tests, either of which can veto:
//
//  1. The whole normalised utterance, or every one of its terms, is a generic
//     continuation.
//  2. There is not enough of it. Four CJK characters, or two Latin words, or
//     one Latin word of four characters or more — the last clause matters
//     because a bare `resolveFileNames` is a completely legitimate query and a
//     word-count rule alone would throw it away. Four is low enough to keep
//     "beta" and "java", and safe only because the short fillers it would
//     otherwise admit ("okay", "sure", "next", "done") are named in the list
//     above.
func HasRetrievalSignal(query string) bool {
	norm := normalizeQuery(query)
	if norm == "" {
		return false
	}
	if genericTurns[norm] {
		return false
	}
	fields := strings.Fields(norm)
	allGeneric := true
	for _, f := range fields {
		if !genericTurns[f] {
			allGeneric = false
			break
		}
	}
	if allGeneric {
		return false
	}

	cjk := 0
	words := 0
	longest := 0
	for _, f := range fields {
		runes := []rune(f)
		isCJKWord := false
		for _, r := range runes {
			if isCJK(r) {
				cjk++
				isCJKWord = true
			}
		}
		if isCJKWord {
			continue
		}
		words++
		if len(runes) > longest {
			longest = len(runes)
		}
	}
	switch {
	case cjk >= 4:
		return true
	case words >= 2:
		return true
	case words == 1 && longest >= 4:
		return true
	}
	return false
}

// normalizeQuery lowercases, replaces punctuation with spaces and collapses
// runs of whitespace, so "好的，继续!" and "继续" normalise to comparable forms.
func normalizeQuery(query string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(strings.TrimSpace(query)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// fusionEntry is one candidate travelling through RRF.
type fusionEntry struct {
	chunkUID string
	result   ChunkResult
	rrf      float64
	fromVec  bool
	fromFTS  bool
}

// fuseRRF merges two rank-ordered candidate lists into one, best first.
//
// Both inputs must already be filtered — this decides ordering, not
// admission. A chunk found by both retrievers accumulates both reciprocal
// ranks and therefore outranks a chunk that either one alone put first, which
// is precisely the agreement signal that makes hybrid retrieval better than
// its halves.
func fuseRRF(vec []ChunkResult, lex []LexicalResult) []fusionEntry {
	byUID := make(map[string]*fusionEntry, len(vec)+len(lex))
	order := make([]*fusionEntry, 0, len(vec)+len(lex))

	add := func(uid string, res ChunkResult, rank int, fromVec bool) {
		e, ok := byUID[uid]
		if !ok {
			e = &fusionEntry{chunkUID: uid, result: res}
			byUID[uid] = e
			order = append(order, e)
		}
		e.rrf += 1.0 / (rrfK + float64(rank))
		if fromVec {
			e.fromVec = true
			// The vector row carries a real distance; the lexical row does
			// not. Prefer the one that knows.
			e.result.Distance = res.Distance
		} else {
			e.fromFTS = true
		}
	}

	for i, h := range vec {
		add(h.ChunkUID, h, i+1, true)
	}
	for i, h := range lex {
		add(h.ChunkUID, h.ChunkResult, i+1, false)
	}

	// Stable insertion sort by fused score: the lists are candidate-pool
	// sized (tens of entries), and stability keeps the original best-first
	// order as the tiebreak instead of leaving ties to map iteration.
	out := make([]fusionEntry, 0, len(order))
	for _, e := range order {
		out = append(out, *e)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].rrf > out[j-1].rrf; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
