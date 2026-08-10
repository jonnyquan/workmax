//go:build desktop

package desktop

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	localrender "server/desktop/local_render"
)

// threadFileUploadResponse is echoed to the renderer after a successful upload;
// the renderer keeps file_id and sends it back in /agent/chat payload.files.
type threadFileUploadResponse struct {
	FileID   int64  `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
}

// handleUploadThreadFile handles POST /agent/threads/:uuid/files (multipart).
// The renderer uploads a file out-of-band (independent of /agent/chat); the
// returned file_id is later referenced in /agent/chat payload.files so the L1
// engine can load the attachment as model context (L3b-2). Upload and turn are
// decoupled so the strict /agent/chat decoder is untouched and a large file
// never inflates the chat body.
func (s *Server) handleUploadThreadFile(c *gin.Context) {
	if s.cfg.LocalFiles == nil || s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "file_upload_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	// The file lands on this disk under this thread's owner. That owner always
	// exists — a signed-out machine has a local account — so the only refusal
	// here is a session we cannot bind at all.
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	uid := identity.UID

	// Resolve the local thread row id (the thread must already exist — created
	// locally via L3a or synced from cloud).
	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_file"})
		return
	}
	src, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_file"})
		return
	}
	defer src.Close()

	saved, err := s.cfg.LocalFiles.SaveThreadFile(uid, threadID, threadUUID, fileHeader.Filename, src)
	if err != nil {
		respondThreadFileUploadError(c, err)
		return
	}
	// L3c-3: kick off knowledge indexing asynchronously so the upload response
	// is not blocked by extraction + embedding. Nil when RAG is disabled.
	if s.cfg.KnowledgeIndex != nil {
		go s.indexUploadedFile(uid, saved.FileID)
	}
	c.JSON(http.StatusCreated, threadFileUploadResponse{
		FileID:   saved.FileID,
		FileName: saved.FileName,
		MimeType: saved.MimeType,
		FileType: saved.FileType,
		FileSize: saved.FileSize,
	})
}

// respondThreadFileUploadError maps local_render store errors to renderer-facing
// status codes. Size violations are client-fault 413; unsupported types are 415;
// anything else is a 500 (disk/DB failure).
func respondThreadFileUploadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, localrender.ErrUploadTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file_too_large"})
	case errors.Is(err, localrender.ErrUnsupportedFileType):
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported_file_type"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "file_upload_failed"})
	}
}

// indexUploadedFile runs L3c-3 knowledge indexing for a freshly saved file
// asynchronously. Failures are logged only: a file that fails to index simply
// is not retrievable via RAG until it is re-uploaded or re-indexed — it does
// not affect the upload itself, which has already succeeded. The recover is
// load-bearing: this runs in a bare goroutine, so a panic in the extraction or
// embedding stack would otherwise crash the whole sidecar.
func (s *Server) indexUploadedFile(uid uint64, fileID int64) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("knowledge: index file %d (uid %d) panicked: %v", fileID, uid, r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.cfg.KnowledgeIndex.IndexFile(ctx, uid, fileID); err != nil {
		log.Printf("knowledge: index file %d (uid %d): %v", fileID, uid, err)
	}
}

// threadFileListResponse mirrors the shape of the other local-history list
// routes: a bounded items array and its count, so the renderer never has to
// guess whether an empty list means "none" or "not loaded".
type threadFileListResponse struct {
	Items []localrender.ThreadFile `json:"items"`
	Count int                      `json:"count"`
}

// handleListThreadFiles handles GET /agent/threads/:uuid/files.
//
// The upload route has always persisted these rows, but nothing could read
// them back: the renderer knew only about files it had uploaded during the
// current session, so reopening a thread showed no attachments even though
// they were still on disk and still usable as model context. This is what the
// right-hand Sources panel reads.
func (s *Server) handleListThreadFiles(c *gin.Context) {
	if s.cfg.LocalFiles == nil || s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "file_list_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	uid := identity.UID

	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}

	files, err := s.cfg.LocalFiles.ListThreadFiles(uid, threadID)
	if err != nil {
		log.Printf("list thread files: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "file_list_failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, threadFileListResponse{Items: files, Count: len(files)})
}
