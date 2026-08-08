package workagent

import (
	"encoding/json"
	"strings"
	"testing"

	"server/globals"
	workagentModel "server/model/workagent"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBuildKnowledgeChunks_OverlapsAndCopiesMetadata(t *testing.T) {
	doc := workagentModel.KnowledgeDocument{
		GraMODEL:     globals.GraMODEL{Id: 9},
		ContentText:  "one two three four five six seven",
		MetadataJSON: `{"source":"unit"}`,
	}
	chunks := BuildKnowledgeChunks(doc, 3, 1)
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d, want 3", len(chunks))
	}
	if chunks[0].ContentText != "one two three" {
		t.Fatalf("chunk 0 = %q", chunks[0].ContentText)
	}
	if chunks[1].ContentText != "three four five" {
		t.Fatalf("chunk 1 = %q", chunks[1].ContentText)
	}
	if chunks[2].ContentText != "five six seven" {
		t.Fatalf("chunk 2 = %q", chunks[2].ContentText)
	}
	if chunks[0].DocumentID != 9 || chunks[0].MetadataJSON != doc.MetadataJSON || chunks[0].ContentHash == "" {
		t.Fatalf("chunk metadata not initialized: %+v", chunks[0])
	}
}

func TestRunKnowledgeIndexJob_MaterializesChunks(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	content := strings.Repeat("token ", 260)
	doc := workagentModel.KnowledgeDocument{
		Title:       "Index me",
		ContentText: strings.TrimSpace(content),
		IndexStatus: workagentModel.KnowledgeIndexStatusPending,
		Status:      workagentModel.KnowledgeDocumentStatusActive,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	job := workagentModel.KnowledgeIndexJob{
		DocumentID: doc.Id,
		Status:     workagentModel.KnowledgeIndexJobStatusQueued,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := RunKnowledgeIndexJob(db, job.Id); err != nil {
		t.Fatalf("RunKnowledgeIndexJob: %v", err)
	}
	var chunks []workagentModel.KnowledgeChunk
	if err := db.Order("chunk_index ASC").Find(&chunks).Error; err != nil {
		t.Fatalf("load chunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(chunks))
	}
	var updatedDoc workagentModel.KnowledgeDocument
	_ = db.First(&updatedDoc, doc.Id).Error
	if updatedDoc.IndexStatus != workagentModel.KnowledgeIndexStatusIndexed || updatedDoc.ChunkCount != 2 {
		t.Fatalf("updated doc = %+v", updatedDoc)
	}
	var updatedJob workagentModel.KnowledgeIndexJob
	_ = db.First(&updatedJob, job.Id).Error
	if updatedJob.Status != workagentModel.KnowledgeIndexJobStatusSucceeded || updatedJob.ChunkCount != 2 {
		t.Fatalf("updated job = %+v", updatedJob)
	}
}

func TestRunKnowledgeIndexJob_RecordsPGVectorFallbackOnlyMetadata(t *testing.T) {
	t.Setenv("WORKMAX_KNOWLEDGE_PGVECTOR_ENABLED", "")
	db := newKnowledgeIndexerTestDB(t)
	doc := workagentModel.KnowledgeDocument{
		Title:       "PGVector pending",
		ContentText: "design knowledge should still be indexed locally",
		IndexStatus: workagentModel.KnowledgeIndexStatusPending,
		Status:      workagentModel.KnowledgeDocumentStatusActive,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	job := workagentModel.KnowledgeIndexJob{
		DocumentID:    doc.Id,
		Status:        workagentModel.KnowledgeIndexJobStatusQueued,
		VectorBackend: "pgvector",
		MetadataJSON:  `{"requested_by":"unit"}`,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := RunKnowledgeIndexJob(db, job.Id); err != nil {
		t.Fatalf("RunKnowledgeIndexJob: %v", err)
	}
	var updatedJob workagentModel.KnowledgeIndexJob
	if err := db.First(&updatedJob, job.Id).Error; err != nil {
		t.Fatalf("load updated job: %v", err)
	}
	if updatedJob.Status != workagentModel.KnowledgeIndexJobStatusSucceeded || updatedJob.ChunkCount != 1 {
		t.Fatalf("updated job = %+v, want succeeded local chunk", updatedJob)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(updatedJob.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode metadata %q: %v", updatedJob.MetadataJSON, err)
	}
	if metadata["vector_backend"] != "pgvector" ||
		metadata["pgvector_mirrored"] != false ||
		metadata["pgvector_fallback_only"] != true ||
		metadata["fallback_backend"] != "lexical" ||
		metadata["fallback_reason"] != "pgvector_adapter_unavailable" {
		t.Fatalf("metadata = %#v, want explicit pgvector fallback-only marker", metadata)
	}
}

func TestRunKnowledgeIndexJob_PGVectorEnabledFailsLoudlyWhenMirrorFails(t *testing.T) {
	t.Setenv("WORKMAX_KNOWLEDGE_PGVECTOR_ENABLED", "true")
	db := newKnowledgeIndexerTestDB(t)
	doc := workagentModel.KnowledgeDocument{
		Title:       "PGVector enabled",
		ContentText: "design knowledge should fail if pgvector table is missing",
		IndexStatus: workagentModel.KnowledgeIndexStatusPending,
		Status:      workagentModel.KnowledgeDocumentStatusActive,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	job := workagentModel.KnowledgeIndexJob{
		DocumentID:    doc.Id,
		Status:        workagentModel.KnowledgeIndexJobStatusQueued,
		VectorBackend: "pgvector",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := RunKnowledgeIndexJob(db, job.Id); err == nil {
		t.Fatal("RunKnowledgeIndexJob succeeded with pgvector enabled and no pgvector table, want loud failure")
	}
	var updatedJob workagentModel.KnowledgeIndexJob
	if err := db.First(&updatedJob, job.Id).Error; err != nil {
		t.Fatalf("load updated job: %v", err)
	}
	if updatedJob.Status != workagentModel.KnowledgeIndexJobStatusFailed || !strings.Contains(updatedJob.ErrorMessage, "pgvector mirror") {
		t.Fatalf("updated job = %+v, want failed pgvector mirror", updatedJob)
	}
}

func newKnowledgeIndexerTestDB(t *testing.T) *gorm.DB {
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
		`CREATE TABLE w_team_member (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME,
			team_id INTEGER NOT NULL,
			uid INTEGER NOT NULL,
			role TEXT NOT NULL DEFAULT 'member',
			status INTEGER NOT NULL DEFAULT 1
		)`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("install schema: %v", err)
		}
	}
	return db
}
