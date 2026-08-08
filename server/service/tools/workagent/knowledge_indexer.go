package workagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

const (
	defaultKnowledgeChunkWords   = 220
	defaultKnowledgeChunkOverlap = 40
)

// RunKnowledgeIndexJob materializes vector-backend-neutral text chunks for one
// queued knowledge index job. It is deliberately synchronous for the first
// admin scaffold; a later worker can call the same function before mirroring
// chunks into pgvector/Qdrant/Pinecone.
func RunKnowledgeIndexJob(db *gorm.DB, jobID uint) error {
	if db == nil {
		return fmt.Errorf("knowledge indexer: nil db")
	}
	if jobID == 0 {
		return fmt.Errorf("knowledge indexer: job id is required")
	}

	var job workagentModel.KnowledgeIndexJob
	if err := db.First(&job, jobID).Error; err != nil {
		return fmt.Errorf("load knowledge index job: %w", err)
	}
	var doc workagentModel.KnowledgeDocument
	if err := db.First(&doc, job.DocumentID).Error; err != nil {
		return markKnowledgeIndexJobFailed(db, &job, fmt.Errorf("load knowledge document: %w", err))
	}

	chunks := BuildKnowledgeChunks(doc, defaultKnowledgeChunkWords, defaultKnowledgeChunkOverlap)
	if len(chunks) == 0 {
		return markKnowledgeIndexJobFailed(db, &job, fmt.Errorf("knowledge document has no indexable text"))
	}

	now := time.Now()
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&job).Updates(map[string]interface{}{
			"status":     "running",
			"started_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ?", doc.Id).Delete(&workagentModel.KnowledgeChunk{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&chunks).Error; err != nil {
			return err
		}
		if err := tx.Model(&job).Updates(map[string]interface{}{
			"status":        workagentModel.KnowledgeIndexJobStatusSucceeded,
			"finished_at":   time.Now(),
			"chunk_count":   len(chunks),
			"metadata_json": mergeKnowledgeJobMetadata(job.MetadataJSON, len(chunks)),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&doc).Updates(map[string]interface{}{
			"index_status": workagentModel.KnowledgeIndexStatusIndexed,
			"chunk_count":  len(chunks),
			"token_count":  estimateKnowledgeTokens(doc.ContentText),
			"indexed_at":   time.Now(),
		}).Error
	}); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(job.VectorBackend), "qdrant") {
		if err := MirrorKnowledgeChunksToQdrant(context.Background(), doc, chunks); err != nil {
			return markKnowledgeIndexJobFailed(db, &job, fmt.Errorf("qdrant mirror: %w", err))
		}
		_ = db.Model(&job).Updates(map[string]interface{}{
			"metadata_json": mergeKnowledgeJobMetadataWithFields(job.MetadataJSON, map[string]interface{}{
				"local_chunk_count": len(chunks),
				"local_chunker":     "words-220-overlap-40",
				"vector_backend":    "qdrant",
				"qdrant_mirrored":   true,
			}),
		}).Error
	}
	if strings.EqualFold(strings.TrimSpace(job.VectorBackend), "pinecone") {
		if err := MirrorKnowledgeChunksToPinecone(context.Background(), doc, chunks); err != nil {
			return markKnowledgeIndexJobFailed(db, &job, fmt.Errorf("pinecone mirror: %w", err))
		}
		_ = db.Model(&job).Updates(map[string]interface{}{
			"metadata_json": mergeKnowledgeJobMetadataWithFields(job.MetadataJSON, map[string]interface{}{
				"local_chunk_count": len(chunks),
				"local_chunker":     "words-220-overlap-40",
				"vector_backend":    "pinecone",
				"pinecone_mirrored": true,
			}),
		}).Error
	}
	if strings.EqualFold(strings.TrimSpace(job.VectorBackend), "pgvector") {
		if err := MirrorKnowledgeChunksToPGVector(context.Background(), db, doc, chunks); err == nil {
			_ = db.Model(&job).Updates(map[string]interface{}{
				"metadata_json": mergeKnowledgeJobMetadataWithFields(job.MetadataJSON, map[string]interface{}{
					"local_chunk_count": len(chunks),
					"local_chunker":     "words-220-overlap-40",
					"vector_backend":    "pgvector",
					"pgvector_mirrored": true,
				}),
			}).Error
			return nil
		} else if envBool("WORKMAX_KNOWLEDGE_PGVECTOR_ENABLED") {
			return markKnowledgeIndexJobFailed(db, &job, fmt.Errorf("pgvector mirror: %w", err))
		}
		_ = db.Model(&job).Updates(map[string]interface{}{
			"metadata_json": mergeKnowledgeJobMetadataWithFields(job.MetadataJSON, map[string]interface{}{
				"local_chunk_count":      len(chunks),
				"local_chunker":          "words-220-overlap-40",
				"vector_backend":         "pgvector",
				"pgvector_mirrored":      false,
				"pgvector_fallback_only": true,
				"fallback_backend":       "lexical",
				"fallback_reason":        "pgvector_adapter_unavailable",
			}),
		}).Error
	}
	return nil
}

func BuildKnowledgeChunks(doc workagentModel.KnowledgeDocument, maxWords int, overlapWords int) []workagentModel.KnowledgeChunk {
	content := strings.TrimSpace(doc.ContentText)
	if content == "" {
		return nil
	}
	if maxWords <= 0 {
		maxWords = defaultKnowledgeChunkWords
	}
	if overlapWords < 0 || overlapWords >= maxWords {
		overlapWords = 0
	}

	words := strings.Fields(content)
	if len(words) == 0 {
		return nil
	}
	if len(words) == 1 {
		return []workagentModel.KnowledgeChunk{buildKnowledgeChunk(doc, 0, content)}
	}

	step := maxWords - overlapWords
	chunks := make([]workagentModel.KnowledgeChunk, 0, (len(words)/step)+1)
	for start := 0; start < len(words); start += step {
		end := start + maxWords
		if end > len(words) {
			end = len(words)
		}
		text := strings.Join(words[start:end], " ")
		chunks = append(chunks, buildKnowledgeChunk(doc, len(chunks), text))
		if end == len(words) {
			break
		}
	}
	return chunks
}

func buildKnowledgeChunk(doc workagentModel.KnowledgeDocument, index int, text string) workagentModel.KnowledgeChunk {
	text = strings.TrimSpace(text)
	sum := sha256.Sum256([]byte(text))
	return workagentModel.KnowledgeChunk{
		DocumentID:   doc.Id,
		ChunkIndex:   index,
		ContentText:  text,
		ContentHash:  hex.EncodeToString(sum[:]),
		TokenCount:   estimateKnowledgeTokens(text),
		MetadataJSON: doc.MetadataJSON,
	}
}

func estimateKnowledgeTokens(content string) int {
	return len(strings.Fields(strings.TrimSpace(content)))
}

func markKnowledgeIndexJobFailed(db *gorm.DB, job *workagentModel.KnowledgeIndexJob, err error) error {
	if job != nil && job.Id != 0 {
		_ = db.Model(job).Updates(map[string]interface{}{
			"status":        workagentModel.KnowledgeIndexJobStatusFailed,
			"finished_at":   time.Now(),
			"error_message": err.Error(),
		}).Error
	}
	return err
}

func mergeKnowledgeJobMetadata(raw string, chunkCount int) string {
	return mergeKnowledgeJobMetadataWithFields(raw, map[string]interface{}{
		"local_chunk_count": chunkCount,
		"local_chunker":     "words-220-overlap-40",
	})
}

func mergeKnowledgeJobMetadataWithFields(raw string, fields map[string]interface{}) string {
	payload := map[string]interface{}{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &payload)
	}
	for k, v := range fields {
		payload[k] = v
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return string(data)
}
