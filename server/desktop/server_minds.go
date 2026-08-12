//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// The mind endpoints: list/create/select over w_desktop_mind, and the two
// that make a mind more than a row — status, which reads its memory out of
// the knowledge store, and feed, which writes new memory into it.
//
// All five are identity-scoped reads/writes of local state; none of them
// touches the cloud. Feed is the minimal honest shape of "training": the
// material becomes retrievable knowledge under the mind's mark, which the
// very next local turn can draw on.

const (
	maxMindBodyBytes     = 4 << 10
	maxMindFeedBodyBytes = 1 << 20 // 1 MiB of text, bounded by the route policy too

	maxMindMaterialTitle = 80
	maxMindMaterialText  = 1 << 20
)

// MindKnowledge is the mind-shaped surface of the knowledge store: feed a
// mind material, and read back what it has been fed. Defined separately from
// KnowledgeIndex so the existing fakes stay valid; the concrete
// implementation is the lazy wiring around *knowledge.Indexer, discovered by
// interface assertion. A sidecar without it (no cgo, RAG off) still lists and
// switches minds — only feeding and the memory listing degrade.
type MindKnowledge interface {
	IndexMindMaterial(ctx context.Context, uid uint64, mindID, title, text string) (int, error)
	MindSources(ctx context.Context, uid uint64, mindID string) ([]MindSourceStat, error)
	// ForgetMind removes everything a mind was taught. Not uid-scoped, for the
	// same reason the store's other delete paths are not: a mind id is unique
	// on this machine, and memory left behind under an identity the user has
	// stopped using would be text they deleted that is still on disk.
	ForgetMind(ctx context.Context, mindID string) (int, error)
}

// MindSourceStat is one fed material as the status endpoint reports it.
type MindSourceStat struct {
	Title     string `json:"title"`
	Chunks    int    `json:"chunks"`
	IndexedAt int64  `json:"indexed_at"` // unix seconds
}

// MindListDTO is GET /minds.
type MindListDTO struct {
	Items []Mind `json:"items"`
	Count int    `json:"count"`
}

// MindModelDTO answers "which model does this mind think with" — the mind's
// own override when it has one, the identity's model route otherwise. Source
// says which of the two answered, so the UI never has to guess whether
// "claude-x" came from the mind or from the account.
type MindModelDTO struct {
	Source string `json:"source"` // "mind" | "identity"
	Label  string `json:"label"`
	Route  string `json:"route"` // "local" | "official"
}

// MindStatusDTO is GET /minds/:id/status.
type MindStatusDTO struct {
	Mind      Mind             `json:"mind"`
	Model     MindModelDTO     `json:"model"`
	Retrieval string           `json:"retrieval"` // "local" | "unavailable"
	Memory    MindMemoryStatus `json:"memory"`
}

// MindMemoryStatus is the memory half of the status: everything the mind has
// been fed, and the chunk total the store reports for its mark.
type MindMemoryStatus struct {
	Chunks  int              `json:"chunks"`
	Sources []MindSourceStat `json:"sources"`
}

// MindFeedPut is the feed body: a title the memory listing can show, and the
// material itself.
type MindFeedPut struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// MindFeedResult reports what a feed wrote. Chunks is the number the status
// endpoint will show for the material, so the UI can update without a second
// round trip.
type MindFeedResult struct {
	Fed    bool   `json:"fed"`
	Title  string `json:"title"`
	Chunks int    `json:"chunks"`
}

// mindKnowledge resolves the knowledge store's mind surface, or nil when this
// build/run has no RAG at all.
func (s *Server) mindKnowledge() MindKnowledge {
	if s.cfg.KnowledgeIndex == nil {
		return nil
	}
	mk, ok := s.cfg.KnowledgeIndex.(MindKnowledge)
	if !ok {
		return nil
	}
	return mk
}

func (s *Server) handleListMinds(c *gin.Context) {
	if s.cfg.Minds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "minds unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	minds, err := s.cfg.Minds.List(identity.UID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mind list failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, MindListDTO{Items: minds, Count: len(minds)})
}

func (s *Server) handleCreateMind(c *gin.Context) {
	if s.cfg.Minds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "minds unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxMindBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if int64(len(raw)) > maxMindBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "mind request body too large"})
		return
	}
	in, err := DecodeMindPut(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mind json"})
		return
	}
	mind, err := s.cfg.Minds.Create(identity.UID, in)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errMindLimit) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, mind)
}

func (s *Server) handleSelectMind(c *gin.Context) {
	if s.cfg.Minds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "minds unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	if err := s.cfg.Minds.Select(identity.UID, c.Param("id")); err != nil {
		writeMindError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"selected": true})
}

func (s *Server) handleMindStatus(c *gin.Context) {
	if s.cfg.Minds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "minds unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	mind, err := s.cfg.Minds.Get(identity.UID, c.Param("id"))
	if err != nil {
		writeMindError(c, err)
		return
	}

	status := MindStatusDTO{
		Mind:      mind,
		Model:     s.mindModel(identity.UID, mind),
		Retrieval: "unavailable",
		Memory:    MindMemoryStatus{Sources: []MindSourceStat{}},
	}
	if mk := s.mindKnowledge(); mk != nil {
		status.Retrieval = "local"
		sources, err := mk.MindSources(c.Request.Context(), identity.UID, mind.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "mind memory read failed"})
			return
		}
		status.Memory.Sources = sources
		for _, source := range sources {
			status.Memory.Chunks += source.Chunks
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, status)
}

// mindModel resolves the model a mind thinks with: its own override when set,
// otherwise the identity's route — local endpoint model, the picked official
// model, or the account default, in that order of specificity.
func (s *Server) mindModel(uid uint64, mind Mind) MindModelDTO {
	if mind.ModelOverride != "" {
		route := "official"
		if s.cfg.ModelSettings != nil {
			if dto, err := s.cfg.ModelSettings.Get(uid); err == nil {
				route = dto.PreferredRoute
			}
		}
		return MindModelDTO{Source: "mind", Label: mind.ModelOverride, Route: route}
	}
	if s.cfg.ModelSettings == nil {
		return MindModelDTO{Source: "identity", Label: "account default", Route: "official"}
	}
	dto, err := s.cfg.ModelSettings.Get(uid)
	if err != nil {
		return MindModelDTO{Source: "identity", Label: "account default", Route: "official"}
	}
	if dto.PreferredRoute == "local" {
		if dto.Local.ModelID != "" {
			return MindModelDTO{Source: "identity", Label: dto.Local.ModelID, Route: "local"}
		}
		if dto.OfficialModelID != "" {
			return MindModelDTO{Source: "identity", Label: dto.OfficialModelID, Route: "official"}
		}
		return MindModelDTO{Source: "identity", Label: "account default", Route: "official"}
	}
	if dto.OfficialModelID != "" {
		return MindModelDTO{Source: "identity", Label: dto.OfficialModelID, Route: "official"}
	}
	return MindModelDTO{Source: "identity", Label: "account default", Route: "official"}
}

// handleFeedMind is the training endpoint, at its smallest real size: one
// titled document becomes knowledge chunks marked as this mind's memory,
// retrievable from the next local turn on. It needs the embedding model, so
// it is the one mind endpoint that can be off on a RAG-less build — said as
// 503, not as a silent no-op.
func (s *Server) handleFeedMind(c *gin.Context) {
	if s.cfg.Minds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "minds unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	mind, err := s.cfg.Minds.Get(identity.UID, c.Param("id"))
	if err != nil {
		writeMindError(c, err)
		return
	}
	mk := s.mindKnowledge()
	if mk == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "local retrieval is unavailable on this build"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxMindFeedBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if int64(len(raw)) > maxMindFeedBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "mind feed body too large"})
		return
	}
	in, err := decodeMindFeedPut(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	chunks, err := mk.IndexMindMaterial(c.Request.Context(), identity.UID, mind.ID, in.Title, in.Text)
	if err != nil {
		// "Nothing on this machine can embed right now" is a different answer
		// from "this material is bad", and the user acts on them differently.
		// Both are 503 — come back later — rather than a 502 that reads as a
		// rejected document.
		switch {
		case errors.Is(err, errKnowledgeAssetsFetching):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the embedding model is still downloading; try again shortly"})
		case errors.Is(err, errKnowledgeAssetsUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "this build has no embedding model, so nothing can be learned yet"})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": "material could not be indexed"})
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, MindFeedResult{Fed: true, Title: in.Title, Chunks: chunks})
}

// handleUpdateMind replaces a mind's describable parts. Without it a role
// hint is written once and forever, and correcting one would mean creating a
// new mind — which means losing everything the old one was taught.
func (s *Server) handleUpdateMind(c *gin.Context) {
	if s.cfg.Minds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "minds unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxMindBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if int64(len(raw)) > maxMindBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "mind request body too large"})
		return
	}
	in, err := DecodeMindPut(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mind json"})
		return
	}
	mind, err := s.cfg.Minds.Update(identity.UID, c.Param("id"), in)
	if err != nil {
		writeMindError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, mind)
}

// handleDeleteMind removes a mind and everything it was taught.
//
// The memory goes FIRST and the row only if that succeeded. The two live in
// different stores and cannot share a transaction, so one of the two orders
// has to be chosen deliberately: deleting the row first and failing on the
// chunks would leave text on disk that nothing can ever reach or name again,
// while failing this way leaves the mind intact and the delete retryable.
//
// A sidecar with no knowledge store at all still deletes the row, because
// there is no memory for it to be inconsistent with.
func (s *Server) handleDeleteMind(c *gin.Context) {
	if s.cfg.Minds == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "minds unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	id := c.Param("id")
	// Existence and the last-mind rule are checked before anything is erased,
	// so a refused delete never costs a mind its memory.
	if _, err := s.cfg.Minds.Get(identity.UID, id); err != nil {
		writeMindError(c, err)
		return
	}
	if mk := s.mindKnowledge(); mk != nil {
		if err := s.cfg.Minds.CanDelete(identity.UID, id); err != nil {
			writeMindError(c, err)
			return
		}
		if _, err := mk.ForgetMind(c.Request.Context(), id); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "mind memory could not be erased"})
			return
		}
	}
	if err := s.cfg.Minds.Delete(identity.UID, id); err != nil {
		writeMindError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func writeMindError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errMindLastOne):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	// The validation vocabulary. These reach here from Update, which shares
	// its normalizers with Create — where they were already answered as 400.
	// Falling through to 500 would tell the user their machine broke when what
	// happened is that they left the name blank.
	case errors.Is(err, errMindID),
		errors.Is(err, errMindName),
		errors.Is(err, errMindDescription),
		errors.Is(err, errMindRoleHint),
		errors.Is(err, errMindModel):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, errMindNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mind request failed"})
	}
}

// decodeMindFeedPut validates the feed body: a displayable title and a
// non-empty text, both bounded. The title becomes part of the knowledge
// source_id, so it is single-line and control-free by construction here, not
// by convention downstream.
func decodeMindFeedPut(raw []byte) (MindFeedPut, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in MindFeedPut
	if err := dec.Decode(&in); err != nil || dec.More() {
		return MindFeedPut{}, errors.New("invalid mind feed json")
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || !utf8.ValidString(in.Title) ||
		utf8.RuneCountInString(in.Title) > maxMindMaterialTitle ||
		strings.ContainsAny(in.Title, "\x00\n\r\t") {
		return MindFeedPut{}, errors.New("mind: invalid material title")
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || !utf8.ValidString(in.Text) || len(in.Text) > maxMindMaterialText {
		return MindFeedPut{}, errors.New("mind: invalid material text")
	}
	return in, nil
}
