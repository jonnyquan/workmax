package workagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
)

func TestNewKnowledgeRetriever_DefaultsToLexical(t *testing.T) {
	retriever, err := NewKnowledgeRetriever(nil, "")
	if err != nil {
		t.Fatalf("NewKnowledgeRetriever: %v", err)
	}
	if _, ok := retriever.(LexicalKnowledgeRetriever); !ok {
		t.Fatalf("retriever type = %T, want LexicalKnowledgeRetriever", retriever)
	}
}

func TestNewKnowledgeRetriever_LocalVector(t *testing.T) {
	retriever, err := NewKnowledgeRetriever(nil, KnowledgeRetrieverBackendLocalVector)
	if err != nil {
		t.Fatalf("NewKnowledgeRetriever: %v", err)
	}
	if _, ok := retriever.(LocalVectorKnowledgeRetriever); !ok {
		t.Fatalf("retriever type = %T, want LocalVectorKnowledgeRetriever", retriever)
	}
}

func TestNewKnowledgeRetriever_PineconeFromEnv(t *testing.T) {
	t.Setenv("WORKMAX_KNOWLEDGE_PINECONE_HOST", "http://pinecone.local")
	t.Setenv("WORKMAX_KNOWLEDGE_PINECONE_API_KEY", "test-key")
	t.Setenv("WORKMAX_KNOWLEDGE_PINECONE_NAMESPACE", "workspace")

	retriever, err := NewKnowledgeRetriever(nil, "pinecone")
	if err != nil {
		t.Fatalf("NewKnowledgeRetriever: %v", err)
	}
	pinecone, ok := retriever.(PineconeKnowledgeRetriever)
	if !ok {
		t.Fatalf("retriever type = %T, want PineconeKnowledgeRetriever", retriever)
	}
	if pinecone.Host != "http://pinecone.local" || pinecone.APIKey != "test-key" || pinecone.Namespace != "workspace" {
		t.Fatalf("pinecone env config = %+v", pinecone)
	}
}

func TestNewKnowledgeRetriever_PGVectorFromEnv(t *testing.T) {
	t.Setenv("WORKMAX_KNOWLEDGE_PGVECTOR_ENABLED", "true")
	t.Setenv("WORKMAX_KNOWLEDGE_PGVECTOR_TABLE", "workagent_test_vectors")

	retriever, err := NewKnowledgeRetriever(nil, "pgvector")
	if err != nil {
		t.Fatalf("NewKnowledgeRetriever: %v", err)
	}
	pgvector, ok := retriever.(PGVectorKnowledgeRetriever)
	if !ok {
		t.Fatalf("retriever type = %T, want PGVectorKnowledgeRetriever", retriever)
	}
	if pgvector.Table != "workagent_test_vectors" {
		t.Fatalf("pgvector table = %q", pgvector.Table)
	}
}

func TestRetrieveKnowledgeContext_LocalVectorRanksAndFormats(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db, "Design vector handbook", "HTML artboard export checks and source fidelity rules")
	seedKnowledgeDocWithChunks(t, db, "Billing vector handbook", "Subscription renewal credits and invoice handling")
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	got, err := RetrieveKnowledgeContext(context.Background(), db, KnowledgeRetrieverBackendLocalVector, KnowledgeRetrievalOptions{
		Query:     "html export artboard fidelity",
		Limit:     1,
		ProjectID: 9,
		AgentMode: "ppt",
	})
	if err != nil {
		t.Fatalf("RetrieveKnowledgeContext: %v", err)
	}
	if got.Metadata.Retriever != KnowledgeRetrieverBackendLocalVector || got.Metadata.DocumentsRetrieved != 1 {
		t.Fatalf("metadata = %+v", got.Metadata)
	}
	if got.Sources[0].Title != "Design vector handbook" {
		t.Fatalf("top vector source = %+v", got.Sources)
	}
	if got.ContextXML == "" || !strings.Contains(got.ContextXML, `retriever="local-vector"`) {
		t.Fatalf("context XML = %s", got.ContextXML)
	}
	ev := rec.FindByEvent("wa_knowledge_retrieval")
	if ev == nil {
		t.Fatal("expected wa_knowledge_retrieval metric")
	}
	if ev.Fields["retriever"] != KnowledgeRetrieverBackendLocalVector || ev.Fields["documents_retrieved"] != 1 || ev.Fields["project_id"] != uint(9) || ev.Fields["agent_mode"] != "ppt" {
		t.Fatalf("unexpected knowledge retrieval metric: %+v", ev.Fields)
	}
}

func TestPGVectorKnowledgeRetriever_FiltersVectorHitsThroughDBScopeAndReview(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	allowed := seedKnowledgeDocWithChunks(t, db, "Allowed pgvector handbook", "HTML export source fidelity guidance")
	unapproved := seedKnowledgeDocWithChunks(t, db, "Unapproved pgvector handbook", "HTML export source fidelity guidance", func(doc *workagentModel.KnowledgeDocument) {
		doc.ReviewStatus = workagentModel.KnowledgeReviewStatusRejected
	})
	otherProject := seedKnowledgeDocWithChunks(t, db, "Other project pgvector handbook", "HTML export source fidelity guidance", func(doc *workagentModel.KnowledgeDocument) {
		doc.ScopeType = "project"
		doc.ScopeID = 99
	})
	retriever := PGVectorKnowledgeRetriever{
		DB: db,
		searchFunc: func(ctx context.Context, vector []float64, limit int) ([]pgvectorHit, error) {
			if len(vector) != localKnowledgeVectorDimensions || limit == 0 {
				t.Fatalf("unexpected pgvector search args: len(vector)=%d limit=%d", len(vector), limit)
			}
			return []pgvectorHit{
				{ChunkID: unapproved.Id, Score: 0.99},
				{ChunkID: otherProject.Id, Score: 0.98},
				{ChunkID: allowed.Id, Score: 0.90},
			}, nil
		},
	}

	got, err := retriever.Retrieve(context.Background(), KnowledgeRetrievalOptions{Query: "html fidelity", Limit: 3, ProjectID: 77})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.Metadata.Retriever != "pgvector" || len(got.Sources) != 1 || got.Sources[0].Title != "Allowed pgvector handbook" {
		t.Fatalf("filtered pgvector result = %+v, want only allowed DB-authorized chunk", got)
	}
}

func TestQdrantKnowledgeRetriever_SearchesAndFiltersThroughDB(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db, "Qdrant design handbook", "HTML export source fidelity guidance")
	var chunk workagentModel.KnowledgeChunk
	if err := db.First(&chunk).Error; err != nil {
		t.Fatalf("load chunk: %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/collections/workagent/points/search" {
			t.Fatalf("unexpected qdrant request: %s %s", req.Method, req.URL.String())
		}
		var payload struct {
			Vector      []float64 `json:"vector"`
			Limit       int       `json:"limit"`
			WithPayload bool      `json:"with_payload"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode search payload: %v", err)
		}
		if len(payload.Vector) != localKnowledgeVectorDimensions || payload.Limit == 0 || !payload.WithPayload {
			t.Fatalf("unexpected search payload: %+v", payload)
		}
		return jsonResponse(`{"result":[{"id":` + uintString(chunk.Id) + `,"score":0.91,"payload":{"chunk_id":` + uintString(chunk.Id) + `}}]}`), nil
	})}
	retriever := QdrantKnowledgeRetriever{
		DB:         db,
		Endpoint:   "http://qdrant.local",
		Collection: "workagent",
		HTTPClient: client,
	}

	got, err := retriever.Retrieve(context.Background(), KnowledgeRetrievalOptions{Query: "html fidelity", Limit: 2})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.Metadata.Retriever != "qdrant" || len(got.Sources) != 1 || got.Sources[0].Title != "Qdrant design handbook" {
		t.Fatalf("qdrant result = %+v", got)
	}
}

func TestQdrantKnowledgeRetriever_FiltersExternalHitsThroughDBScopeAndReview(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	allowed := seedKnowledgeDocWithChunks(t, db, "Allowed Qdrant handbook", "HTML export source fidelity guidance")
	unapproved := seedKnowledgeDocWithChunks(t, db, "Unapproved Qdrant handbook", "HTML export source fidelity guidance", func(doc *workagentModel.KnowledgeDocument) {
		doc.ReviewStatus = workagentModel.KnowledgeReviewStatusPending
	})
	otherProject := seedKnowledgeDocWithChunks(t, db, "Other project Qdrant handbook", "HTML export source fidelity guidance", func(doc *workagentModel.KnowledgeDocument) {
		doc.ScopeType = "project"
		doc.ScopeID = 99
	})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/collections/workagent/points/search" {
			t.Fatalf("unexpected qdrant request: %s %s", req.Method, req.URL.String())
		}
		return jsonResponse(`{"result":[` +
			`{"id":` + uintString(unapproved.Id) + `,"score":0.99,"payload":{"chunk_id":` + uintString(unapproved.Id) + `}},` +
			`{"id":` + uintString(otherProject.Id) + `,"score":0.98,"payload":{"chunk_id":` + uintString(otherProject.Id) + `}},` +
			`{"id":` + uintString(allowed.Id) + `,"score":0.90,"payload":{"chunk_id":` + uintString(allowed.Id) + `}}` +
			`]}`), nil
	})}
	retriever := QdrantKnowledgeRetriever{
		DB:         db,
		Endpoint:   "http://qdrant.local",
		Collection: "workagent",
		HTTPClient: client,
	}

	got, err := retriever.Retrieve(context.Background(), KnowledgeRetrievalOptions{Query: "html fidelity", Limit: 3, ProjectID: 77})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Title != "Allowed Qdrant handbook" {
		t.Fatalf("filtered qdrant sources = %+v, want only allowed DB-authorized chunk", got.Sources)
	}
}

func TestQdrantKnowledgeRetriever_UpsertsChunkVectors(t *testing.T) {
	doc := workagentModel.KnowledgeDocument{Title: "Qdrant mirror", ScopeType: "team", ScopeID: 7, AgentMode: "ppt"}
	doc.Id = 42
	chunk := workagentModel.KnowledgeChunk{DocumentID: 42, ChunkIndex: 0, ContentText: "Mirror this chunk"}
	chunk.Id = 77
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut || req.URL.Path != "/collections/workagent/points" || req.URL.Query().Get("wait") != "true" {
			t.Fatalf("unexpected qdrant upsert request: %s %s", req.Method, req.URL.String())
		}
		var payload struct {
			Points []struct {
				ID      uint64                 `json:"id"`
				Vector  []float64              `json:"vector"`
				Payload map[string]interface{} `json:"payload"`
			} `json:"points"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upsert payload: %v", err)
		}
		if len(payload.Points) != 1 || payload.Points[0].ID != 77 || len(payload.Points[0].Vector) != localKnowledgeVectorDimensions {
			t.Fatalf("unexpected upsert payload: %+v", payload)
		}
		if payload.Points[0].Payload["document_id"].(float64) != 42 || payload.Points[0].Payload["scope_type"].(string) != "team" {
			t.Fatalf("unexpected upsert payload metadata: %+v", payload.Points[0].Payload)
		}
		return jsonResponse(`{"result":{"operation_id":1,"status":"completed"}}`), nil
	})}
	retriever := QdrantKnowledgeRetriever{Endpoint: "http://qdrant.local", Collection: "workagent", HTTPClient: client}

	if err := retriever.upsert(context.Background(), doc, []workagentModel.KnowledgeChunk{chunk}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func TestPineconeKnowledgeRetriever_SearchesAndFiltersThroughDB(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db, "Pinecone design handbook", "HTML export source fidelity guidance")
	var chunk workagentModel.KnowledgeChunk
	if err := db.First(&chunk).Error; err != nil {
		t.Fatalf("load chunk: %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/query" {
			t.Fatalf("unexpected pinecone request: %s %s", req.Method, req.URL.String())
		}
		if got := req.Header.Get("Api-Key"); got != "test-key" {
			t.Fatalf("Api-Key header = %q", got)
		}
		var payload struct {
			Vector          []float64 `json:"vector"`
			TopK            int       `json:"topK"`
			IncludeMetadata bool      `json:"includeMetadata"`
			Namespace       string    `json:"namespace"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode search payload: %v", err)
		}
		if len(payload.Vector) != localKnowledgeVectorDimensions || payload.TopK == 0 || !payload.IncludeMetadata || payload.Namespace != "workspace" {
			t.Fatalf("unexpected search payload: %+v", payload)
		}
		return jsonResponse(`{"matches":[{"id":"` + uintString(chunk.Id) + `","score":0.91,"metadata":{"chunk_id":` + uintString(chunk.Id) + `}}]}`), nil
	})}
	retriever := PineconeKnowledgeRetriever{
		DB:         db,
		Host:       "http://pinecone.local",
		APIKey:     "test-key",
		Namespace:  "workspace",
		HTTPClient: client,
	}

	got, err := retriever.Retrieve(context.Background(), KnowledgeRetrievalOptions{Query: "html fidelity", Limit: 2})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.Metadata.Retriever != "pinecone" || len(got.Sources) != 1 || got.Sources[0].Title != "Pinecone design handbook" {
		t.Fatalf("pinecone result = %+v", got)
	}
}

func TestPineconeKnowledgeRetriever_FiltersExternalHitsThroughDBScopeAndReview(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	allowed := seedKnowledgeDocWithChunks(t, db, "Allowed Pinecone handbook", "HTML export source fidelity guidance")
	unapproved := seedKnowledgeDocWithChunks(t, db, "Unapproved Pinecone handbook", "HTML export source fidelity guidance", func(doc *workagentModel.KnowledgeDocument) {
		doc.ReviewStatus = workagentModel.KnowledgeReviewStatusRejected
	})
	otherTeam := seedKnowledgeDocWithChunks(t, db, "Other team Pinecone handbook", "HTML export source fidelity guidance", func(doc *workagentModel.KnowledgeDocument) {
		doc.ScopeType = "team"
		doc.ScopeID = 9001
	})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/query" {
			t.Fatalf("unexpected pinecone request: %s %s", req.Method, req.URL.String())
		}
		return jsonResponse(`{"matches":[` +
			`{"id":"` + uintString(unapproved.Id) + `","score":0.99,"metadata":{"chunk_id":` + uintString(unapproved.Id) + `}},` +
			`{"id":"` + uintString(otherTeam.Id) + `","score":0.98,"metadata":{"chunk_id":` + uintString(otherTeam.Id) + `}},` +
			`{"id":"` + uintString(allowed.Id) + `","score":0.90,"metadata":{"chunk_id":` + uintString(allowed.Id) + `}}` +
			`]}`), nil
	})}
	retriever := PineconeKnowledgeRetriever{
		DB:         db,
		Host:       "http://pinecone.local",
		APIKey:     "test-key",
		Namespace:  "workspace",
		HTTPClient: client,
	}

	got, err := retriever.Retrieve(context.Background(), KnowledgeRetrievalOptions{Query: "html fidelity", Limit: 3, TeamIDs: []uint64{7001}})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Title != "Allowed Pinecone handbook" {
		t.Fatalf("filtered pinecone sources = %+v, want only allowed DB-authorized chunk", got.Sources)
	}
}

func TestPineconeKnowledgeRetriever_UpsertsChunkVectors(t *testing.T) {
	doc := workagentModel.KnowledgeDocument{Title: "Pinecone mirror", ScopeType: "team", ScopeID: 7, AgentMode: "ppt"}
	doc.Id = 42
	chunk := workagentModel.KnowledgeChunk{DocumentID: 42, ChunkIndex: 0, ContentText: "Mirror this chunk"}
	chunk.Id = 77
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/vectors/upsert" {
			t.Fatalf("unexpected pinecone upsert request: %s %s", req.Method, req.URL.String())
		}
		if got := req.Header.Get("Api-Key"); got != "test-key" {
			t.Fatalf("Api-Key header = %q", got)
		}
		var payload struct {
			Vectors []struct {
				ID       string                 `json:"id"`
				Values   []float64              `json:"values"`
				Metadata map[string]interface{} `json:"metadata"`
			} `json:"vectors"`
			Namespace string `json:"namespace"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upsert payload: %v", err)
		}
		if len(payload.Vectors) != 1 || payload.Vectors[0].ID != "77" || len(payload.Vectors[0].Values) != localKnowledgeVectorDimensions || payload.Namespace != "workspace" {
			t.Fatalf("unexpected upsert payload: %+v", payload)
		}
		if payload.Vectors[0].Metadata["document_id"].(float64) != 42 || payload.Vectors[0].Metadata["scope_type"].(string) != "team" {
			t.Fatalf("unexpected upsert payload metadata: %+v", payload.Vectors[0].Metadata)
		}
		return jsonResponse(`{"upsertedCount":1}`), nil
	})}
	retriever := PineconeKnowledgeRetriever{Host: "http://pinecone.local", APIKey: "test-key", Namespace: "workspace", HTTPClient: client}

	if err := retriever.upsert(context.Background(), doc, []workagentModel.KnowledgeChunk{chunk}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func TestRetrieveKnowledgeContext_ConfiguredVectorBackendFallsBackToLexical(t *testing.T) {
	t.Setenv("WORKMAX_KNOWLEDGE_PGVECTOR_ENABLED", "")
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db, "Fallback handbook", "Design export fallback context")
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	got, err := RetrieveKnowledgeContext(context.Background(), db, "pgvector", KnowledgeRetrievalOptions{
		Query: "design export fallback",
	})
	if err != nil {
		t.Fatalf("RetrieveKnowledgeContext: %v", err)
	}
	if got.Metadata.DocumentsRetrieved != 1 || got.Sources[0].Title != "Fallback handbook" {
		t.Fatalf("fallback result = %+v", got)
	}
	if got.Metadata.RequestedBackend != "pgvector" ||
		got.Metadata.Retriever != KnowledgeRetrieverBackendLexical ||
		!got.Metadata.FallbackToLexical ||
		got.Metadata.FallbackReason != "pgvector_adapter_unavailable" {
		t.Fatalf("fallback metadata = %+v", got.Metadata)
	}
	ev := rec.FindByEvent("wa_knowledge_retrieval")
	if ev == nil {
		t.Fatal("expected wa_knowledge_retrieval metric")
	}
	if ev.Fields["requested_backend"] != "pgvector" ||
		ev.Fields["retriever"] != KnowledgeRetrieverBackendLexical ||
		ev.Fields["fallback_to_lexical"] != true ||
		ev.Fields["fallback_reason"] != "pgvector_adapter_unavailable" {
		t.Fatalf("unexpected fallback metric: %+v", ev.Fields)
	}
}

func TestRetrieveKnowledgeContext_ExternalBackendConfigFailureUsesTypedFallbackReason(t *testing.T) {
	cases := []struct {
		name       string
		backend    string
		wantReason string
	}{
		{name: "qdrant", backend: "qdrant", wantReason: "qdrant_adapter_unavailable"},
		{name: "pinecone", backend: "pinecone", wantReason: "pinecone_adapter_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newKnowledgeIndexerTestDB(t)
			seedKnowledgeDocWithChunks(t, db, "Fallback handbook", "Design export fallback context")
			rec := &RecordingSink{}
			prev := SetMetricSink(rec)
			defer SetMetricSink(prev)

			got, err := RetrieveKnowledgeContext(context.Background(), db, tc.backend, KnowledgeRetrievalOptions{
				Query: "design export fallback",
			})
			if err != nil {
				t.Fatalf("RetrieveKnowledgeContext: %v", err)
			}
			if got.Metadata.Retriever != KnowledgeRetrieverBackendLexical ||
				!got.Metadata.FallbackToLexical ||
				got.Metadata.FallbackReason != tc.wantReason ||
				got.Metadata.DocumentsRetrieved != 1 {
				t.Fatalf("fallback metadata = %+v, want reason %q with lexical result", got.Metadata, tc.wantReason)
			}
			ev := rec.FindByEvent("wa_knowledge_retrieval")
			if ev == nil {
				t.Fatal("expected wa_knowledge_retrieval metric")
			}
			if ev.Fields["requested_backend"] != tc.backend ||
				ev.Fields["fallback_to_lexical"] != true ||
				ev.Fields["fallback_reason"] != tc.wantReason {
				t.Fatalf("unexpected fallback metric: %+v", ev.Fields)
			}
		})
	}
}

func TestRetrieveKnowledgeContext_EmptyIndexKeepsBackendFallbackMetadata(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	got, err := RetrieveKnowledgeContext(context.Background(), db, "qdrant", KnowledgeRetrievalOptions{
		Query: "html export fallback with no indexed documents",
	})
	if err != nil {
		t.Fatalf("RetrieveKnowledgeContext: %v", err)
	}
	if got.ContextXML != "" || got.Metadata.DocumentsRetrieved != 0 || got.Metadata.RAGEnabled {
		t.Fatalf("empty-index result should have no context/docs: %+v xml=%q", got.Metadata, got.ContextXML)
	}
	if got.Metadata.RequestedBackend != "qdrant" ||
		got.Metadata.Retriever != KnowledgeRetrieverBackendLexical ||
		!got.Metadata.FallbackToLexical ||
		got.Metadata.FallbackReason != "qdrant_adapter_unavailable" {
		t.Fatalf("empty-index fallback metadata = %+v", got.Metadata)
	}
	payload := got.Metadata.ToMap()
	if payload == nil ||
		payload["documents_retrieved"] != float64(0) ||
		payload["requested_backend"] != "qdrant" ||
		payload["retriever"] != KnowledgeRetrieverBackendLexical ||
		payload["fallback_to_lexical"] != true ||
		payload["fallback_reason"] != "qdrant_adapter_unavailable" {
		t.Fatalf("fallback metadata map = %#v", payload)
	}
	ev := rec.FindByEvent("wa_knowledge_retrieval")
	if ev == nil {
		t.Fatal("expected wa_knowledge_retrieval metric")
	}
	if ev.Fields["documents_retrieved"] != 0 ||
		ev.Fields["requested_backend"] != "qdrant" ||
		ev.Fields["fallback_to_lexical"] != true ||
		ev.Fields["fallback_reason"] != "qdrant_adapter_unavailable" {
		t.Fatalf("unexpected empty-index fallback metric: %+v", ev.Fields)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func uintString(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
