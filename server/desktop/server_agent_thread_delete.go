//go:build desktop

package desktop

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"gorm.io/gorm"
)

// Deleting a conversation, on a machine the user owns.
//
// A local-first product that can create data but not destroy it is making a
// promise it cannot keep: "your data stays on your machine" has to include
// "and leaves it when you say so". Until this route existed there was no way
// to delete a thread at all, and the pieces that clean up after one —
// RemoveFile and RemoveTurn on the knowledge index — were written and never
// called, so file bytes and RAG chunks would have outlived any deletion that
// was bolted on later.
//
// Scope: local-only threads (cloud_sync_state='local'). A synced thread has a
// cloud copy and a sync worker that would pull it straight back, so deleting
// it here would look like a delete that silently undoes itself. Refusing with
// a clear error is honest; deleting the cloud copy is Plus-side semantics
// this sidecar does not own.

// deleteAgentThreadResponse reports what was actually removed, so the caller
// (and a support transcript) can tell a clean delete from a partial one.
type deleteAgentThreadResponse struct {
	Deleted       bool  `json:"deleted"`
	Messages      int64 `json:"messages"`
	Files         int   `json:"files"`
	TurnIntents   int64 `json:"turn_intents"`
	IndexCleanups int   `json:"index_cleanups"`
}

func (s *Server) handleDeleteAgentThread(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_delete_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	// The same identity resolution as a turn: a signed-in user deletes their
	// own rows, a signed-out local-route user deletes the local single user's.
	uid, _, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}

	var (
		threadID  uint64
		syncState string
	)
	row := s.cfg.DB.Raw(
		`SELECT id, COALESCE(cloud_sync_state,'synced') FROM w_workagent_thread
		  WHERE uuid = ? AND uid = ? AND agent_type = 'general_agent'`,
		threadUUID, uid,
	).Row()
	if err := row.Scan(&threadID, &syncState); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "thread_delete_failed"})
		return
	}
	if syncState != "local" {
		// Not a permission problem — a semantics one. The renderer uses this
		// code to explain why the affordance is absent for synced threads.
		c.JSON(http.StatusConflict, gin.H{"error": "thread_synced"})
		return
	}

	// A turn that is still running would write its answer into rows this
	// handler is about to remove, resurrecting the thread as a half-deleted
	// ghost. The intent table knows about every local turn in flight.
	var running int64
	if err := s.cfg.DB.Raw(
		`SELECT COUNT(*) FROM w_desktop_agent_turn_intent
		  WHERE uid = ? AND thread_uuid = ? AND state IN ('starting','streaming')`,
		uid, threadUUID,
	).Row().Scan(&running); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "thread_delete_failed"})
		return
	}
	if running > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "thread_busy"})
		return
	}

	purge, err := s.purgeLocalThread(uid, threadID, threadUUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "thread_delete_failed"})
		return
	}

	c.JSON(http.StatusOK, deleteAgentThreadResponse{
		Deleted:       true,
		Messages:      purge.messages,
		Files:         purge.files,
		TurnIntents:   purge.intents,
		IndexCleanups: purge.indexCleanups,
	})
}

// threadPurgeResult reports what one thread's cascade actually removed.
type threadPurgeResult struct {
	messages      int64
	intents       int64
	files         int
	indexCleanups int
}

// purgeLocalThread removes everything one thread owns: file rows and bytes,
// conversational rows (atomically), the L2 agent workspace directory, and the
// RAG chunks. Shared by thread delete and local-account delete — the cascade
// must be one piece of code, or the two paths will rot apart.
//
// The caller has already verified ownership and that no turn is running.
func (s *Server) purgeLocalThread(uid uint64, threadID uint64, threadUUID string) (threadPurgeResult, error) {
	var purge threadPurgeResult

	// Collected before the rows go away: the knowledge index is keyed by turn
	// UUID and file id, and after the transaction there is nothing left to ask.
	turnUUIDs, err := listThreadTurnUUIDs(s.cfg.DB, uid, threadUUID)
	if err != nil {
		return purge, err
	}

	// Files first (rows + bytes), because the store owns both halves and
	// reports the ids the knowledge cleanup below needs.
	var fileIDs []int64
	if s.cfg.LocalFiles != nil {
		fileIDs, err = s.cfg.LocalFiles.DeleteThreadFiles(uid, threadID, threadUUID)
		if err != nil {
			return purge, err
		}
	}
	purge.files = len(fileIDs)

	// The conversational rows, atomically: a thread must not survive its
	// messages or vice versa.
	txErr := s.cfg.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`DELETE FROM w_workagent_message WHERE uid = ? AND thread_id = ?`, uid, threadID)
		if res.Error != nil {
			return res.Error
		}
		purge.messages = res.RowsAffected
		res = tx.Exec(`DELETE FROM w_desktop_agent_turn_intent WHERE uid = ? AND thread_uuid = ?`, uid, threadUUID)
		if res.Error != nil {
			return res.Error
		}
		purge.intents = res.RowsAffected
		return tx.Exec(`DELETE FROM w_workagent_thread WHERE uid = ? AND id = ?`, uid, threadID).Error
	})
	if txErr != nil {
		return purge, txErr
	}

	// The L2 workspace directory: files the tool loop produced. Until this
	// call they outlived every deletion — "your data leaves when you say so"
	// has to include what the agent wrote, not only what you uploaded. The
	// uuid is canonical v4 (validated at every entry point), so the joined
	// path cannot point anywhere but this thread's own directory.
	if s.cfg.DataDir != "" {
		dir := filepath.Join(s.cfg.DataDir, "agent_workspace", "thread_"+threadUUID)
		if err := os.RemoveAll(dir); err != nil {
			// Best-effort like the knowledge cleanup: the rows are gone, and
			// a directory that resists deletion is logged residue.
			log.Printf("workspace: remove %s after thread delete: %v", dir, err)
		}
	}

	// Knowledge cleanup last and best-effort: the user asked for the thread to
	// be gone, and it is. Chunks that fail to delete are logged residue, not a
	// reason to report the deletion as failed — the rows they described no
	// longer exist, so they can never again be attributed to anything.
	if s.cfg.KnowledgeIndex != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, fileID := range fileIDs {
			if _, err := s.cfg.KnowledgeIndex.RemoveFile(ctx, fileID); err != nil {
				log.Printf("knowledge: remove file %d after thread delete: %v", fileID, err)
				continue
			}
			purge.indexCleanups++
		}
		for _, turnUUID := range turnUUIDs {
			if _, err := s.cfg.KnowledgeIndex.RemoveTurn(ctx, turnUUID); err != nil {
				log.Printf("knowledge: remove turn %s after thread delete: %v", turnUUID, err)
				continue
			}
			purge.indexCleanups++
		}
	}
	return purge, nil
}

// listThreadTurnUUIDs returns every turn the thread has ever run, from the
// intent table — the same key IndexTurn used when the chunks were written.
func listThreadTurnUUIDs(db *gorm.DB, uid uint64, threadUUID string) ([]string, error) {
	rows, err := db.Raw(
		`SELECT turn_uuid FROM w_desktop_agent_turn_intent
		  WHERE uid = ? AND thread_uuid = ?`,
		uid, threadUUID,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uuid string
		if err := rows.Scan(&uuid); err != nil {
			return nil, err
		}
		out = append(out, uuid)
	}
	return out, rows.Err()
}
