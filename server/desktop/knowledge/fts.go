//go:build desktop && cgo

package knowledge

import (
	"strings"
	"unicode"
)

// This file holds the lexical half of hybrid retrieval: how text is turned
// into FTS5 terms, and how a query is turned into a MATCH expression.
//
// # Why FTS5 at all
//
// A 384-dim MiniLM embedding is a similarity instrument, not an identity one.
// It cannot reliably distinguish `resolveFileNames` from `resolveThreadNames`,
// and a query that is a path, an error string, or an exact phrase is precisely
// the query where the user knows what they are looking for and the vector
// index is least able to find it. FTS5 with bm25() ships inside
// modernc.org/sqlite, so the lexical half costs no new dependency.
//
// # Tokenizer selection — measured, not assumed
//
// Both candidate tokenizers were run against modernc.org/sqlite v1.47 through
// glebarez/go-sqlite before this was written:
//
//   - `tokenize='unicode61'` (the default) treats an unbroken run of CJK as a
//     single token. Indexing 本地检索质量优化方案 makes it matchable only by
//     that entire string: a query for 检索 returns nothing. Unusable for
//     Chinese on its own.
//   - `tokenize='trigram'` does match inside CJK, but its query terms must be
//     at least three characters. A query for 薪资 — two characters, and the
//     single most common shape of a Chinese word — returned NO ROWS. It also
//     rejects `store.go` unless the caller quotes it. Rejected: a Chinese
//     retriever that cannot match two-character words is not a Chinese
//     retriever.
//   - `unicode61` fed pre-segmented text — CJK runs expanded to unigrams *and*
//     bigrams at write time, the same expansion applied to the query — matched
//     薪资 and 索质 and `resolvefilenames` in the same index. This is what is
//     implemented.
//
// The cost of the chosen approach is index size: a CJK run of n runes emits
// 2n-1 terms. For a personal knowledge base that is a rounding error against
// the 1.5KB every chunk already spends on its embedding.
//
// Unigrams are kept alongside bigrams so a single-character query (a surname,
// 猫) still matches; bigrams are what make a two-character word rank above a
// document that merely contains both characters apart.

// segmentForIndex renders text as the space-separated term stream stored in
// the FTS5 body column.
//
// Latin/digit runs are lowercased and additionally split on the punctuation
// inside them, so `server/desktop/store.go` contributes `server`, `desktop`,
// `store` and `go` as well as the joined form. That is what lets a query for a
// bare function name hit a chunk that only ever mentions it as part of a path.
func segmentForIndex(text string) string {
	terms := segmentTerms(text)
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " ")
}

// segmentTerms is the shared segmentation used for both writing and querying.
// The two must not drift: a term that is written one way and queried another
// simply never matches, and nothing would report it.
func segmentTerms(text string) []string {
	var terms []string
	var latin []rune
	var cjk []rune

	flushLatin := func() {
		if len(latin) == 0 {
			return
		}
		terms = append(terms, string(latin))
		latin = latin[:0]
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		for i, r := range cjk {
			terms = append(terms, string(r))
			if i+1 < len(cjk) {
				terms = append(terms, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range text {
		switch {
		case isCJK(r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin = append(latin, unicode.ToLower(r))
		default:
			// Punctuation and whitespace are separators. `store.go` therefore
			// yields `store` and `go`; the joined form is added below.
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return terms
}

// ftsMatchExpr builds the FTS5 MATCH expression for a query: every segmented
// term, quoted, ORed together.
//
// Quoting every term is not cosmetic. FTS5's query language treats `.`, `(`,
// `*`, `AND`, `OR`, `NOT` and `NEAR` as syntax, so passing raw user text
// through produces either a parse error or — as measured — a silent zero-row
// result for a query like `store.go`. Segmenting first and quoting each term
// removes the user's text from the grammar entirely: a query can no longer be
// a malformed expression, only a set of literals. The OR makes recall the
// goal and leaves precision to bm25 ranking and the coverage gate.
//
// Returns "" when the query contributes no terms, which the caller must read
// as "do not run a lexical search" rather than "match everything".
func ftsMatchExpr(query string) string {
	terms := dedupeTerms(segmentTerms(query))
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}

func dedupeTerms(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// lexicalCoverage reports the fraction of the query's distinct terms that
// appear in a chunk's term stream.
//
// It is the admission test for a hit that only the lexical index found. A
// vector hit arrives with a cosine similarity that can be thresholded; an FTS
// hit arrives with a bm25 score whose scale depends on corpus statistics and
// is meaningless in isolation (measured: bm25 returns 0.0 for every row when
// the term appears in every document). Coverage is the honest substitute — it
// answers "how much of what the user typed is actually in here" — and it is
// computed from the same segmentation both sides already agree on.
func lexicalCoverage(queryTerms map[string]bool, body string) float64 {
	if len(queryTerms) == 0 {
		return 0
	}
	present := make(map[string]bool, len(queryTerms))
	for _, t := range strings.Fields(body) {
		if queryTerms[t] {
			present[t] = true
		}
	}
	return float64(len(present)) / float64(len(queryTerms))
}

// queryTermSet is the deduped term set of a query, for lexicalCoverage.
func queryTermSet(query string) map[string]bool {
	terms := dedupeTerms(segmentTerms(query))
	set := make(map[string]bool, len(terms))
	for _, t := range terms {
		set[t] = true
	}
	return set
}
