//go:build desktop

package desktop

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// POST /agent/threads/:uuid/export — the conversation as a Markdown file.
//
// Local-first has to include "and you can take it with you": a history that
// lives in this app's SQLite but cannot leave as a plain file is a cache, not
// the user's data. The export lands in the thread's own workspace directory,
// which means the Deliverables panel lists it and the existing reveal route
// opens it — no new download machinery, no new permissions.
//
// The file is written server-side because the renderer has no filesystem; the
// path is derived entirely from the validated uuid and a fixed name, so the
// request carries nothing that could point the write anywhere else.

type exportThreadResponse struct {
	Exported bool   `json:"exported"`
	Path     string `json:"path"` // workspace-relative, matching the listing
	Messages int    `json:"messages"`
	Bytes    int    `json:"bytes"`
}

func (s *Server) handleExportThread(c *gin.Context) {
	if s.cfg.DB == nil || s.cfg.DataDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	// Ownership exactly as the workspace routes check it: the thread row
	// under the caller's uid is the authority, and a foreign uuid is not
	// found rather than forbidden.
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	uid := identity.UID
	var (
		threadID   uint64
		threadName string
	)
	if err := s.cfg.DB.Raw(
		`SELECT id, name FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID, &threadName); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}
	messages, err := ListLocalMessages(s.cfg.DB, uid, threadUUID, 1000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export_failed"})
		return
	}
	if len(messages) == 0 {
		// Exporting nothing produces a file that says nothing; refusing is
		// more honest than an empty deliverable.
		c.JSON(http.StatusConflict, gin.H{"error": "thread_empty"})
		return
	}

	content := renderThreadMarkdown(threadName, messages)
	dir := filepath.Join(s.cfg.DataDir, "agent_workspace", "thread_"+threadUUID, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export_failed"})
		return
	}
	// Date-stamped, so repeated exports version themselves instead of
	// silently overwriting yesterday's snapshot mid-review.
	name := "conversation-" + time.Now().UTC().Format("2006-01-02-150405") + ".md"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export_failed"})
		return
	}
	c.JSON(http.StatusOK, exportThreadResponse{
		Exported: true,
		Path:     "exports/" + name,
		Messages: len(messages),
		Bytes:    len(content),
	})
}

// renderThreadMarkdown lays the exchange out the way the thread reads:
// question, answer, in order, with honest metadata and nothing invented.
func renderThreadMarkdown(threadName string, messages []LocalMessageRow) string {
	var b strings.Builder
	title := strings.TrimSpace(threadName)
	if title == "" {
		title = "Conversation"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "Exported from WorkMax Desktop · %d message(s)\n", len(messages))
	for _, m := range messages {
		if strings.TrimSpace(m.UserText) != "" {
			fmt.Fprintf(&b, "\n---\n\n**You** · %s\n\n%s\n", m.CreatedAt.UTC().Format("2006-01-02 15:04"), strings.TrimSpace(m.UserText))
		}
		if strings.TrimSpace(m.AIText) != "" {
			suffix := ""
			if m.StreamingState == "partial" {
				// A truncated answer exported as if complete would misquote
				// the model; the marker keeps the file honest.
				suffix = "\n\n> (answer was interrupted before completion)"
			}
			fmt.Fprintf(&b, "\n**WorkMax**\n\n%s%s\n", strings.TrimSpace(m.AIText), suffix)
		}
	}
	return b.String()
}
