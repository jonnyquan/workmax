package admin

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"server/globals"
	workagentModel "server/model/workagent"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBuildKnowledgeDocumentValidation(t *testing.T) {
	if _, err := buildKnowledgeDocument(adminKnowledgeDocumentRequest{ContentText: "body"}, 7); err == nil {
		t.Fatal("expected missing title validation error")
	}
	if _, err := buildKnowledgeDocument(adminKnowledgeDocumentRequest{Title: "Doc"}, 7); err == nil {
		t.Fatal("expected missing contentText validation error")
	}
	if _, err := buildKnowledgeDocument(adminKnowledgeDocumentRequest{Title: "Doc", ContentText: "body", MetadataJSON: "not-json"}, 7); err == nil {
		t.Fatal("expected invalid metadata validation error")
	}
	if _, err := buildKnowledgeDocument(adminKnowledgeDocumentRequest{Title: "Doc", ContentText: "body", ScopeType: "org", ScopeID: 99}, 7); err == nil || !strings.Contains(err.Error(), "scopeType must be global, project, or team") {
		t.Fatalf("expected unsupported org scope validation error, got %v", err)
	}

	doc, err := buildKnowledgeDocument(adminKnowledgeDocumentRequest{
		Title:        " Brand handbook ",
		ContentText:  " use source product references ",
		MetadataJSON: `{"audience":"work-agent"}`,
	}, 7)
	if err != nil {
		t.Fatalf("buildKnowledgeDocument: %v", err)
	}
	if doc.Title != "Brand handbook" {
		t.Fatalf("title = %q", doc.Title)
	}
	if doc.SourceType != "manual" || doc.MimeType != "text/plain" {
		t.Fatalf("defaults not applied: %+v", doc)
	}
	if doc.ContentHash == "" || doc.IndexStatus != workagentModel.KnowledgeIndexStatusPending {
		t.Fatalf("hash/index status not initialized: %+v", doc)
	}
	if doc.ReviewStatus != workagentModel.KnowledgeReviewStatusPending {
		t.Fatalf("review status = %q, want pending", doc.ReviewStatus)
	}
	if doc.UpdatedBy != 7 {
		t.Fatalf("updatedBy = %d", doc.UpdatedBy)
	}
	globalDoc, err := buildKnowledgeDocument(adminKnowledgeDocumentRequest{
		Title:       "Global Doc",
		ContentText: "global guidance",
		ScopeType:   "global",
		ScopeID:     99,
	}, 7)
	if err != nil {
		t.Fatalf("build global doc: %v", err)
	}
	if globalDoc.ScopeID != 0 {
		t.Fatalf("global scope id = %d, want reset to 0", globalDoc.ScopeID)
	}
}

func TestAdminWorkAgentKnowledgeApiCreateListReindex(t *testing.T) {
	db := newKnowledgeAPITestDB(t)
	restore := installKnowledgeAPITestDB(db)
	defer restore()

	gin.SetMode(gin.TestMode)
	api := AdminWorkAgentKnowledgeApi{}
	r := gin.New()
	r.GET("/knowledge-documents", api.ListKnowledgeDocuments)
	r.POST("/knowledge-documents", api.CreateKnowledgeDocument)
	r.POST("/knowledge-documents/upload", api.UploadKnowledgeDocument)
	r.POST("/knowledge-documents/review-batch", api.BatchReviewKnowledgeDocuments)
	r.POST("/knowledge-documents/reindex-batch", api.BatchReindexKnowledgeDocuments)
	r.GET("/knowledge-documents/index-jobs", api.ListKnowledgeIndexJobs)
	r.POST("/knowledge-documents/index-jobs/:jobId/retry", api.RetryKnowledgeIndexJob)
	r.PUT("/knowledge-documents/:id", api.UpdateKnowledgeDocument)
	r.POST("/knowledge-documents/:id/review", api.ReviewKnowledgeDocument)
	r.POST("/knowledge-documents/:id/reindex", api.ReindexKnowledgeDocument)

	createBody := map[string]any{
		"title":        "Design QA handbook",
		"contentText":  "Always cite source assets before claiming fidelity.",
		"metadataJson": `{"scope":"rag-prereq"}`,
	}
	w := performKnowledgeRequest(r, http.MethodPost, "/knowledge-documents", createBody)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var createResp struct {
		Code int                              `json:"code"`
		Data workagentModel.KnowledgeDocument `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.Code != 1 || createResp.Data.Id == 0 {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	if createResp.Data.ReviewStatus != workagentModel.KnowledgeReviewStatusPending {
		t.Fatalf("new doc review status = %q", createResp.Data.ReviewStatus)
	}

	w = performKnowledgeRequest(r, http.MethodPost, "/knowledge-documents/1/review", map[string]any{
		"decision": "approved",
		"note":     "ready for runtime retrieval",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("review status = %d body=%s", w.Code, w.Body.String())
	}
	var reviewResp struct {
		Code int                              `json:"code"`
		Data workagentModel.KnowledgeDocument `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reviewResp); err != nil {
		t.Fatalf("decode review response: %v", err)
	}
	if reviewResp.Data.ReviewStatus != workagentModel.KnowledgeReviewStatusApproved || reviewResp.Data.ReviewedAt == nil || reviewResp.Data.ReviewNote == "" {
		t.Fatalf("unexpected review response: %+v", reviewResp.Data)
	}
	w = performKnowledgeRequest(r, http.MethodGet, "/knowledge-documents?q=Design", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	var listResp struct {
		Code int `json:"code"`
		Data struct {
			Total int64                              `json:"total"`
			List  []workagentModel.KnowledgeDocument `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.List) != 1 {
		t.Fatalf("unexpected list response: %+v", listResp.Data)
	}

	w = performKnowledgeRequest(r, http.MethodGet, "/knowledge-documents?scopeType=org", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "scopeType must be global, project, or team") {
		t.Fatalf("unsupported scope filter response = %d body=%s", w.Code, w.Body.String())
	}

	w = performKnowledgeRequest(r, http.MethodPost, "/knowledge-documents/1/reindex", map[string]any{
		"vectorBackend": "pgvector",
		"metadataJson":  `{"reason":"manual"}`,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("reindex status = %d body=%s", w.Code, w.Body.String())
	}
	var jobCount int64
	if err := db.Model(&workagentModel.KnowledgeIndexJob{}).Count(&jobCount).Error; err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("job count = %d, want 1", jobCount)
	}
	var chunkCount int64
	if err := db.Model(&workagentModel.KnowledgeChunk{}).Count(&chunkCount).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunkCount != 1 {
		t.Fatalf("chunk count = %d, want 1", chunkCount)
	}
	var doc workagentModel.KnowledgeDocument
	if err := db.First(&doc, 1).Error; err != nil {
		t.Fatalf("load doc: %v", err)
	}
	if doc.IndexStatus != workagentModel.KnowledgeIndexStatusIndexed {
		t.Fatalf("index status = %q", doc.IndexStatus)
	}
	if doc.ChunkCount != 1 {
		t.Fatalf("doc chunk count = %d, want 1", doc.ChunkCount)
	}

	w = performKnowledgeUploadRequest(r, "/knowledge-documents/upload", "playbook.md", "# Playbook\nUse uploaded markdown as knowledge.")
	if w.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", w.Code, w.Body.String())
	}
	var uploadResp struct {
		Code int                              `json:"code"`
		Data workagentModel.KnowledgeDocument `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &uploadResp); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if uploadResp.Data.SourceType != "upload" || uploadResp.Data.MimeType != "text/markdown" || !strings.Contains(uploadResp.Data.ContentText, "uploaded markdown") {
		t.Fatalf("unexpected upload doc: %+v", uploadResp.Data)
	}

	w = performKnowledgeRequest(r, http.MethodPost, "/knowledge-documents/reindex-batch", map[string]any{
		"ids":          []uint{1, uploadResp.Data.Id, uploadResp.Data.Id},
		"metadataJson": `{"reason":"batch"}`,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("batch reindex status = %d body=%s", w.Code, w.Body.String())
	}
	var batchIndexResp struct {
		Code int `json:"code"`
		Data struct {
			Requested int `json:"requested"`
			Indexed   int `json:"indexed"`
			Failed    []struct {
				DocumentID uint   `json:"documentId"`
				JobID      uint   `json:"jobId"`
				Error      string `json:"error"`
			} `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &batchIndexResp); err != nil {
		t.Fatalf("decode batch reindex response: %v", err)
	}
	if batchIndexResp.Data.Requested != 2 || batchIndexResp.Data.Indexed != 2 || len(batchIndexResp.Data.Failed) != 0 {
		t.Fatalf("unexpected batch reindex response: %+v", batchIndexResp.Data)
	}
	if err := db.Model(&workagentModel.KnowledgeIndexJob{}).Count(&jobCount).Error; err != nil {
		t.Fatalf("count jobs after batch reindex: %v", err)
	}
	if jobCount != 3 {
		t.Fatalf("job count after batch reindex = %d, want 3", jobCount)
	}
	if err := db.Model(&workagentModel.KnowledgeChunk{}).Count(&chunkCount).Error; err != nil {
		t.Fatalf("count chunks after batch reindex: %v", err)
	}
	if chunkCount != 2 {
		t.Fatalf("chunk count after batch reindex = %d, want 2", chunkCount)
	}

	failedJob := workagentModel.KnowledgeIndexJob{
		DocumentID:   1,
		Status:       workagentModel.KnowledgeIndexJobStatusFailed,
		RequestedBy:  7,
		ErrorMessage: "previous worker failure",
		MetadataJSON: `{"reason":"retry"}`,
	}
	if err := db.Create(&failedJob).Error; err != nil {
		t.Fatalf("seed failed job: %v", err)
	}
	w = performKnowledgeRequest(r, http.MethodGet, "/knowledge-documents/index-jobs?documentId=1&status=failed", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list index jobs status = %d body=%s", w.Code, w.Body.String())
	}
	var jobsResp struct {
		Code int `json:"code"`
		Data struct {
			Total int64                              `json:"total"`
			List  []workagentModel.KnowledgeIndexJob `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &jobsResp); err != nil {
		t.Fatalf("decode index jobs response: %v", err)
	}
	if jobsResp.Data.Total != 1 || len(jobsResp.Data.List) != 1 || jobsResp.Data.List[0].Id != failedJob.Id {
		t.Fatalf("unexpected index jobs response: %+v", jobsResp.Data)
	}
	w = performKnowledgeRequest(r, http.MethodPost, "/knowledge-documents/index-jobs/"+strconv.FormatUint(uint64(failedJob.Id), 10)+"/retry", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("retry index job status = %d body=%s", w.Code, w.Body.String())
	}
	var retryResp struct {
		Code int                              `json:"code"`
		Data workagentModel.KnowledgeIndexJob `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &retryResp); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	if retryResp.Data.Id == failedJob.Id || retryResp.Data.Status != workagentModel.KnowledgeIndexJobStatusSucceeded || retryResp.Data.DocumentID != 1 {
		t.Fatalf("unexpected retry response: %+v", retryResp.Data)
	}

	w = performKnowledgeRequest(r, http.MethodPost, "/knowledge-documents/review-batch", map[string]any{
		"ids":      []uint{1, uploadResp.Data.Id, uploadResp.Data.Id},
		"decision": "rejected",
		"note":     "batch cleanup",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("batch review status = %d body=%s", w.Code, w.Body.String())
	}
	var batchResp struct {
		Code int `json:"code"`
		Data struct {
			Reviewed int `json:"reviewed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &batchResp); err != nil {
		t.Fatalf("decode batch review response: %v", err)
	}
	if batchResp.Data.Reviewed != 2 {
		t.Fatalf("batch reviewed = %d, want 2", batchResp.Data.Reviewed)
	}
	w = performKnowledgeRequest(r, http.MethodPut, "/knowledge-documents/1", map[string]any{
		"title":        "Design QA handbook v2",
		"contentText":  "Updated source fidelity guidance for Work Agent.",
		"metadataJson": `{"scope":"rag-prereq","version":2}`,
		"scopeType":    "project",
		"scopeId":      42,
		"agentMode":    "design",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	var updateResp struct {
		Code int                              `json:"code"`
		Data workagentModel.KnowledgeDocument `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp.Data.Title != "Design QA handbook v2" || updateResp.Data.ScopeType != "project" || updateResp.Data.ScopeID != 42 || updateResp.Data.AgentMode != "design" {
		t.Fatalf("unexpected updated document: %+v", updateResp.Data)
	}
	if updateResp.Data.IndexStatus != workagentModel.KnowledgeIndexStatusPending || updateResp.Data.ReviewStatus != workagentModel.KnowledgeReviewStatusPending || updateResp.Data.ChunkCount != 0 {
		t.Fatalf("update did not reset index/review state: %+v", updateResp.Data)
	}
	if err := db.Model(&workagentModel.KnowledgeChunk{}).Where("document_id = ?", uint(1)).Count(&chunkCount).Error; err != nil {
		t.Fatalf("count chunks after update: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("document 1 chunk count after update = %d, want 0", chunkCount)
	}

}

func newKnowledgeAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	for _, stmt := range []string{
		`CREATE TABLE w_workagent_knowledge_document (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			title TEXT NOT NULL,
			source_type TEXT NOT NULL DEFAULT 'manual',
			source_uri TEXT NOT NULL DEFAULT '',
			mime_type TEXT NOT NULL DEFAULT 'text/plain',
			content_hash TEXT NOT NULL DEFAULT '',
			content_text TEXT,
			metadata_json TEXT,
			scope_type TEXT NOT NULL DEFAULT 'global',
			scope_id INTEGER NOT NULL DEFAULT 0,
			agent_mode TEXT NOT NULL DEFAULT '',
			review_status TEXT NOT NULL DEFAULT 'approved',
			reviewed_by INTEGER NOT NULL DEFAULT 0,
			reviewed_at DATETIME,
			review_note TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			index_status TEXT NOT NULL DEFAULT 'pending',
			chunk_count INTEGER NOT NULL DEFAULT 0,
			token_count INTEGER NOT NULL DEFAULT 0,
			indexed_at DATETIME,
			updated_by INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE w_workagent_knowledge_index_job (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			document_id INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'queued',
			vector_backend TEXT NOT NULL DEFAULT '',
			requested_by INTEGER NOT NULL DEFAULT 0,
			started_at DATETIME,
			finished_at DATETIME,
			chunk_count INTEGER NOT NULL DEFAULT 0,
			error_message TEXT,
			metadata_json TEXT
		)`,
		`CREATE TABLE w_workagent_knowledge_chunk (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			document_id INTEGER NOT NULL,
			chunk_index INTEGER NOT NULL,
			content_text TEXT,
			content_hash TEXT NOT NULL DEFAULT '',
			token_count INTEGER NOT NULL DEFAULT 0,
			metadata_json TEXT
		)`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("install schema: %v", err)
		}
	}
	return db
}

func installKnowledgeAPITestDB(db *gorm.DB) func() {
	prev := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	return func() {
		globals.GraDBs = prev
	}
}

func performKnowledgeRequest(r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func performKnowledgeUploadRequest(r http.Handler, path string, filename string, content string) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("title", "")
	part, _ := writer.CreateFormFile("file", filename)
	_, _ = part.Write([]byte(content))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
