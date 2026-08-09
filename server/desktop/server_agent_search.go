//go:build desktop

package desktop

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// GET /agent/search?q=… — full-text-ish search over message content.
//
// The sidebar search only ever matched thread titles, and the reason was
// honest at the time: message bodies live in SQLite and are not loaded for
// unselected threads, so the renderer could not search what it did not have.
// The data's owner can. This route searches user and assistant text server-
// side, scoped to the caller's uid like every history read.
//
// Matching is a case-insensitive substring (instr + lower), not FTS5: local
// scale is thousands of messages, a table scan answers in milliseconds, and
// an FTS index would be a second copy of every message to keep in sync —
// machinery this need does not yet justify. No LIKE, so no wildcard/escape
// surface either: the query is a literal, never a pattern.

type searchMatch struct {
	ThreadUUID string `json:"thread_uuid"`
	ThreadName string `json:"thread_name"`
	Role       string `json:"role"` // "you" | "assistant"
	Snippet    string `json:"snippet"`
	CreatedAt  string `json:"created_at"`
}

type searchResponse struct {
	Items []searchMatch `json:"items"`
	Count int           `json:"count"`
}

const (
	maxSearchQueryChars   = 200
	maxSearchMatches      = 20
	searchSnippetRuneWing = 60
)

func (s *Server) handleSearchMessages(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "search_unavailable"})
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > maxSearchQueryChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query"})
		return
	}
	uid := s.activeLocalHistoryUID()
	needle := strings.ToLower(query)

	rows, err := s.cfg.DB.Raw(
		`SELECT t.uuid, t.name, m.user_text, m.ai_text, m.created_at
		   FROM w_workagent_message m
		   JOIN w_workagent_thread t ON t.id = m.thread_id AND t.uid = m.uid
		  WHERE m.uid = ?
		    AND (instr(lower(COALESCE(m.user_text,'')), ?) > 0
		      OR instr(lower(COALESCE(m.ai_text,'')), ?) > 0)
		  ORDER BY m.created_at DESC, m.id DESC
		  LIMIT ?`,
		uid, needle, needle, maxSearchMatches,
	).Rows()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search_failed"})
		return
	}
	defer rows.Close()

	items := []searchMatch{}
	for rows.Next() {
		var (
			threadUUID, threadName string
			userText, aiText       *string
			createdAt              string
		)
		if err := rows.Scan(&threadUUID, &threadName, &userText, &aiText, &createdAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "search_failed"})
			return
		}
		// One message row can match in both halves; each matching half is its
		// own result, because "who said it" changes what the snippet means.
		if userText != nil {
			if snippet, ok := searchSnippet(*userText, needle); ok {
				items = append(items, searchMatch{
					ThreadUUID: threadUUID, ThreadName: threadName,
					Role: "you", Snippet: snippet, CreatedAt: createdAt,
				})
			}
		}
		if aiText != nil {
			if snippet, ok := searchSnippet(*aiText, needle); ok {
				items = append(items, searchMatch{
					ThreadUUID: threadUUID, ThreadName: threadName,
					Role: "assistant", Snippet: snippet, CreatedAt: createdAt,
				})
			}
		}
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search_failed"})
		return
	}
	if len(items) > maxSearchMatches {
		items = items[:maxSearchMatches]
	}
	c.JSON(http.StatusOK, searchResponse{Items: items, Count: len(items)})
}

// searchSnippet cuts a window of text around the first case-insensitive
// occurrence of needle, on rune boundaries, elided at both ends.
func searchSnippet(text, lowerNeedle string) (string, bool) {
	idx := strings.Index(strings.ToLower(text), lowerNeedle)
	if idx < 0 {
		return "", false
	}
	// idx is a byte offset into the lowered string; for ASCII needles in UTF-8
	// text the offsets agree, and ToLower never changes byte length for the
	// characters where offsets could drift meaningfully. Clamp to rune
	// boundaries in the ORIGINAL text to stay safe regardless.
	for idx > 0 && !utf8.RuneStart(text[idx]) {
		idx--
	}
	runes := []rune(text[:idx])
	start := len(runes) - searchSnippetRuneWing
	if start < 0 {
		start = 0
	}
	all := []rune(text)
	end := len(runes) + utf8.RuneCountInString(lowerNeedle) + searchSnippetRuneWing
	if end > len(all) {
		end = len(all)
	}
	snippet := strings.TrimSpace(string(all[start:end]))
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(all) {
		snippet += "…"
	}
	return snippet, true
}
