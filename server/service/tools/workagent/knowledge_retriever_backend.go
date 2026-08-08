package workagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	KnowledgeRetrieverBackendLexical     = "lexical"
	KnowledgeRetrieverBackendLocalVector = "local-vector"
)

type KnowledgeRetriever interface {
	Retrieve(ctx context.Context, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error)
}

type LexicalKnowledgeRetriever struct {
	DB *gorm.DB
}

type LocalVectorKnowledgeRetriever struct {
	DB *gorm.DB
}

func (r LexicalKnowledgeRetriever) Retrieve(ctx context.Context, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return KnowledgeRetrievalResult{}, err
		}
	}
	return RetrieveKnowledgeContextLexical(r.DB, opts)
}

func (r LocalVectorKnowledgeRetriever) Retrieve(ctx context.Context, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return KnowledgeRetrievalResult{}, err
		}
	}
	return RetrieveKnowledgeContextLocalVector(r.DB, opts)
}

func NewKnowledgeRetriever(db *gorm.DB, backend string) (KnowledgeRetriever, error) {
	switch strings.TrimSpace(strings.ToLower(backend)) {
	case "", KnowledgeRetrieverBackendLexical:
		return LexicalKnowledgeRetriever{DB: db}, nil
	case KnowledgeRetrieverBackendLocalVector:
		return LocalVectorKnowledgeRetriever{DB: db}, nil
	case "qdrant":
		retriever, err := NewQdrantKnowledgeRetrieverFromEnv(db)
		if err != nil {
			return nil, err
		}
		return retriever, nil
	case "pinecone":
		retriever, err := NewPineconeKnowledgeRetrieverFromEnv(db)
		if err != nil {
			return nil, err
		}
		return retriever, nil
	case "pgvector":
		retriever, err := NewPGVectorKnowledgeRetrieverFromEnv(db)
		if err != nil {
			return nil, err
		}
		return retriever, nil
	default:
		return nil, fmt.Errorf("unsupported knowledge retriever backend: %s", backend)
	}
}

func RetrieveKnowledgeContext(ctx context.Context, db *gorm.DB, backend string, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	start := time.Now()
	requestedBackend := strings.TrimSpace(strings.ToLower(backend))
	retriever, err := NewKnowledgeRetriever(db, backend)
	fallbackReason := ""
	if err != nil {
		if strings.TrimSpace(backend) != "" {
			fallbackReason = err.Error()
			retriever, err = NewKnowledgeRetriever(db, KnowledgeRetrieverBackendLexical)
		}
		if err != nil {
			return KnowledgeRetrievalResult{}, err
		}
	}
	result, err := retriever.Retrieve(ctx, opts)
	if err != nil {
		return result, err
	}
	effectiveRetriever := result.Metadata.Retriever
	if strings.TrimSpace(effectiveRetriever) == "" {
		switch retriever.(type) {
		case LocalVectorKnowledgeRetriever:
			effectiveRetriever = KnowledgeRetrieverBackendLocalVector
		case QdrantKnowledgeRetriever:
			effectiveRetriever = "qdrant"
		case PineconeKnowledgeRetriever:
			effectiveRetriever = "pinecone"
		case PGVectorKnowledgeRetriever:
			effectiveRetriever = "pgvector"
		default:
			effectiveRetriever = KnowledgeRetrieverBackendLexical
		}
	}
	result.Metadata.Retriever = effectiveRetriever
	result.Metadata.RequestedBackend = requestedBackend
	result.Metadata.FallbackToLexical = requestedBackend != "" && requestedBackend != effectiveRetriever && effectiveRetriever == KnowledgeRetrieverBackendLexical
	if result.Metadata.FallbackToLexical {
		switch {
		case strings.Contains(strings.ToLower(fallbackReason), "pgvector"):
			result.Metadata.FallbackReason = "pgvector_adapter_unavailable"
		case strings.Contains(strings.ToLower(fallbackReason), "qdrant"):
			result.Metadata.FallbackReason = "qdrant_adapter_unavailable"
		case strings.Contains(strings.ToLower(fallbackReason), "pinecone"):
			result.Metadata.FallbackReason = "pinecone_adapter_unavailable"
		case strings.TrimSpace(fallbackReason) != "":
			result.Metadata.FallbackReason = "retriever_backend_unavailable"
		}
	}
	EmitMetric("wa_knowledge_retrieval", map[string]any{
		"requested_backend":     requestedBackend,
		"retriever":             effectiveRetriever,
		"uid":                   opts.UID,
		"documents_retrieved":   result.Metadata.DocumentsRetrieved,
		"average_relevance":     result.Metadata.AverageRelevance,
		"project_id":            opts.ProjectID,
		"agent_mode":            strings.TrimSpace(opts.AgentMode),
		"context_chars":         len(result.ContextXML),
		"latency_ms":            time.Since(start).Milliseconds(),
		"fallback_to_lexical":   result.Metadata.FallbackToLexical,
		"fallback_reason":       result.Metadata.FallbackReason,
		"configured_backend":    requestedBackend != "",
		"knowledge_query_empty": strings.TrimSpace(opts.Query) == "",
	})
	return result, nil
}
