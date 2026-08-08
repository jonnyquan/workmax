package workagent

import (
	"time"

	"server/globals"
)

const (
	KnowledgeDocumentStatusActive   = "active"
	KnowledgeDocumentStatusArchived = "archived"

	KnowledgeReviewStatusPending  = "pending"
	KnowledgeReviewStatusApproved = "approved"
	KnowledgeReviewStatusRejected = "rejected"

	KnowledgeIndexStatusPending = "pending"
	KnowledgeIndexStatusIndexed = "indexed"
	KnowledgeIndexStatusFailed  = "failed"

	KnowledgeIndexJobStatusQueued    = "queued"
	KnowledgeIndexJobStatusSucceeded = "succeeded"
	KnowledgeIndexJobStatusFailed    = "failed"
)

// KnowledgeDocument is the platform-owned knowledge-base source that can later
// feed Work Agent RAG. It intentionally stores normalized text and metadata
// only; vector chunks live behind a backend-specific indexer.
type KnowledgeDocument struct {
	globals.GraMODEL
	Title        string     `json:"title" gorm:"column:title;type:varchar(255);not null;comment:document title"`
	SourceType   string     `json:"sourceType" gorm:"column:source_type;type:varchar(32);not null;default:'manual';index;comment:manual/upload/url"`
	SourceURI    string     `json:"sourceUri" gorm:"column:source_uri;type:varchar(1024);not null;default:'';comment:source object key or URL"`
	MimeType     string     `json:"mimeType" gorm:"column:mime_type;type:varchar(120);not null;default:'text/plain';comment:source mime type"`
	ContentHash  string     `json:"contentHash" gorm:"column:content_hash;type:varchar(64);not null;default:'';index;comment:sha256 of normalized text"`
	ContentText  string     `json:"contentText" gorm:"column:content_text;type:longtext;comment:normalized plain text for indexing"`
	MetadataJSON string     `json:"metadataJson" gorm:"column:metadata_json;type:text;comment:JSON metadata for filters/citations"`
	ScopeType    string     `json:"scopeType" gorm:"column:scope_type;type:varchar(32);not null;default:'global';index;comment:global/project/team"`
	ScopeID      uint       `json:"scopeId" gorm:"column:scope_id;not null;default:0;index;comment:scope entity id"`
	AgentMode    string     `json:"agentMode" gorm:"column:agent_mode;type:varchar(64);not null;default:'';index;comment:optional Work Agent mode filter"`
	ReviewStatus string     `json:"reviewStatus" gorm:"column:review_status;type:varchar(32);not null;default:'pending';index;comment:pending/approved/rejected"`
	ReviewedBy   int        `json:"reviewedBy" gorm:"column:reviewed_by;not null;default:0;index;comment:review admin uid"`
	ReviewedAt   *time.Time `json:"reviewedAt" gorm:"column:reviewed_at;comment:review time"`
	ReviewNote   string     `json:"reviewNote" gorm:"column:review_note;type:varchar(512);not null;default:'';comment:review note"`
	Status       string     `json:"status" gorm:"column:status;type:varchar(32);not null;default:'active';index;comment:active/archived"`
	IndexStatus  string     `json:"indexStatus" gorm:"column:index_status;type:varchar(32);not null;default:'pending';index;comment:pending/indexed/failed"`
	ChunkCount   int        `json:"chunkCount" gorm:"column:chunk_count;not null;default:0;comment:last indexed chunk count"`
	TokenCount   int        `json:"tokenCount" gorm:"column:token_count;not null;default:0;comment:estimated source token count"`
	IndexedAt    *time.Time `json:"indexedAt" gorm:"column:indexed_at;comment:last successful index time"`
	UpdatedBy    int        `json:"updatedBy" gorm:"column:updated_by;not null;default:0;index;comment:admin uid"`
}

func (KnowledgeDocument) TableName() string {
	return "w_workagent_knowledge_document"
}

// KnowledgeIndexJob records admin-triggered reindex requests. The queued row is
// the durable handoff point for a future vector backend worker.
type KnowledgeIndexJob struct {
	globals.GraMODEL
	DocumentID    uint       `json:"documentId" gorm:"column:document_id;not null;index;comment:w_workagent_knowledge_document.id"`
	Status        string     `json:"status" gorm:"column:status;type:varchar(32);not null;default:'queued';index;comment:queued/succeeded/failed"`
	VectorBackend string     `json:"vectorBackend" gorm:"column:vector_backend;type:varchar(64);not null;default:'';comment:pgvector/qdrant/pinecone/etc"`
	RequestedBy   int        `json:"requestedBy" gorm:"column:requested_by;not null;default:0;index;comment:admin uid"`
	StartedAt     *time.Time `json:"startedAt" gorm:"column:started_at;comment:index start time"`
	FinishedAt    *time.Time `json:"finishedAt" gorm:"column:finished_at;comment:index finish time"`
	ChunkCount    int        `json:"chunkCount" gorm:"column:chunk_count;not null;default:0;comment:indexed chunk count"`
	ErrorMessage  string     `json:"errorMessage" gorm:"column:error_message;type:text;comment:index error"`
	MetadataJSON  string     `json:"metadataJson" gorm:"column:metadata_json;type:text;comment:worker metadata JSON"`
}

func (KnowledgeIndexJob) TableName() string {
	return "w_workagent_knowledge_index_job"
}

// KnowledgeChunk is the vector-backend-neutral indexing unit. Runtime RAG can
// later either query this table directly for lexical fallback or mirror it into
// pgvector/Qdrant/Pinecone with DocumentID + ChunkIndex as the stable key.
type KnowledgeChunk struct {
	globals.GraMODEL
	DocumentID   uint   `json:"documentId" gorm:"column:document_id;not null;index;comment:w_workagent_knowledge_document.id"`
	ChunkIndex   int    `json:"chunkIndex" gorm:"column:chunk_index;not null;comment:zero-based chunk order"`
	ContentText  string `json:"contentText" gorm:"column:content_text;type:text;comment:chunk text"`
	ContentHash  string `json:"contentHash" gorm:"column:content_hash;type:varchar(64);not null;default:'';index;comment:sha256 of chunk text"`
	TokenCount   int    `json:"tokenCount" gorm:"column:token_count;not null;default:0;comment:estimated chunk token count"`
	MetadataJSON string `json:"metadataJson" gorm:"column:metadata_json;type:text;comment:JSON metadata for citation filters"`
}

func (KnowledgeChunk) TableName() string {
	return "w_workagent_knowledge_chunk"
}
