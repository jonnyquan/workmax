//go:build desktop && cgo

package knowledge

import (
	"log"
	"os"
	"strings"
	"sync"
)

// Retrieval telemetry, and the experiment switch it exists to serve.
//
// # The question this is instrumentation for
//
// The KPI that decides whether local RAG stays is *task pass rate with
// retrieval minus task pass rate without it*. Not recall@k, not mean
// similarity, not "did we find the chunk" — a retriever can find the right
// chunk and still make the answer worse, by displacing context the model
// needed, by anchoring it on a stale note, or by injecting something confidently
// irrelevant. The only measurement that captures all three is the difference
// between two runs of the same task, one with retrieval and one without.
//
// That is why WORKMAX_EXPERIMENT_NO_RAG exists: it is the control arm. It
// hides retrieval without touching indexing, so the same install can be
// A/B'd against itself and the index does not go cold while the flag is set.
//
// The counters below are the supporting evidence for such a comparison — how
// often retrieval fired at all, how often it was suppressed, how much of what
// the search returned survived the thresholds. They are aggregate and
// content-free by construction.
//
// # Privacy
//
// No query text, no chunk text, no labels, no file names, no source ids. Not
// truncated, not hashed — absent. Lengths are reported as buckets rather than
// exact counts, because an exact rune count is a weak fingerprint of a
// specific message and a bucket answers every question a tuning decision
// actually asks. Anything added here later must clear the same bar: a support
// transcript containing this data must not narrow down what the user typed.

// experimentNoRAGEnv hides retrieval while leaving indexing on.
const experimentNoRAGEnv = "WORKMAX_EXPERIMENT_NO_RAG"

// RetrievalDisabled reports whether the counterfactual switch is set.
//
// Read from the environment on every call rather than cached at startup. The
// cost is a map lookup against an embedding model and a KNN scan, and the
// benefit is that the flag is a property of the run rather than of the launch
// — which is what makes it usable from a test with t.Setenv and from a support
// session without a restart.
func RetrievalDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(experimentNoRAGEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// RetrievalStats is a content-free snapshot of what retrieval has been doing
// this process. Every field is a count; none of them can be inverted into
// text.
type RetrievalStats struct {
	// Calls is every Retrieve that reached the retriever, including the ones
	// that returned nothing.
	Calls int64 `json:"calls"`
	// Disabled counts calls short-circuited by WORKMAX_EXPERIMENT_NO_RAG.
	Disabled int64 `json:"disabled"`
	// Suppressed counts calls skipped because the query carried too little
	// information to search with ("继续", "ok").
	Suppressed int64 `json:"suppressed"`
	// Searched counts calls that actually ran a search.
	Searched int64 `json:"searched"`
	// Errors counts calls that failed (embedding or SQL).
	Errors int64 `json:"errors"`
	// Empty counts searches that returned nothing after thresholding — the
	// "no relevant memory" outcome that used to be unrepresentable.
	Empty int64 `json:"empty"`

	// VectorCandidates / LexicalCandidates are the raw hits each half
	// produced, before any filtering. Their ratio is how you tell whether the
	// FTS half is contributing.
	VectorCandidates  int64 `json:"vector_candidates"`
	LexicalCandidates int64 `json:"lexical_candidates"`
	// VectorKept / LexicalKept survived their respective gates.
	VectorKept  int64 `json:"vector_kept"`
	LexicalKept int64 `json:"lexical_kept"`
	// LexicalOnlyInjected counts injected chunks the vector half never
	// surfaced — the direct answer to "was adding FTS5 worth it".
	LexicalOnlyInjected int64 `json:"lexical_only_injected"`
	// Injected is the number of chunks handed to the caller.
	Injected int64 `json:"injected"`

	// TopScoreBuckets counts searches by their best surviving similarity,
	// in tenths: index 6 is [0.6, 0.7). Index 10 is an exact 1.0.
	TopScoreBuckets [11]int64 `json:"top_score_buckets"`

	// LexicalUnavailable counts searches that ran vector-only because the
	// FTS5 shadow index was missing or had failed.
	LexicalUnavailable int64 `json:"lexical_unavailable"`
}

// retrievalTelemetry accumulates RetrievalStats.
type retrievalTelemetry struct {
	mu    sync.Mutex
	stats RetrievalStats
}

// defaultTelemetry is the process-wide recorder. Retrieval telemetry is a
// property of the sidecar process, not of any one Indexer — the lazily built
// indexer does not even exist for the calls that get suppressed or disabled —
// so the counters live here and every Indexer points at them by default.
var defaultTelemetry = &retrievalTelemetry{}

// RetrievalTelemetrySnapshot returns the current counters. Exposed so the
// diagnostics endpoint can report them without the desktop package knowing
// anything about how retrieval works.
func RetrievalTelemetrySnapshot() RetrievalStats {
	return defaultTelemetry.snapshot()
}

// NoteRetrievalDisabled records a retrieval the experiment switch
// short-circuited upstream of the retriever — the wiring layer checks the flag
// before loading the embedding model, so the counter has to be raised from
// there or the control arm would look like it never ran at all.
func NoteRetrievalDisabled(query string) {
	defaultTelemetry.record(retrievalOutcome{disabled: true, queryRunes: runeLen(query)})
}

func (t *retrievalTelemetry) snapshot() RetrievalStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stats
}

// retrievalOutcome is one search's worth of measurements, filled in by the
// retriever and handed to record exactly once.
type retrievalOutcome struct {
	disabled   bool
	suppressed bool
	failed     bool

	queryRunes         int
	vectorCandidates   int
	lexicalCandidates  int
	vectorKept         int
	lexicalKept        int
	lexicalOnly        int
	injected           int
	topScore           float64
	lexicalUnavailable bool
}

// record folds an outcome into the counters and emits one structured log
// line. The log line is the part a support transcript carries, so it holds the
// same content-free fields the counters do — a length bucket, never a length.
func (t *retrievalTelemetry) record(o retrievalOutcome) {
	t.mu.Lock()
	s := &t.stats
	s.Calls++
	switch {
	case o.disabled:
		s.Disabled++
	case o.suppressed:
		s.Suppressed++
	case o.failed:
		s.Errors++
	default:
		s.Searched++
		s.VectorCandidates += int64(o.vectorCandidates)
		s.LexicalCandidates += int64(o.lexicalCandidates)
		s.VectorKept += int64(o.vectorKept)
		s.LexicalKept += int64(o.lexicalKept)
		s.LexicalOnlyInjected += int64(o.lexicalOnly)
		s.Injected += int64(o.injected)
		if o.lexicalUnavailable {
			s.LexicalUnavailable++
		}
		if o.injected == 0 {
			s.Empty++
		} else {
			s.TopScoreBuckets[scoreBucket(o.topScore)]++
		}
	}
	t.mu.Unlock()

	switch {
	case o.disabled:
		log.Printf("knowledge: retrieval outcome=disabled_by_experiment q_len=%s", lengthBucket(o.queryRunes))
	case o.suppressed:
		log.Printf("knowledge: retrieval outcome=suppressed_low_signal q_len=%s", lengthBucket(o.queryRunes))
	case o.failed:
		log.Printf("knowledge: retrieval outcome=error q_len=%s", lengthBucket(o.queryRunes))
	default:
		log.Printf("knowledge: retrieval outcome=searched q_len=%s vec=%d/%d fts=%d/%d fts_only=%d injected=%d top_score=%s lexical=%s",
			lengthBucket(o.queryRunes),
			o.vectorKept, o.vectorCandidates,
			o.lexicalKept, o.lexicalCandidates,
			o.lexicalOnly, o.injected,
			bucketLabel(o.topScore), availabilityLabel(!o.lexicalUnavailable))
	}
}

// scoreBucket maps a similarity to its tenth. 1.0 gets its own slot so the
// array index can never run past the end.
func scoreBucket(score float64) int {
	switch {
	case score <= 0:
		return 0
	case score >= 1:
		return 10
	}
	return int(score * 10)
}

// bucketLabel renders a similarity as the tenth-wide range it falls in, so a
// log line never carries a score precise enough to correlate two sessions.
func bucketLabel(score float64) string {
	if score <= 0 {
		return "none"
	}
	b := scoreBucket(score)
	switch b {
	case 10:
		return "1.0"
	}
	return [...]string{"0.0-0.1", "0.1-0.2", "0.2-0.3", "0.3-0.4", "0.4-0.5",
		"0.5-0.6", "0.6-0.7", "0.7-0.8", "0.8-0.9", "0.9-1.0"}[b]
}

// lengthBucket renders a query length as a coarse range.
func lengthBucket(runes int) string {
	switch {
	case runes <= 0:
		return "0"
	case runes < 8:
		return "1-7"
	case runes < 24:
		return "8-23"
	case runes < 64:
		return "24-63"
	case runes < 256:
		return "64-255"
	}
	return "256+"
}

func availabilityLabel(ok bool) string {
	if ok {
		return "on"
	}
	return "off"
}
