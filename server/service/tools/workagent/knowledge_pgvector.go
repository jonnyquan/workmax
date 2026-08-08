package workagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

const defaultPGVectorKnowledgeTable = "w_workagent_knowledge_pgvector"

var pgvectorTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

type PGVectorKnowledgeRetriever struct {
	DB         *gorm.DB
	Table      string
	searchFunc func(context.Context, []float64, int) ([]pgvectorHit, error)
}

type pgvectorHit struct {
	ChunkID uint
	Score   float64
}

func NewPGVectorKnowledgeRetrieverFromEnv(db *gorm.DB) (PGVectorKnowledgeRetriever, error) {
	if !envBool("WORKMAX_KNOWLEDGE_PGVECTOR_ENABLED") {
		return PGVectorKnowledgeRetriever{}, fmt.Errorf("pgvector knowledge retriever requires WORKMAX_KNOWLEDGE_PGVECTOR_ENABLED=true")
	}
	table := strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_PGVECTOR_TABLE"))
	if table == "" {
		table = defaultPGVectorKnowledgeTable
	}
	if !validPGVectorTableName(table) {
		return PGVectorKnowledgeRetriever{}, fmt.Errorf("pgvector knowledge retriever table name is invalid")
	}
	return PGVectorKnowledgeRetriever{DB: db, Table: table}, nil
}

func (r PGVectorKnowledgeRetriever) Retrieve(ctx context.Context, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return KnowledgeRetrievalResult{}, err
		}
	}
	if r.DB == nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("pgvector retrieval: nil db")
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return KnowledgeRetrievalResult{}, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultKnowledgeRetrievalLimit
	}
	searchLimit := limit * 4
	if searchLimit < limit {
		searchLimit = limit
	}
	hits, err := r.search(ctx, embedKnowledgeText(query), searchLimit)
	if err != nil {
		return KnowledgeRetrievalResult{}, err
	}
	if len(hits) == 0 {
		return KnowledgeRetrievalResult{}, nil
	}

	chunkScores := map[uint]float64{}
	chunkOrder := make([]uint, 0, len(hits))
	for _, hit := range hits {
		if hit.ChunkID == 0 {
			continue
		}
		if _, exists := chunkScores[hit.ChunkID]; !exists {
			chunkOrder = append(chunkOrder, hit.ChunkID)
		}
		chunkScores[hit.ChunkID] = hit.Score
	}
	if len(chunkOrder) == 0 {
		return KnowledgeRetrievalResult{}, nil
	}

	var candidates []knowledgeChunkCandidate
	queryDB := r.DB.Table((&workagentModel.KnowledgeChunk{}).TableName()+" AS c").
		Select("c.id AS chunk_id, c.document_id, c.chunk_index, c.content_text, c.token_count, c.metadata_json, d.title, d.source_uri, d.mime_type, d.scope_type, d.scope_id, d.agent_mode").
		Joins("JOIN "+((&workagentModel.KnowledgeDocument{}).TableName())+" AS d ON d.id = c.document_id").
		Where("c.id IN ?", chunkOrder).
		Where("d.status = ? AND d.index_status = ? AND d.review_status = ?", workagentModel.KnowledgeDocumentStatusActive, workagentModel.KnowledgeIndexStatusIndexed, workagentModel.KnowledgeReviewStatusApproved).
		Where("d.agent_mode = '' OR d.agent_mode = ?", strings.TrimSpace(opts.AgentMode))
	if opts.ProjectID > 0 {
		queryDB = queryDB.Where(buildKnowledgeScopeSQL(opts.TeamIDs, true), buildKnowledgeScopeArgs(opts.ProjectID, opts.TeamIDs)...)
	} else {
		queryDB = queryDB.Where(buildKnowledgeScopeSQL(opts.TeamIDs, false), buildKnowledgeScopeArgs(0, opts.TeamIDs)...)
	}
	if err := queryDB.Scan(&candidates).Error; err != nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("pgvector retrieval db filter: %w", err)
	}
	byChunk := map[uint]knowledgeChunkCandidate{}
	for _, c := range candidates {
		byChunk[c.ChunkID] = c
	}
	scored := make([]scoredKnowledgeChunk, 0, limit)
	for _, id := range chunkOrder {
		c, ok := byChunk[id]
		if !ok {
			continue
		}
		scored = append(scored, scoredKnowledgeChunk{candidate: c, score: chunkScores[id]})
		if len(scored) == limit {
			break
		}
	}
	contextMaxChars := opts.ContextMaxChars
	if contextMaxChars <= 0 {
		contextMaxChars = defaultKnowledgeContextMaxChars
	}
	return buildKnowledgeRetrievalResult(scored, contextMaxChars, "pgvector"), nil
}

func MirrorKnowledgeChunksToPGVector(ctx context.Context, db *gorm.DB, doc workagentModel.KnowledgeDocument, chunks []workagentModel.KnowledgeChunk) error {
	retriever, err := NewPGVectorKnowledgeRetrieverFromEnv(db)
	if err != nil {
		return err
	}
	return retriever.upsert(ctx, doc, chunks)
}

func (r PGVectorKnowledgeRetriever) search(ctx context.Context, vector []float64, limit int) ([]pgvectorHit, error) {
	if r.searchFunc != nil {
		return r.searchFunc(nonNilContext(ctx), vector, limit)
	}
	if r.DB == nil {
		return nil, fmt.Errorf("pgvector retrieval: nil db")
	}
	if limit <= 0 {
		limit = defaultKnowledgeRetrievalLimit
	}
	var rows []struct {
		ChunkID uint    `gorm:"column:chunk_id"`
		Score   float64 `gorm:"column:score"`
	}
	err := r.DB.WithContext(nonNilContext(ctx)).Raw(
		"SELECT chunk_id, GREATEST(0, 1 - (embedding <=> ?::vector)) AS score FROM "+r.tableName()+" ORDER BY embedding <=> ?::vector LIMIT ?",
		pgvectorLiteral(vector), pgvectorLiteral(vector), limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pgvector retrieval search: %w", err)
	}
	hits := make([]pgvectorHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, pgvectorHit{ChunkID: row.ChunkID, Score: row.Score})
	}
	return hits, nil
}

func (r PGVectorKnowledgeRetriever) upsert(ctx context.Context, doc workagentModel.KnowledgeDocument, chunks []workagentModel.KnowledgeChunk) error {
	if r.DB == nil {
		return fmt.Errorf("pgvector mirror: nil db")
	}
	if len(chunks) == 0 {
		return nil
	}
	return r.DB.WithContext(nonNilContext(ctx)).Transaction(func(tx *gorm.DB) error {
		ids := make([]uint, 0, len(chunks))
		for _, chunk := range chunks {
			if chunk.Id != 0 {
				ids = append(ids, chunk.Id)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Exec("DELETE FROM "+r.tableName()+" WHERE chunk_id IN ?", ids).Error; err != nil {
			return fmt.Errorf("pgvector mirror delete stale chunks: %w", err)
		}
		for _, chunk := range chunks {
			if chunk.Id == 0 {
				continue
			}
			vector := pgvectorLiteral(embedKnowledgeText(doc.Title + " " + chunk.ContentText))
			if err := tx.Exec(
				"INSERT INTO "+r.tableName()+" (chunk_id, document_id, embedding, metadata_json, updated_at) VALUES (?, ?, ?::vector, ?::jsonb, NOW())",
				chunk.Id,
				doc.Id,
				vector,
				buildPGVectorMetadataJSON(doc, chunk),
			).Error; err != nil {
				return fmt.Errorf("pgvector mirror upsert chunk %d: %w", chunk.Id, err)
			}
		}
		return nil
	})
}

func (r PGVectorKnowledgeRetriever) tableName() string {
	table := strings.TrimSpace(r.Table)
	if table == "" {
		return defaultPGVectorKnowledgeTable
	}
	return table
}

func validPGVectorTableName(table string) bool {
	return pgvectorTablePattern.MatchString(strings.TrimSpace(table))
}

func pgvectorLiteral(vector []float64) string {
	parts := make([]string, 0, len(vector))
	for _, v := range vector {
		parts = append(parts, strconv.FormatFloat(v, 'f', -1, 64))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func buildPGVectorMetadataJSON(doc workagentModel.KnowledgeDocument, chunk workagentModel.KnowledgeChunk) string {
	data, _ := json.Marshal(map[string]interface{}{
		"document_id": doc.Id,
		"chunk_id":    chunk.Id,
		"chunk_index": chunk.ChunkIndex,
		"title":       doc.Title,
		"scope_type":  doc.ScopeType,
		"scope_id":    doc.ScopeID,
		"agent_mode":  doc.AgentMode,
	})
	return string(data)
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}
