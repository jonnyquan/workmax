package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model/common/response"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	adminKnowledgeDefaultPageSize = 20
	adminKnowledgeMaxPageSize     = 100
	adminKnowledgeMaxTextBytes    = 2 * 1024 * 1024
	adminKnowledgeMaxUploadBytes  = 8 * 1024 * 1024
)

type AdminWorkAgentKnowledgeApi struct{}

type adminKnowledgeDocumentRequest struct {
	Title        string `json:"title"`
	SourceType   string `json:"sourceType"`
	SourceURI    string `json:"sourceUri"`
	MimeType     string `json:"mimeType"`
	ContentText  string `json:"contentText"`
	MetadataJSON string `json:"metadataJson"`
	ScopeType    string `json:"scopeType"`
	ScopeID      uint   `json:"scopeId"`
	AgentMode    string `json:"agentMode"`
	Status       string `json:"status"`
}

type adminKnowledgeIndexRequest struct {
	VectorBackend string `json:"vectorBackend"`
	MetadataJSON  string `json:"metadataJson"`
}

type adminKnowledgeBatchIndexRequest struct {
	IDs           []uint `json:"ids"`
	VectorBackend string `json:"vectorBackend"`
	MetadataJSON  string `json:"metadataJson"`
}

type adminKnowledgeReviewRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

type adminKnowledgeBatchReviewRequest struct {
	IDs      []uint `json:"ids"`
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// ListKnowledgeDocuments handles GET /api/admin/workagent/knowledge-documents.
func (a *AdminWorkAgentKnowledgeApi) ListKnowledgeDocuments(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}

	pageIndex, pageSize := adminKnowledgePagination(c)
	query := db.Model(&workagentModel.KnowledgeDocument{}).Order("id DESC")
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if indexStatus := strings.TrimSpace(c.Query("indexStatus")); indexStatus != "" {
		query = query.Where("index_status = ?", indexStatus)
	}
	if scopeType := strings.TrimSpace(c.Query("scopeType")); scopeType != "" {
		normalizedScopeType, err := normalizeKnowledgeScopeType(scopeType)
		if err != nil {
			response.FailWithMessage(err.Error(), c)
			return
		}
		scopeType = normalizedScopeType
		query = query.Where("scope_type = ?", scopeType)
	}
	if agentMode := strings.TrimSpace(c.Query("agentMode")); agentMode != "" {
		query = query.Where("agent_mode = ?", agentMode)
	}
	if reviewStatus := strings.TrimSpace(c.Query("reviewStatus")); reviewStatus != "" {
		query = query.Where("review_status = ?", reviewStatus)
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		like := "%" + q + "%"
		query = query.Where("title LIKE ? OR source_uri LIKE ?", like, like)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.FailWithMessage("failed to count knowledge documents", c)
		return
	}
	var docs []workagentModel.KnowledgeDocument
	if err := query.Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&docs).Error; err != nil {
		response.FailWithMessage("failed to list knowledge documents", c)
		return
	}
	response.OkWithData(gin.H{
		"list":      docs,
		"total":     total,
		"pageIndex": pageIndex,
		"pageSize":  pageSize,
	}, c)
}

// CreateKnowledgeDocument handles POST /api/admin/workagent/knowledge-documents.
func (a *AdminWorkAgentKnowledgeApi) CreateKnowledgeDocument(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}

	var req adminKnowledgeDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("invalid request body", c)
		return
	}
	doc, err := buildKnowledgeDocument(req, int(utils.GetUserID(c)))
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if err := db.Create(&doc).Error; err != nil {
		response.FailWithMessage("failed to create knowledge document", c)
		return
	}
	response.OkWithData(doc, c)
}

// UpdateKnowledgeDocument handles PUT /api/admin/workagent/knowledge-documents/:id.
func (a *AdminWorkAgentKnowledgeApi) UpdateKnowledgeDocument(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.FailWithMessage("invalid document id", c)
		return
	}
	var req adminKnowledgeDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("invalid request body", c)
		return
	}
	next, err := buildKnowledgeDocument(req, int(utils.GetUserID(c)))
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	var updated workagentModel.KnowledgeDocument
	err = db.Transaction(func(tx *gorm.DB) error {
		var current workagentModel.KnowledgeDocument
		if err := tx.First(&current, uint(id)).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ?", current.Id).Delete(&workagentModel.KnowledgeChunk{}).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{
			"title":         next.Title,
			"source_type":   next.SourceType,
			"source_uri":    next.SourceURI,
			"mime_type":     next.MimeType,
			"content_hash":  next.ContentHash,
			"content_text":  next.ContentText,
			"metadata_json": next.MetadataJSON,
			"scope_type":    next.ScopeType,
			"scope_id":      next.ScopeID,
			"agent_mode":    next.AgentMode,
			"status":        next.Status,
			"index_status":  workagentModel.KnowledgeIndexStatusPending,
			"chunk_count":   0,
			"token_count":   next.TokenCount,
			"indexed_at":    nil,
			"review_status": workagentModel.KnowledgeReviewStatusPending,
			"reviewed_by":   0,
			"reviewed_at":   nil,
			"review_note":   "",
			"updated_by":    int(utils.GetUserID(c)),
		}
		if err := tx.Model(&current).Updates(updates).Error; err != nil {
			return err
		}
		return tx.First(&updated, current.Id).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, response.Response{Code: response.ERROR, Message: "knowledge document not found", Data: gin.H{}})
			return
		}
		response.FailWithMessage("failed to update knowledge document", c)
		return
	}
	response.OkWithData(updated, c)
}

// UploadKnowledgeDocument handles multipart uploads and normalizes supported
// text-bearing files into the same document model as manual text sources.
func (a *AdminWorkAgentKnowledgeApi) UploadKnowledgeDocument(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.FailWithMessage("file is required", c)
		return
	}
	if fileHeader.Size > adminKnowledgeMaxUploadBytes {
		response.FailWithMessage("file is too large", c)
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		response.FailWithMessage("failed to open upload", c)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, adminKnowledgeMaxUploadBytes+1))
	if err != nil || len(data) > adminKnowledgeMaxUploadBytes {
		response.FailWithMessage("failed to read upload", c)
		return
	}

	normalized, err := workagentService.NormalizeKnowledgeFile(fileHeader.Filename, fileHeader.Header.Get("Content-Type"), data)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	req := adminKnowledgeDocumentRequest{
		Title:        strings.TrimSpace(c.PostForm("title")),
		SourceType:   "upload",
		SourceURI:    strings.TrimSpace(c.PostForm("sourceUri")),
		MimeType:     normalized.MimeType,
		ContentText:  normalized.Text,
		MetadataJSON: strings.TrimSpace(c.PostForm("metadataJson")),
		ScopeType:    strings.TrimSpace(c.PostForm("scopeType")),
		AgentMode:    strings.TrimSpace(c.PostForm("agentMode")),
		Status:       strings.TrimSpace(c.PostForm("status")),
	}
	if scopeIDRaw := strings.TrimSpace(c.PostForm("scopeId")); scopeIDRaw != "" {
		if parsed, err := strconv.ParseUint(scopeIDRaw, 10, 64); err == nil {
			req.ScopeID = uint(parsed)
		}
	}
	if req.Title == "" {
		req.Title = fileHeader.Filename
	}
	if req.SourceURI == "" {
		req.SourceURI = fileHeader.Filename
	}
	doc, err := buildKnowledgeDocument(req, int(utils.GetUserID(c)))
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if err := db.Create(&doc).Error; err != nil {
		response.FailWithMessage("failed to create knowledge document", c)
		return
	}
	response.OkWithData(doc, c)
}

// ReviewKnowledgeDocument handles POST /api/admin/workagent/knowledge-documents/:id/review.
func (a *AdminWorkAgentKnowledgeApi) ReviewKnowledgeDocument(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.FailWithMessage("invalid document id", c)
		return
	}
	var req adminKnowledgeReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("invalid request body", c)
		return
	}
	decision := strings.TrimSpace(req.Decision)
	if decision != workagentModel.KnowledgeReviewStatusApproved && decision != workagentModel.KnowledgeReviewStatusRejected {
		response.FailWithMessage("decision must be approved or rejected", c)
		return
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > 512 {
		response.FailWithMessage("note is too long", c)
		return
	}
	var doc workagentModel.KnowledgeDocument
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&doc, uint(id)).Error; err != nil {
			return err
		}
		now := time.Now()
		status := workagentModel.KnowledgeDocumentStatusArchived
		if decision == workagentModel.KnowledgeReviewStatusApproved {
			status = workagentModel.KnowledgeDocumentStatusActive
		}
		if err := tx.Model(&doc).Updates(map[string]interface{}{
			"review_status": decision,
			"reviewed_by":   int(utils.GetUserID(c)),
			"reviewed_at":   now,
			"review_note":   note,
			"updated_by":    int(utils.GetUserID(c)),
			"status":        status,
		}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, response.Response{Code: response.ERROR, Message: "knowledge document not found", Data: gin.H{}})
			return
		}
		response.FailWithMessage("failed to review knowledge document", c)
		return
	}
	_ = db.First(&doc, uint(id)).Error
	response.OkWithData(doc, c)
}

// BatchReviewKnowledgeDocuments handles POST /api/admin/workagent/knowledge-documents/review-batch.
func (a *AdminWorkAgentKnowledgeApi) BatchReviewKnowledgeDocuments(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	var req adminKnowledgeBatchReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("invalid request body", c)
		return
	}
	ids := dedupeKnowledgeReviewIDs(req.IDs)
	if len(ids) == 0 {
		response.FailWithMessage("ids are required", c)
		return
	}
	if len(ids) > 100 {
		response.FailWithMessage("ids exceed batch limit", c)
		return
	}
	decision := strings.TrimSpace(req.Decision)
	if decision != workagentModel.KnowledgeReviewStatusApproved && decision != workagentModel.KnowledgeReviewStatusRejected {
		response.FailWithMessage("decision must be approved or rejected", c)
		return
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > 512 {
		response.FailWithMessage("note is too long", c)
		return
	}

	reviewedBy := int(utils.GetUserID(c))
	var reviewedCount int
	err := db.Transaction(func(tx *gorm.DB) error {
		var docs []workagentModel.KnowledgeDocument
		if err := tx.Where("id IN ?", ids).Find(&docs).Error; err != nil {
			return err
		}
		if len(docs) != len(ids) {
			return gorm.ErrRecordNotFound
		}
		status := workagentModel.KnowledgeDocumentStatusArchived
		if decision == workagentModel.KnowledgeReviewStatusApproved {
			status = workagentModel.KnowledgeDocumentStatusActive
		}
		now := time.Now()
		for _, doc := range docs {
			if err := tx.Model(&workagentModel.KnowledgeDocument{}).Where("id = ?", doc.Id).Updates(map[string]interface{}{
				"review_status": decision,
				"reviewed_by":   reviewedBy,
				"reviewed_at":   now,
				"review_note":   note,
				"updated_by":    reviewedBy,
				"status":        status,
			}).Error; err != nil {
				return err
			}
			reviewedCount++
		}
		return nil
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, response.Response{Code: response.ERROR, Message: "one or more knowledge documents were not found", Data: gin.H{}})
			return
		}
		response.FailWithMessage("failed to batch review knowledge documents", c)
		return
	}
	response.OkWithData(gin.H{"reviewed": reviewedCount}, c)
}

// ListKnowledgeIndexJobs handles GET /api/admin/workagent/knowledge-documents/index-jobs.
func (a *AdminWorkAgentKnowledgeApi) ListKnowledgeIndexJobs(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	pageIndex, pageSize := adminKnowledgePagination(c)
	query := db.Model(&workagentModel.KnowledgeIndexJob{}).Order("id DESC")
	if documentID, err := strconv.ParseUint(strings.TrimSpace(c.Query("documentId")), 10, 64); err == nil && documentID > 0 {
		query = query.Where("document_id = ?", uint(documentID))
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if vectorBackend := strings.TrimSpace(c.Query("vectorBackend")); vectorBackend != "" {
		query = query.Where("vector_backend = ?", vectorBackend)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.FailWithMessage("failed to count knowledge index jobs", c)
		return
	}
	var jobs []workagentModel.KnowledgeIndexJob
	if err := query.Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&jobs).Error; err != nil {
		response.FailWithMessage("failed to list knowledge index jobs", c)
		return
	}
	response.OkWithData(gin.H{
		"list":      jobs,
		"total":     total,
		"pageIndex": pageIndex,
		"pageSize":  pageSize,
	}, c)
}

// RetryKnowledgeIndexJob handles POST /api/admin/workagent/knowledge-documents/index-jobs/:jobId/retry.
func (a *AdminWorkAgentKnowledgeApi) RetryKnowledgeIndexJob(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	jobID, err := strconv.ParseUint(c.Param("jobId"), 10, 64)
	if err != nil || jobID == 0 {
		response.FailWithMessage("invalid job id", c)
		return
	}
	var retryJob workagentModel.KnowledgeIndexJob
	err = db.Transaction(func(tx *gorm.DB) error {
		var current workagentModel.KnowledgeIndexJob
		if err := tx.First(&current, uint(jobID)).Error; err != nil {
			return err
		}
		if current.Status != workagentModel.KnowledgeIndexJobStatusFailed {
			return fmt.Errorf("only failed index jobs can be retried")
		}
		var doc workagentModel.KnowledgeDocument
		if err := tx.First(&doc, current.DocumentID).Error; err != nil {
			return err
		}
		retryJob = workagentModel.KnowledgeIndexJob{
			DocumentID:    doc.Id,
			Status:        workagentModel.KnowledgeIndexJobStatusQueued,
			VectorBackend: current.VectorBackend,
			RequestedBy:   int(utils.GetUserID(c)),
			MetadataJSON:  current.MetadataJSON,
		}
		if err := tx.Create(&retryJob).Error; err != nil {
			return err
		}
		return tx.Model(&doc).Updates(map[string]interface{}{
			"index_status": workagentModel.KnowledgeIndexStatusPending,
			"updated_by":   int(utils.GetUserID(c)),
		}).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, response.Response{Code: response.ERROR, Message: "knowledge index job not found", Data: gin.H{}})
			return
		}
		response.FailWithMessage(err.Error(), c)
		return
	}
	if err := workagentService.RunKnowledgeIndexJob(db, retryJob.Id); err != nil {
		response.FailWithMessage("failed to retry knowledge index job: "+err.Error(), c)
		return
	}
	var refreshed workagentModel.KnowledgeIndexJob
	_ = db.First(&refreshed, retryJob.Id).Error
	response.OkWithData(refreshed, c)
}

// BatchReindexKnowledgeDocuments handles POST /api/admin/workagent/knowledge-documents/reindex-batch.
func (a *AdminWorkAgentKnowledgeApi) BatchReindexKnowledgeDocuments(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	var req adminKnowledgeBatchIndexRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("invalid request body", c)
		return
	}
	ids := dedupeKnowledgeReviewIDs(req.IDs)
	if len(ids) == 0 {
		response.FailWithMessage("ids are required", c)
		return
	}
	if len(ids) > 100 {
		response.FailWithMessage("ids exceed batch limit", c)
		return
	}
	metadata := strings.TrimSpace(req.MetadataJSON)
	if metadata != "" && !json.Valid([]byte(metadata)) {
		response.FailWithMessage("metadataJson must be valid JSON", c)
		return
	}

	requestedBy := int(utils.GetUserID(c))
	jobs := make([]workagentModel.KnowledgeIndexJob, 0, len(ids))
	err := db.Transaction(func(tx *gorm.DB) error {
		var docs []workagentModel.KnowledgeDocument
		if err := tx.Where("id IN ?", ids).Find(&docs).Error; err != nil {
			return err
		}
		if len(docs) != len(ids) {
			return gorm.ErrRecordNotFound
		}
		docsByID := map[uint]workagentModel.KnowledgeDocument{}
		for _, doc := range docs {
			docsByID[doc.Id] = doc
		}
		for _, id := range ids {
			doc := docsByID[id]
			job := workagentModel.KnowledgeIndexJob{
				DocumentID:    doc.Id,
				Status:        workagentModel.KnowledgeIndexJobStatusQueued,
				VectorBackend: strings.TrimSpace(req.VectorBackend),
				RequestedBy:   requestedBy,
				MetadataJSON:  metadata,
			}
			if err := tx.Create(&job).Error; err != nil {
				return err
			}
			if err := tx.Model(&workagentModel.KnowledgeDocument{}).Where("id = ?", doc.Id).Updates(map[string]interface{}{
				"index_status": workagentModel.KnowledgeIndexStatusPending,
				"updated_by":   requestedBy,
			}).Error; err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return nil
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, response.Response{Code: response.ERROR, Message: "one or more knowledge documents were not found", Data: gin.H{}})
			return
		}
		response.FailWithMessage("failed to enqueue knowledge document reindex batch", c)
		return
	}

	type failedIndexJob struct {
		DocumentID uint   `json:"documentId"`
		JobID      uint   `json:"jobId"`
		Error      string `json:"error"`
	}
	failed := make([]failedIndexJob, 0)
	indexed := 0
	for _, job := range jobs {
		if err := workagentService.RunKnowledgeIndexJob(db, job.Id); err != nil {
			failed = append(failed, failedIndexJob{DocumentID: job.DocumentID, JobID: job.Id, Error: err.Error()})
			continue
		}
		indexed++
	}
	response.OkWithData(gin.H{
		"requested": len(jobs),
		"indexed":   indexed,
		"failed":    failed,
	}, c)
}

// ReindexKnowledgeDocument handles POST /api/admin/workagent/knowledge-documents/:id/reindex.
func (a *AdminWorkAgentKnowledgeApi) ReindexKnowledgeDocument(c *gin.Context) {
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithMessage("database is not configured", c)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.FailWithMessage("invalid document id", c)
		return
	}
	var req adminKnowledgeIndexRequest
	if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
		response.FailWithMessage("invalid request body", c)
		return
	}
	metadata := strings.TrimSpace(req.MetadataJSON)
	if metadata != "" && !json.Valid([]byte(metadata)) {
		response.FailWithMessage("metadataJson must be valid JSON", c)
		return
	}

	var doc workagentModel.KnowledgeDocument
	var job workagentModel.KnowledgeIndexJob
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&doc, uint(id)).Error; err != nil {
			return err
		}
		job = workagentModel.KnowledgeIndexJob{
			DocumentID:    doc.Id,
			Status:        workagentModel.KnowledgeIndexJobStatusQueued,
			VectorBackend: strings.TrimSpace(req.VectorBackend),
			RequestedBy:   int(utils.GetUserID(c)),
			MetadataJSON:  metadata,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		return tx.Model(&doc).Updates(map[string]interface{}{
			"index_status": workagentModel.KnowledgeIndexStatusPending,
			"updated_by":   int(utils.GetUserID(c)),
		}).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, response.Response{Code: response.ERROR, Message: "knowledge document not found", Data: gin.H{}})
			return
		}
		response.FailWithMessage("failed to enqueue knowledge document reindex", c)
		return
	}
	if err := workagentService.RunKnowledgeIndexJob(db, job.Id); err != nil {
		response.FailWithMessage("failed to index knowledge document: "+err.Error(), c)
		return
	}
	var refreshed workagentModel.KnowledgeDocument
	_ = db.First(&refreshed, doc.Id).Error
	response.OkWithData(gin.H{
		"documentId":  doc.Id,
		"jobId":       job.Id,
		"indexStatus": refreshed.IndexStatus,
		"chunkCount":  refreshed.ChunkCount,
	}, c)
}

func buildKnowledgeDocument(req adminKnowledgeDocumentRequest, adminID int) (workagentModel.KnowledgeDocument, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return workagentModel.KnowledgeDocument{}, fmt.Errorf("title is required")
	}
	content := strings.TrimSpace(req.ContentText)
	if content == "" {
		return workagentModel.KnowledgeDocument{}, fmt.Errorf("contentText is required")
	}
	if len([]byte(content)) > adminKnowledgeMaxTextBytes {
		return workagentModel.KnowledgeDocument{}, fmt.Errorf("contentText is too large")
	}
	metadata := strings.TrimSpace(req.MetadataJSON)
	if metadata != "" && !json.Valid([]byte(metadata)) {
		return workagentModel.KnowledgeDocument{}, fmt.Errorf("metadataJson must be valid JSON")
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = workagentModel.KnowledgeDocumentStatusActive
	}
	if status != workagentModel.KnowledgeDocumentStatusActive && status != workagentModel.KnowledgeDocumentStatusArchived {
		return workagentModel.KnowledgeDocument{}, fmt.Errorf("status must be active or archived")
	}
	sourceType := strings.TrimSpace(req.SourceType)
	if sourceType == "" {
		sourceType = "manual"
	}
	scopeType, err := normalizeKnowledgeScopeType(req.ScopeType)
	if err != nil {
		return workagentModel.KnowledgeDocument{}, err
	}
	if scopeType == "global" {
		req.ScopeID = 0
	}
	mimeType := strings.TrimSpace(req.MimeType)
	if mimeType == "" {
		mimeType = "text/plain"
	}
	sum := sha256.Sum256([]byte(content))
	return workagentModel.KnowledgeDocument{
		Title:        title,
		SourceType:   sourceType,
		SourceURI:    strings.TrimSpace(req.SourceURI),
		MimeType:     mimeType,
		ContentHash:  hex.EncodeToString(sum[:]),
		ContentText:  content,
		MetadataJSON: metadata,
		ScopeType:    scopeType,
		ScopeID:      req.ScopeID,
		AgentMode:    strings.TrimSpace(req.AgentMode),
		ReviewStatus: workagentModel.KnowledgeReviewStatusPending,
		Status:       status,
		IndexStatus:  workagentModel.KnowledgeIndexStatusPending,
		TokenCount:   estimateKnowledgeTokenCount(content),
		UpdatedBy:    adminID,
	}, nil
}

func normalizeKnowledgeScopeType(value string) (string, error) {
	scopeType := strings.TrimSpace(value)
	if scopeType == "" {
		return "global", nil
	}
	switch scopeType {
	case "global", "project", "team":
		return scopeType, nil
	default:
		return "", fmt.Errorf("scopeType must be global, project, or team")
	}
}

func estimateKnowledgeTokenCount(content string) int {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0
	}
	return len(fields)
}

func adminKnowledgePagination(c *gin.Context) (int, int) {
	pageIndex, _ := strconv.Atoi(c.DefaultQuery("pageIndex", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", strconv.Itoa(adminKnowledgeDefaultPageSize)))
	if pageIndex < 1 {
		pageIndex = 1
	}
	if pageSize < 1 {
		pageSize = adminKnowledgeDefaultPageSize
	}
	if pageSize > adminKnowledgeMaxPageSize {
		pageSize = adminKnowledgeMaxPageSize
	}
	return pageIndex, pageSize
}

func dedupeKnowledgeReviewIDs(ids []uint) []uint {
	seen := map[uint]bool{}
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
