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

type QdrantKnowledgeRetriever struct {
	DB         *gorm.DB
	Endpoint   string
	Collection string
	APIKey     string
	HTTPClient *http.Client
}

type qdrantPoint struct {
	ID      uint64                 `json:"id"`
	Vector  []float64              `json:"vector"`
	Payload map[string]interface{} `json:"payload"`
}

type qdrantSearchResponse struct {
	Result []struct {
		ID      interface{}            `json:"id"`
		Score   float64                `json:"score"`
		Payload map[string]interface{} `json:"payload"`
	} `json:"result"`
}

func NewQdrantKnowledgeRetrieverFromEnv(db *gorm.DB) (QdrantKnowledgeRetriever, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_QDRANT_URL")), "/")
	collection := strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_QDRANT_COLLECTION"))
	if endpoint == "" || collection == "" {
		return QdrantKnowledgeRetriever{}, fmt.Errorf("qdrant knowledge retriever requires WORKMAX_KNOWLEDGE_QDRANT_URL and WORKMAX_KNOWLEDGE_QDRANT_COLLECTION")
	}
	return QdrantKnowledgeRetriever{
		DB:         db,
		Endpoint:   endpoint,
		Collection: collection,
		APIKey:     strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_QDRANT_API_KEY")),
		HTTPClient: http.DefaultClient,
	}, nil
}

func (r QdrantKnowledgeRetriever) Retrieve(ctx context.Context, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	if r.DB == nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("qdrant retrieval: nil db")
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
	points, err := r.search(ctx, embedKnowledgeText(query), searchLimit)
	if err != nil {
		return KnowledgeRetrievalResult{}, err
	}
	if len(points.Result) == 0 {
		return KnowledgeRetrievalResult{}, nil
	}
	chunkScores := map[uint]float64{}
	chunkOrder := make([]uint, 0, len(points.Result))
	for _, point := range points.Result {
		chunkID := qdrantPayloadUint(point.Payload, "chunk_id")
		if chunkID == 0 {
			chunkID = qdrantPointIDUint(point.ID)
		}
		if chunkID == 0 {
			continue
		}
		id := uint(chunkID)
		if _, exists := chunkScores[id]; !exists {
			chunkOrder = append(chunkOrder, id)
		}
		chunkScores[id] = point.Score
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
		return KnowledgeRetrievalResult{}, fmt.Errorf("qdrant retrieval db filter: %w", err)
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
	return buildKnowledgeRetrievalResult(scored, contextMaxChars, "qdrant"), nil
}

func MirrorKnowledgeChunksToQdrant(ctx context.Context, doc workagentModel.KnowledgeDocument, chunks []workagentModel.KnowledgeChunk) error {
	retriever, err := NewQdrantKnowledgeRetrieverFromEnv(nil)
	if err != nil {
		return err
	}
	return retriever.upsert(ctx, doc, chunks)
}

func (r QdrantKnowledgeRetriever) upsert(ctx context.Context, doc workagentModel.KnowledgeDocument, chunks []workagentModel.KnowledgeChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	points := make([]qdrantPoint, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk.Id == 0 {
			continue
		}
		points = append(points, qdrantPoint{
			ID:     uint64(chunk.Id),
			Vector: embedKnowledgeText(doc.Title + " " + chunk.ContentText),
			Payload: map[string]interface{}{
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
	if len(points) == 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]interface{}{"points": points})
	path := fmt.Sprintf("%s/collections/%s/points?wait=true", r.Endpoint, r.Collection)
	return r.doJSON(ctx, http.MethodPut, path, body, nil)
}

func (r QdrantKnowledgeRetriever) search(ctx context.Context, vector []float64, limit int) (qdrantSearchResponse, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"vector":       vector,
		"limit":        limit,
		"with_payload": true,
	})
	path := fmt.Sprintf("%s/collections/%s/points/search", r.Endpoint, r.Collection)
	var out qdrantSearchResponse
	err := r.doJSON(ctx, http.MethodPost, path, body, &out)
	return out, err
}

func (r QdrantKnowledgeRetriever) doJSON(ctx context.Context, method string, url string, body []byte, out interface{}) error {
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
		req.Header.Set("api-key", r.APIKey)
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
		return fmt.Errorf("qdrant %s %s returned %s", method, url, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func qdrantPayloadUint(payload map[string]interface{}, key string) uint64 {
	if payload == nil {
		return 0
	}
	return qdrantPointIDUint(payload[key])
}

func qdrantPointIDUint(value interface{}) uint64 {
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
