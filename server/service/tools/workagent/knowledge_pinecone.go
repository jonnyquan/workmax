package workagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

type PineconeKnowledgeRetriever struct {
	DB         *gorm.DB
	Host       string
	APIKey     string
	Namespace  string
	HTTPClient *http.Client
}

type pineconeVector struct {
	ID       string                 `json:"id"`
	Values   []float64              `json:"values"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type pineconeQueryResponse struct {
	Matches []struct {
		ID       string                 `json:"id"`
		Score    float64                `json:"score"`
		Metadata map[string]interface{} `json:"metadata"`
	} `json:"matches"`
}

func NewPineconeKnowledgeRetrieverFromEnv(db *gorm.DB) (PineconeKnowledgeRetriever, error) {
	host := strings.TrimRight(strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_PINECONE_HOST")), "/")
	apiKey := strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_PINECONE_API_KEY"))
	if host == "" || apiKey == "" {
		return PineconeKnowledgeRetriever{}, fmt.Errorf("pinecone knowledge retriever requires WORKMAX_KNOWLEDGE_PINECONE_HOST and WORKMAX_KNOWLEDGE_PINECONE_API_KEY")
	}
	return PineconeKnowledgeRetriever{
		DB:         db,
		Host:       host,
		APIKey:     apiKey,
		Namespace:  strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_PINECONE_NAMESPACE")),
		HTTPClient: http.DefaultClient,
	}, nil
}

func (r PineconeKnowledgeRetriever) Retrieve(ctx context.Context, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	if r.DB == nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("pinecone retrieval: nil db")
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
	matches, err := r.search(ctx, embedKnowledgeText(query), searchLimit)
	if err != nil {
		return KnowledgeRetrievalResult{}, err
	}
	if len(matches.Matches) == 0 {
		return KnowledgeRetrievalResult{}, nil
	}

	chunkScores := map[uint]float64{}
	chunkOrder := make([]uint, 0, len(matches.Matches))
	for _, match := range matches.Matches {
		chunkID := pineconeMetadataUint(match.Metadata, "chunk_id")
		if chunkID == 0 {
			chunkID = pineconeIDUint(match.ID)
		}
		if chunkID == 0 {
			continue
		}
		id := uint(chunkID)
		if _, exists := chunkScores[id]; !exists {
			chunkOrder = append(chunkOrder, id)
		}
		chunkScores[id] = match.Score
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
		return KnowledgeRetrievalResult{}, fmt.Errorf("pinecone retrieval db filter: %w", err)
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
	return buildKnowledgeRetrievalResult(scored, contextMaxChars, "pinecone"), nil
}

func MirrorKnowledgeChunksToPinecone(ctx context.Context, doc workagentModel.KnowledgeDocument, chunks []workagentModel.KnowledgeChunk) error {
	retriever, err := NewPineconeKnowledgeRetrieverFromEnv(nil)
	if err != nil {
		return err
	}
	return retriever.upsert(ctx, doc, chunks)
}

func (r PineconeKnowledgeRetriever) upsert(ctx context.Context, doc workagentModel.KnowledgeDocument, chunks []workagentModel.KnowledgeChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	vectors := make([]pineconeVector, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk.Id == 0 {
			continue
		}
		vectors = append(vectors, pineconeVector{
			ID:     strconv.FormatUint(uint64(chunk.Id), 10),
			Values: embedKnowledgeText(doc.Title + " " + chunk.ContentText),
			Metadata: map[string]interface{}{
				"document_id": doc.Id,
				"chunk_id":    chunk.Id,
				"chunk_index": chunk.ChunkIndex,
				"title":       doc.Title,
				"scope_type":  doc.ScopeType,
				"scope_id":    doc.ScopeID,
				"agent_mode":  doc.AgentMode,
			},
		})
	}
	if len(vectors) == 0 {
		return nil
	}
	bodyMap := map[string]interface{}{"vectors": vectors}
	if strings.TrimSpace(r.Namespace) != "" {
		bodyMap["namespace"] = strings.TrimSpace(r.Namespace)
	}
	body, _ := json.Marshal(bodyMap)
	return r.doJSON(ctx, http.MethodPost, r.Host+"/vectors/upsert", body, nil)
}

func (r PineconeKnowledgeRetriever) search(ctx context.Context, vector []float64, limit int) (pineconeQueryResponse, error) {
	bodyMap := map[string]interface{}{
		"vector":          vector,
		"topK":            limit,
		"includeMetadata": true,
	}
	if strings.TrimSpace(r.Namespace) != "" {
		bodyMap["namespace"] = strings.TrimSpace(r.Namespace)
	}
	body, _ := json.Marshal(bodyMap)
	var out pineconeQueryResponse
	err := r.doJSON(ctx, http.MethodPost, r.Host+"/query", body, &out)
	return out, err
}

func (r PineconeKnowledgeRetriever) doJSON(ctx context.Context, method string, url string, body []byte, out interface{}) error {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Api-Key", r.APIKey)
	}
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pinecone %s %s returned %s", method, url, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func pineconeMetadataUint(metadata map[string]interface{}, key string) uint64 {
	if metadata == nil {
		return 0
	}
	return pineconeIDUint(metadata[key])
}

func pineconeIDUint(value interface{}) uint64 {
	switch v := value.(type) {
	case float64:
		return uint64(v)
	case int:
		return uint64(v)
	case uint:
		return uint64(v)
	case uint64:
		return v
	case json.Number:
		out, _ := strconv.ParseUint(string(v), 10, 64)
		return out
	case string:
		out, _ := strconv.ParseUint(v, 10, 64)
		return out
	default:
		return 0
	}
}
