package workagent

import (
	"strings"
	"testing"

	"server/model"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

func TestRetrieveKnowledgeContextLexical_RanksAndFormatsSources(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db,
		"Design system rules",
		"Use a quiet operational layout, stable artboards, image and HTML export checks, and concise designer-facing copy.",
	)
	seedKnowledgeDocWithChunks(t, db,
		"Billing policy",
		"Credits are reserved before model execution and settled after the result reports cost.",
	)

	got, err := RetrieveKnowledgeContextLexical(db, KnowledgeRetrievalOptions{
		Query: "HTML export design artboards",
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("RetrieveKnowledgeContextLexical: %v", err)
	}
	if got.Metadata.DocumentsRetrieved == 0 || !got.Metadata.RAGEnabled {
		t.Fatalf("metadata not populated: %+v", got.Metadata)
	}
	if got.Sources[0].Title != "Design system rules" {
		t.Fatalf("top source = %q, want design doc", got.Sources[0].Title)
	}
	if !strings.Contains(got.ContextXML, `<knowledge-context retriever="lexical"`) ||
		!strings.Contains(got.ContextXML, `Design system rules`) ||
		!strings.Contains(got.ContextXML, `HTML export checks`) {
		t.Fatalf("context XML missing retrieved source: %s", got.ContextXML)
	}
}

func TestRetrieveKnowledgeContextLexical_IgnoresArchivedOrUnindexedDocs(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db,
		"Archived design rules",
		"HTML export blocker handling",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.Status = workagentModel.KnowledgeDocumentStatusArchived
		},
	)
	seedKnowledgeDocWithChunks(t, db,
		"Pending design rules",
		"HTML export blocker handling",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.IndexStatus = workagentModel.KnowledgeIndexStatusPending
		},
	)
	seedKnowledgeDocWithChunks(t, db,
		"Unapproved design rules",
		"HTML export blocker handling",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.ReviewStatus = workagentModel.KnowledgeReviewStatusPending
		},
	)

	got, err := RetrieveKnowledgeContextLexical(db, KnowledgeRetrievalOptions{Query: "HTML export"})
	if err != nil {
		t.Fatalf("RetrieveKnowledgeContextLexical: %v", err)
	}
	if got.Metadata.RAGEnabled || got.ContextXML != "" {
		t.Fatalf("retrieved inactive docs: %+v xml=%s", got.Metadata, got.ContextXML)
	}
}

func TestRetrieveKnowledgeContextLexical_FiltersProjectAndAgentMode(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db,
		"Global PPT rules",
		"HTML export design guidance",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.AgentMode = "ppt"
		},
	)
	seedKnowledgeDocWithChunks(t, db,
		"Project social rules",
		"HTML export social ad guidance",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.ScopeType = "project"
			doc.ScopeID = 77
			doc.AgentMode = "socialAd"
		},
	)
	seedKnowledgeDocWithChunks(t, db,
		"Other project rules",
		"HTML export design guidance should not leak",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.ScopeType = "project"
			doc.ScopeID = 88
		},
	)

	got, err := RetrieveKnowledgeContextLexical(db, KnowledgeRetrievalOptions{
		Query:     "HTML export guidance",
		ProjectID: 77,
		AgentMode: "ppt",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("RetrieveKnowledgeContextLexical: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Title != "Global PPT rules" {
		t.Fatalf("filtered sources = %+v", got.Sources)
	}
}

func TestRetrieveKnowledgeContextLexical_IncludesTeamScopedDocs(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	seedKnowledgeDocWithChunks(t, db,
		"Team design rules",
		"Shared team HTML export guidance",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.ScopeType = "team"
			doc.ScopeID = 7001
		},
	)
	seedKnowledgeDocWithChunks(t, db,
		"Other team rules",
		"Shared team HTML export guidance should not leak",
		func(doc *workagentModel.KnowledgeDocument) {
			doc.ScopeType = "team"
			doc.ScopeID = 9001
		},
	)

	got, err := RetrieveKnowledgeContextLexical(db, KnowledgeRetrievalOptions{
		Query:   "team HTML export guidance",
		TeamIDs: []uint64{7001},
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("RetrieveKnowledgeContextLexical: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Title != "Team design rules" {
		t.Fatalf("team filtered sources = %+v", got.Sources)
	}
}

func TestLoadKnowledgeTeamIDsForUser(t *testing.T) {
	db := newKnowledgeIndexerTestDB(t)
	rows := []model.TeamMember{
		{TeamID: 7001, UID: 42, Role: model.TeamMemberRoleMember, Status: model.TeamMemberStatusActive},
		{TeamID: 7001, UID: 42, Role: model.TeamMemberRoleMember, Status: model.TeamMemberStatusActive},
		{TeamID: 9001, UID: 42, Role: model.TeamMemberRoleMember, Status: model.TeamMemberStatusRemoved},
		{TeamID: 8001, UID: 99, Role: model.TeamMemberRoleMember, Status: model.TeamMemberStatusActive},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed team members: %v", err)
	}
	got := LoadKnowledgeTeamIDsForUser(db, 42)
	if len(got) != 1 || got[0] != 7001 {
		t.Fatalf("team ids = %+v", got)
	}
}

func seedKnowledgeDocWithChunks(t *testing.T, db *gorm.DB, title string, content string, overrides ...func(*workagentModel.KnowledgeDocument)) *workagentModel.KnowledgeChunk {
	t.Helper()
	doc := workagentModel.KnowledgeDocument{
		Title:        title,
		ContentText:  content,
		IndexStatus:  workagentModel.KnowledgeIndexStatusIndexed,
		ReviewStatus: workagentModel.KnowledgeReviewStatusApproved,
		Status:       workagentModel.KnowledgeDocumentStatusActive,
	}
	for _, override := range overrides {
		override(&doc)
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk := workagentModel.KnowledgeChunk{
		DocumentID:  doc.Id,
		ChunkIndex:  0,
		ContentText: content,
		ContentHash: "hash",
		TokenCount:  len(strings.Fields(content)),
	}
	if err := db.Create(&chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	return &chunk
}
