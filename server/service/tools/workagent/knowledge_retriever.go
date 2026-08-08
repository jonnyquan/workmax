package workagent

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"server/model"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

const (
	defaultKnowledgeRetrievalLimit     = 4
	defaultKnowledgeRetrievalMaxChunks = 500
	defaultKnowledgeContextMaxChars    = 6000
	localKnowledgeVectorDimensions     = 64
)

type KnowledgeRetrievalOptions struct {
	Query           string
	Limit           int
	MaxChunks       int
	ContextMaxChars int
	UID             uint
	ProjectID       uint
	TeamIDs         []uint64
	AgentMode       string
}

type KnowledgeRetrievalResult struct {
	ContextXML string                     `json:"-"`
	Metadata   KnowledgeRetrievalMetadata `json:"metadata"`
	Sources    []KnowledgeRetrievedSource `json:"sources"`
}

type KnowledgeRetrievalMetadata struct {
	RAGEnabled         bool                       `json:"rag_enabled"`
	DocumentsRetrieved int                        `json:"documents_retrieved"`
	AverageRelevance   float64                    `json:"average_relevance,omitempty"`
	VectorSources      []string                   `json:"vector_sources,omitempty"`
	Sources            []KnowledgeRetrievedSource `json:"sources,omitempty"`
	Retriever          string                     `json:"retriever,omitempty"`
	RequestedBackend   string                     `json:"requested_backend,omitempty"`
	FallbackToLexical  bool                       `json:"fallback_to_lexical,omitempty"`
	FallbackReason     string                     `json:"fallback_reason,omitempty"`
}

type KnowledgeRetrievedSource struct {
	DocumentID uint    `json:"document_id"`
	ChunkID    uint    `json:"chunk_id"`
	ChunkIndex int     `json:"chunk_index"`
	Title      string  `json:"title"`
	SourceURI  string  `json:"source_uri,omitempty"`
	MimeType   string  `json:"mime_type,omitempty"`
	Relevance  float64 `json:"relevance"`
}

type knowledgeChunkCandidate struct {
	ChunkID      uint
	DocumentID   uint
	ChunkIndex   int
	ContentText  string
	TokenCount   int
	Title        string
	SourceURI    string
	MimeType     string
	ScopeType    string
	ScopeID      uint
	AgentMode    string
	MetadataJSON string
}

type scoredKnowledgeChunk struct {
	candidate knowledgeChunkCandidate
	score     float64
}

func RetrieveKnowledgeContextLexical(db *gorm.DB, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	if db == nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("knowledge retrieval: nil db")
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return KnowledgeRetrievalResult{}, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultKnowledgeRetrievalLimit
	}
	maxChunks := opts.MaxChunks
	if maxChunks <= 0 {
		maxChunks = defaultKnowledgeRetrievalMaxChunks
	}
	queryTerms := tokenizeKnowledgeText(query)
	if len(queryTerms) == 0 {
		return KnowledgeRetrievalResult{}, nil
	}

	var candidates []knowledgeChunkCandidate
	queryDB := db.Table((&workagentModel.KnowledgeChunk{}).TableName()+" AS c").
		Select("c.id AS chunk_id, c.document_id, c.chunk_index, c.content_text, c.token_count, c.metadata_json, d.title, d.source_uri, d.mime_type, d.scope_type, d.scope_id, d.agent_mode").
		Joins("JOIN "+((&workagentModel.KnowledgeDocument{}).TableName())+" AS d ON d.id = c.document_id").
		Where("d.status = ? AND d.index_status = ? AND d.review_status = ?", workagentModel.KnowledgeDocumentStatusActive, workagentModel.KnowledgeIndexStatusIndexed, workagentModel.KnowledgeReviewStatusApproved).
		Where("d.agent_mode = '' OR d.agent_mode = ?", strings.TrimSpace(opts.AgentMode)).
		Order("d.updated_at DESC, c.document_id DESC, c.chunk_index ASC").
		Limit(maxChunks)
	if opts.ProjectID > 0 {
		queryDB = queryDB.Where(buildKnowledgeScopeSQL(opts.TeamIDs, true), buildKnowledgeScopeArgs(opts.ProjectID, opts.TeamIDs)...)
	} else {
		queryDB = queryDB.Where(buildKnowledgeScopeSQL(opts.TeamIDs, false), buildKnowledgeScopeArgs(0, opts.TeamIDs)...)
	}
	err := queryDB.Scan(&candidates).Error
	if err != nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("knowledge retrieval query: %w", err)
	}

	scored := make([]scoredKnowledgeChunk, 0, len(candidates))
	for _, c := range candidates {
		score := scoreKnowledgeCandidate(query, queryTerms, c)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredKnowledgeChunk{candidate: c, score: score})
	}
	if len(scored) == 0 {
		return KnowledgeRetrievalResult{}, nil
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			if scored[i].candidate.DocumentID == scored[j].candidate.DocumentID {
				return scored[i].candidate.ChunkIndex < scored[j].candidate.ChunkIndex
			}
			return scored[i].candidate.DocumentID > scored[j].candidate.DocumentID
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	contextMaxChars := opts.ContextMaxChars
	if contextMaxChars <= 0 {
		contextMaxChars = defaultKnowledgeContextMaxChars
	}
	return buildKnowledgeRetrievalResult(scored, contextMaxChars, KnowledgeRetrieverBackendLexical), nil
}

func RetrieveKnowledgeContextLocalVector(db *gorm.DB, opts KnowledgeRetrievalOptions) (KnowledgeRetrievalResult, error) {
	if db == nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("knowledge retrieval: nil db")
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return KnowledgeRetrievalResult{}, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultKnowledgeRetrievalLimit
	}
	maxChunks := opts.MaxChunks
	if maxChunks <= 0 {
		maxChunks = defaultKnowledgeRetrievalMaxChunks
	}
	queryVector := embedKnowledgeText(query)
	if isZeroKnowledgeVector(queryVector) {
		return KnowledgeRetrievalResult{}, nil
	}

	var candidates []knowledgeChunkCandidate
	queryDB := db.Table((&workagentModel.KnowledgeChunk{}).TableName()+" AS c").
		Select("c.id AS chunk_id, c.document_id, c.chunk_index, c.content_text, c.token_count, c.metadata_json, d.title, d.source_uri, d.mime_type, d.scope_type, d.scope_id, d.agent_mode").
		Joins("JOIN "+((&workagentModel.KnowledgeDocument{}).TableName())+" AS d ON d.id = c.document_id").
		Where("d.status = ? AND d.index_status = ? AND d.review_status = ?", workagentModel.KnowledgeDocumentStatusActive, workagentModel.KnowledgeIndexStatusIndexed, workagentModel.KnowledgeReviewStatusApproved).
		Where("d.agent_mode = '' OR d.agent_mode = ?", strings.TrimSpace(opts.AgentMode)).
		Order("d.updated_at DESC, c.document_id DESC, c.chunk_index ASC").
		Limit(maxChunks)
	if opts.ProjectID > 0 {
		queryDB = queryDB.Where(buildKnowledgeScopeSQL(opts.TeamIDs, true), buildKnowledgeScopeArgs(opts.ProjectID, opts.TeamIDs)...)
	} else {
		queryDB = queryDB.Where(buildKnowledgeScopeSQL(opts.TeamIDs, false), buildKnowledgeScopeArgs(0, opts.TeamIDs)...)
	}
	if err := queryDB.Scan(&candidates).Error; err != nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("knowledge vector retrieval query: %w", err)
	}

	scored := make([]scoredKnowledgeChunk, 0, len(candidates))
	for _, c := range candidates {
		score := cosineKnowledgeVectors(queryVector, embedKnowledgeText(c.Title+" "+c.ContentText))
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredKnowledgeChunk{candidate: c, score: score})
	}
	if len(scored) == 0 {
		return KnowledgeRetrievalResult{}, nil
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			if scored[i].candidate.DocumentID == scored[j].candidate.DocumentID {
				return scored[i].candidate.ChunkIndex < scored[j].candidate.ChunkIndex
			}
			return scored[i].candidate.DocumentID > scored[j].candidate.DocumentID
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	contextMaxChars := opts.ContextMaxChars
	if contextMaxChars <= 0 {
		contextMaxChars = defaultKnowledgeContextMaxChars
	}
	return buildKnowledgeRetrievalResult(scored, contextMaxChars, KnowledgeRetrieverBackendLocalVector), nil
}

func LoadKnowledgeTeamIDsForUser(db *gorm.DB, uid uint) []uint64 {
	if db == nil || uid == 0 {
		return nil
	}
	var rows []model.TeamMember
	if err := db.Select("team_id").
		Where("uid = ? AND status = ? AND deleted_at IS NULL", int(uid), model.TeamMemberStatusActive).
		Find(&rows).Error; err != nil {
		return nil
	}
	ids := make([]uint64, 0, len(rows))
	seen := map[uint64]bool{}
	for _, row := range rows {
		if row.TeamID == 0 || seen[row.TeamID] {
			continue
		}
		seen[row.TeamID] = true
		ids = append(ids, row.TeamID)
	}
	return ids
}

func buildKnowledgeScopeSQL(teamIDs []uint64, includeProject bool) string {
	parts := []string{"d.scope_type = 'global'"}
	if includeProject {
		parts = append(parts, "(d.scope_type = 'project' AND d.scope_id = ?)")
	}
	if len(teamIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(teamIDs)), ",")
		parts = append(parts, "(d.scope_type = 'team' AND d.scope_id IN ("+placeholders+"))")
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func buildKnowledgeScopeArgs(projectID uint, teamIDs []uint64) []interface{} {
	args := []interface{}{}
	if projectID > 0 {
		args = append(args, projectID)
	}
	for _, id := range teamIDs {
		args = append(args, id)
	}
	return args
}

func buildKnowledgeRetrievalResult(scored []scoredKnowledgeChunk, contextMaxChars int, retriever string) KnowledgeRetrievalResult {
	if len(scored) == 0 {
		return KnowledgeRetrievalResult{}
	}
	sources := make([]KnowledgeRetrievedSource, 0, len(scored))
	vectorSources := make([]string, 0, len(scored))
	total := 0.0
	writtenChars := 0
	var b strings.Builder
	if strings.TrimSpace(retriever) == "" {
		retriever = KnowledgeRetrieverBackendLexical
	}
	fmt.Fprintf(&b, `<knowledge-context retriever="%s" source="workagent_knowledge">`, escapeXMLAttr(retriever))
	b.WriteString("\nUse these retrieved knowledge snippets only when they are relevant to the user's current request. Cite the source title when it affects the answer.\n")
	for i, item := range scored {
		c := item.candidate
		relevance := math.Round(item.score*1000) / 1000
		source := KnowledgeRetrievedSource{
			DocumentID: c.DocumentID,
			ChunkID:    c.ChunkID,
			ChunkIndex: c.ChunkIndex,
			Title:      c.Title,
			SourceURI:  c.SourceURI,
			MimeType:   c.MimeType,
			Relevance:  relevance,
		}
		sources = append(sources, source)
		vectorSources = append(vectorSources, c.Title)
		total += relevance

		text := strings.TrimSpace(c.ContentText)
		if remaining := contextMaxChars - writtenChars; remaining <= 0 {
			text = ""
		} else if len(text) > remaining {
			text = truncateStringBytes(text, remaining)
		}
		if text == "" {
			continue
		}
		writtenChars += len(text)
		fmt.Fprintf(&b, `<source index="%d" document_id="%d" chunk_id="%d" chunk_index="%d" title="%s" relevance="%.3f">`,
			i+1, c.DocumentID, c.ChunkID, c.ChunkIndex, escapeXMLAttr(c.Title), relevance)
		b.WriteString("\n")
		b.WriteString(escapeXMLText(text))
		b.WriteString("\n</source>\n")
	}
	b.WriteString("</knowledge-context>")

	average := 0.0
	if len(sources) > 0 {
		average = math.Round((total/float64(len(sources)))*1000) / 1000
	}
	return KnowledgeRetrievalResult{
		ContextXML: b.String(),
		Metadata: KnowledgeRetrievalMetadata{
			RAGEnabled:         true,
			DocumentsRetrieved: len(sources),
			AverageRelevance:   average,
			VectorSources:      dedupeStrings(vectorSources),
			Sources:            sources,
			Retriever:          retriever,
		},
		Sources: sources,
	}
}

func scoreKnowledgeCandidate(query string, queryTerms map[string]float64, c knowledgeChunkCandidate) float64 {
	textTerms := tokenizeKnowledgeText(c.Title + " " + c.ContentText)
	if len(textTerms) == 0 {
		return 0
	}
	matchWeight := 0.0
	totalWeight := 0.0
	for term, weight := range queryTerms {
		totalWeight += weight
		if _, ok := textTerms[term]; ok {
			matchWeight += weight
		}
	}
	if totalWeight <= 0 || matchWeight <= 0 {
		return 0
	}
	score := matchWeight / totalWeight
	lowerText := strings.ToLower(c.Title + "\n" + c.ContentText)
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery != "" && strings.Contains(lowerText, lowerQuery) {
		score += 0.25
	}
	if titleTerms := tokenizeKnowledgeText(c.Title); len(titleTerms) > 0 {
		titleMatches := 0.0
		for term := range queryTerms {
			if _, ok := titleTerms[term]; ok {
				titleMatches++
			}
		}
		if titleMatches > 0 {
			score += 0.1 * (titleMatches / float64(len(queryTerms)))
		}
	}
	if score > 1 {
		return 1
	}
	return score
}

func embedKnowledgeText(input string) []float64 {
	terms := tokenizeKnowledgeText(input)
	vector := make([]float64, localKnowledgeVectorDimensions)
	for term, weight := range terms {
		idx := stableKnowledgeHash(term) % uint32(len(vector))
		vector[idx] += weight
	}
	norm := 0.0
	for _, v := range vector {
		norm += v * v
	}
	if norm == 0 {
		return vector
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] = vector[i] / norm
	}
	return vector
}

func stableKnowledgeHash(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func cosineKnowledgeVectors(a []float64, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	score := 0.0
	for i := range a {
		score += a[i] * b[i]
	}
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func isZeroKnowledgeVector(vector []float64) bool {
	for _, v := range vector {
		if v != 0 {
			return false
		}
	}
	return true
}

func tokenizeKnowledgeText(input string) map[string]float64 {
	input = strings.ToLower(strings.TrimSpace(input))
	out := map[string]float64{}
	var token []rune
	flush := func() {
		if len(token) == 0 {
			return
		}
		s := string(token)
		token = token[:0]
		if isKnowledgeStopword(s) {
			return
		}
		out[s] = 1
		if containsCJK(s) {
			addCJKNgrams(out, []rune(s))
		}
	}
	for _, r := range input {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			token = append(token, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func addCJKNgrams(out map[string]float64, runes []rune) {
	for n := 2; n <= 3; n++ {
		if len(runes) < n {
			continue
		}
		for i := 0; i+n <= len(runes); i++ {
			out[string(runes[i:i+n])] = 0.8
		}
	}
}

func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

func isKnowledgeStopword(s string) bool {
	if len(s) <= 1 && !containsCJK(s) {
		return true
	}
	switch s {
	case "the", "and", "for", "with", "that", "this", "from", "into", "about", "what", "when", "where", "how", "why", "are", "was", "were", "you", "your", "请", "的", "了", "和", "及":
		return true
	default:
		return false
	}
}

func escapeXMLText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func escapeXMLAttr(s string) string {
	return strings.ReplaceAll(escapeXMLText(s), `"`, "&quot;")
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func truncateStringBytes(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	n := 0
	for i := range s {
		if i > limit {
			break
		}
		n = i
	}
	if n <= 0 {
		return ""
	}
	return strings.TrimSpace(s[:n])
}

func (m KnowledgeRetrievalMetadata) IsEmpty() bool {
	return !m.RAGEnabled && m.DocumentsRetrieved <= 0 && !m.hasBackendTrace()
}

func (m KnowledgeRetrievalMetadata) ToMap() map[string]interface{} {
	if m.IsEmpty() {
		return nil
	}
	var out map[string]interface{}
	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func (m KnowledgeRetrievalMetadata) hasBackendTrace() bool {
	return strings.TrimSpace(m.Retriever) != "" ||
		strings.TrimSpace(m.RequestedBackend) != "" ||
		m.FallbackToLexical ||
		strings.TrimSpace(m.FallbackReason) != ""
}
