package workagent

import (
	"fmt"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

// ThreadRepository owns every chat_thread column write outside of the
// plan_history slice (PlanRepository handles that). Pulled out of the
// HandleAgentChat / saveAgentConversation / recordAgentError /
// updateThreadName / updateThreadStatistics quintet — five different
// call sites, five different ad-hoc Updates(map[string]interface{})
// calls, three of which forgot the matching ThreadCache.Invalidate.
//
// The repo bakes cache invalidation into every state-changing method
// so a future writer can't ship "the persisted change took 5 minutes
// to show up" — that exact bug shipped twice on the agent_session_id
// path before this consolidation.
type ThreadRepository struct {
	db    *gorm.DB
	cache *ThreadCache
}

// NewThreadRepository binds the repo to a *gorm.DB and the package
// singleton ThreadCache. Tests that need a per-test cache use
// NewThreadRepositoryWithCache instead.
func NewThreadRepository(db *gorm.DB) *ThreadRepository {
	return &ThreadRepository{db: db, cache: GetThreadCache()}
}

// NewThreadRepositoryWithCache lets tests inject a fresh cache so
// invalidation assertions don't bleed across cases. Production callers
// should prefer NewThreadRepository / DefaultThreadRepository.
func NewThreadRepositoryWithCache(db *gorm.DB, cache *ThreadCache) *ThreadRepository {
	return &ThreadRepository{db: db, cache: cache}
}

// DefaultThreadRepository returns a repo bound to the system DB and the
// shared ThreadCache. Convenience for handler code with no other reason
// to plumb a *gorm.DB.
func DefaultThreadRepository() *ThreadRepository {
	return NewThreadRepository(globals.GraDBs["system"])
}

// PersistAgentMode writes a mode switch and clears the SDK session so
// the next turn bootstraps fresh under the new prompt. Lossy on
// context but correct on identity — the alternative is a feature that
// lies (the user toggles mode, the agent keeps acting like the old
// one because the cached agent_session_id resumes against the old
// mode's prompt). The matching ThreadCache.Invalidate is here, not at
// the call site, so a future caller can't forget it.
func (r *ThreadRepository) PersistAgentMode(threadID uint, mode string) error {
	if mode == "" {
		return nil
	}
	updates := map[string]interface{}{
		"agent_mode":               mode,
		"agent_session_id":         "",
		"agent_session_created_at": nil,
	}
	if err := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("persist agent mode: %w", err)
	}
	r.cache.Invalidate(fmt.Sprint(threadID))
	return nil
}

// UpdateModelTier persists a model-tier change (e.g. work-pro →
// work-plus). Cache invalidation is mandatory: a stale cached row
// routes the next turn against the old model tier despite the
// persisted change.
func (r *ThreadRepository) UpdateModelTier(threadID uint, model string) error {
	if model == "" {
		return nil
	}
	if err := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Update("model", model).Error; err != nil {
		return fmt.Errorf("update model tier: %w", err)
	}
	r.cache.Invalidate(fmt.Sprint(threadID))
	return nil
}

// TouchTimestamp bumps updated_at without touching any other column —
// used by the error path so a failed turn still moves the thread to
// the top of the user's list. No cache invalidate: the cached row's
// only "fresh" field is updated_at and the Sort-by-updated UI doesn't
// read from cache.
func (r *ThreadRepository) TouchTimestamp(threadID uint, now time.Time) error {
	if err := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Update("updated_at", now).Error; err != nil {
		return fmt.Errorf("touch timestamp: %w", err)
	}
	return nil
}

// RenameThread persists a thread name. Used by the AI-naming path
// when an "Untitled" thread gets its first generated title.
func (r *ThreadRepository) RenameThread(threadID uint, name string) error {
	if name == "" {
		return nil
	}
	if err := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Update("name", name).Error; err != nil {
		return fmt.Errorf("rename thread: %w", err)
	}
	r.cache.Invalidate(fmt.Sprint(threadID))
	return nil
}

// SetProjectForOwner binds a thread to a project and invalidates the
// cached thread row. Project-aware agent tools read project_id from
// getChatThread, so a stale cache would keep routing the next turn
// against the previous project scope.
func (r *ThreadRepository) SetProjectForOwner(threadID uint, uid uint, projectID uint, updatedAt time.Time) error {
	if threadID == 0 || uid == 0 || projectID == 0 {
		return fmt.Errorf("set project for owner: threadID, uid, projectID must all be > 0")
	}
	res := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ? AND uid = ?", threadID, uid).
		Updates(map[string]interface{}{
			"project_id": projectID,
			"updated_at": updatedAt,
		})
	if res.Error != nil {
		return fmt.Errorf("set project for owner: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	r.cache.Invalidate(fmt.Sprint(threadID))
	return nil
}

// ThreadStatistics is the bag of per-turn aggregates the chat_thread
// row carries. Computed by the caller from chat_message + thread_file
// counts; the repo just persists. UpdatedAt is explicit (rather than
// a free-running NOW()) so tests can pin the value and the chat list
// sort order stays deterministic across the per-turn write.
type ThreadStatistics struct {
	MessageCount int64
	Preview      string
	FileCount    int64
	UpdatedAt    time.Time
}

// SetStatistics persists per-turn aggregates and invalidates the
// cache. Without the invalidate, the per-turn stats and preview stay
// frozen for up to 5 minutes after each user message — the exact bug
// that motivated the inline cache.Invalidate at the previous call
// site (see updateThreadStatistics' history).
func (r *ThreadRepository) SetStatistics(threadID uint, stats ThreadStatistics) error {
	updates := map[string]interface{}{
		"message_count": stats.MessageCount,
		"msg_preview":   stats.Preview,
		"file_count":    stats.FileCount,
		"updated_at":    stats.UpdatedAt,
	}
	if err := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("set statistics: %w", err)
	}
	r.cache.Invalidate(fmt.Sprint(threadID))
	return nil
}

// LoadByIDForOwner reads a chat_thread row by id only when the row's
// uid matches the calling user. Returns gorm.ErrRecordNotFound for
// BOTH "row doesn't exist" and "row exists under a different uid" so
// callers can map the same error to the same 404 — never leak
// existence on cross-tenant probes (CWE-639 IDOR defence). The DB
// query carries the uid in the WHERE rather than loading-then-
// comparing so a forgotten ownership check at the call site can't
// accidentally serve another user's thread.
//
// uid==0 means "no caller" / system path — refuse to short-circuit
// the ownership predicate, force callers to provide a real uid.
func (r *ThreadRepository) LoadByIDForOwner(threadID uint, uid uint) (*workagentModel.ChatThread, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	thread := &workagentModel.ChatThread{}
	if err := r.db.Where("id = ? AND uid = ?", threadID, uid).First(thread).Error; err != nil {
		return nil, err
	}
	return thread, nil
}

// LoadByUUIDForOwner is the LoadByIDForOwner equivalent keyed by
// thread.uuid (v4 random) instead of the numeric primary key. Used
// by the SPA's deep-history-URL landing path (GetConversationByUuid)
// where the URL carries the UUID and the numeric id isn't known
// client-side. Same uid-in-WHERE defence as LoadByIDForOwner.
//
// agentType narrows results to the work-agent surface — avoids
// returning a thread that lives under a sibling tool's agent_type
// even if the UUIDs (theoretically) collide.
func (r *ThreadRepository) LoadByUUIDForOwner(threadUUID string, uid uint, agentType string) (*workagentModel.ChatThread, error) {
	if uid == 0 || threadUUID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	thread := &workagentModel.ChatThread{}
	q := r.db.Where("uuid = ? AND uid = ?", threadUUID, uid)
	if agentType != "" {
		q = q.Where("agent_type = ?", agentType)
	}
	if err := q.First(thread).Error; err != nil {
		return nil, err
	}
	return thread, nil
}

// ApplyUpdatesForOwner persists a map[string]interface{} of column
// updates to one chat_thread row, scoped to the given uid in the
// WHERE clause. Atomic in the sense that an attacker can't race the
// load+update sequence to write through a row they don't own — the
// ownership predicate is part of the same UPDATE, not a separate
// SELECT.
//
// Returns gorm.ErrRecordNotFound when no rows match (id missing OR
// uid mismatch), same generic shape as LoadByIDForOwner — caller
// branches on errors.Is to render the right 404 without an
// existence oracle.
//
// Bakes in cache invalidation so a future caller can't ship the
// "settings updated, next chat turn still uses the old prompt for 5
// minutes" bug. Empty updates map is a no-op (no DB write, no cache
// bust) — the alternative is a `WHERE id=? AND uid=?` UPDATE with no
// SET clause, which gorm would reject anyway.
func (r *ThreadRepository) ApplyUpdatesForOwner(threadID uint, uid uint, updates map[string]interface{}) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	if len(updates) == 0 {
		return nil
	}
	res := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ? AND uid = ?", threadID, uid).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("apply updates for owner: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	r.cache.Invalidate(fmt.Sprint(threadID))
	return nil
}

// SetVisibility flips a thread's is_public flag. Owner-only via the
// embedded uid predicate. Callers that need the same flow must use
// this method (rather than reaching for ApplyUpdatesForOwner with a
// one-key map) so the share-flag toggle has a single, named entry
// point in the cache-invalidation contract — easier for ops/audit
// log readers to spot than a generic "settings update" event.
func (r *ThreadRepository) SetVisibility(threadID uint, uid uint, isPublic bool) error {
	return r.ApplyUpdatesForOwner(threadID, uid, map[string]interface{}{
		"is_public": isPublic,
	})
}

// LoadByUUID reads a thread by its UUID without a uid scope. Used by
// the PUBLIC share endpoints (conversation_share.go) where access is
// granted purely by knowing the v4-random UUID; the handler's own
// IsPublic check decides whether the thread is currently exposed.
//
// Distinct method (vs. an "include unauthenticated" flag on
// LoadByUUIDForOwner) so the audit trail is clear: any call site
// reaching for this is opting into the no-uid path explicitly. A
// future grep for "LoadByUUID(" in handler code surfaces every
// public-surface lookup at a glance.
func (r *ThreadRepository) LoadByUUID(threadUUID string) (*workagentModel.ChatThread, error) {
	if threadUUID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	thread := &workagentModel.ChatThread{}
	if err := r.db.Where("uuid = ?", threadUUID).First(thread).Error; err != nil {
		return nil, err
	}
	return thread, nil
}

// LoadByID reads a thread by its primary key without a uid scope.
// Used by the public message-detail endpoint to fetch the thread that
// owns a previously-located message — the message UUID was the auth
// gate, so we already know the caller is allowed to see thread
// metadata for that specific row. As with LoadByUUID, this is a
// distinct method (rather than a flag on LoadByIDForOwner) so the
// audit trail makes the no-uid path obvious.
func (r *ThreadRepository) LoadByID(threadID uint) (*workagentModel.ChatThread, error) {
	thread := &workagentModel.ChatThread{}
	if err := r.db.Where("id = ?", threadID).First(thread).Error; err != nil {
		return nil, err
	}
	return thread, nil
}

// PersistAgentSessionID writes the SDK session id rotation onto the
// thread row and busts the cache. Critical path: the next
// HandleAgentChat for this thread reads agent_session_id and decides
// whether to call SDK.WithResume(...) — a cached pre-rotation value
// would either error against the SDK's expired session or replay
// stale context for up to 5 minutes.
//
// Empty newSessionID is a no-op (mirrors the old call site's check)
// — the SDK occasionally returns an empty session id on transient
// upstream hiccups; we don't want to clear the persisted id.
//
// createdAt is taken explicitly (rather than time.Now() inside the
// repo) so tests can pin the value and the caller controls when the
// timestamp is sampled.
func (r *ThreadRepository) PersistAgentSessionID(threadID uint, newSessionID string, createdAt time.Time) error {
	if newSessionID == "" {
		return nil
	}
	updates := map[string]interface{}{
		"agent_session_id":         newSessionID,
		"agent_session_created_at": &createdAt,
	}
	if err := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("persist agent session id: %w", err)
	}
	r.cache.Invalidate(fmt.Sprint(threadID))
	return nil
}

// ListThreadsOptions parameterises ListByOwner. The cursor pair
// (CursorTime + CursorID) is taken pre-decoded so the wire format
// (currently base64-wrapped "<unixNano>|<id>") stays at the API
// boundary. ListByOwner doesn't deal with strings.
//
// Page is 1-indexed and only consulted when CursorTime is zero.
//
// MaxOffsetGuard refuses (page-1)*limit beyond the cap so a deep
// OFFSET scan can't lock the DB behind a multi-second seek. 0 means
// "no guard" — used by tests; production callers pass a real cap.
type ListThreadsOptions struct {
	UID       uint
	AgentType string
	// ProjectID > 0 scopes the listing to threads bound to that
	// project. 0 means "all of the user's threads regardless of
	// project" — preserves legacy behaviour for callers that don't
	// know about projects yet. The §1 Project/Workspace Stage 2
	// Rail uses this to filter by the currently-selected project.
	ProjectID      uint
	CursorTime     time.Time
	CursorID       uint
	Page           int
	Limit          int
	MaxOffsetGuard int
}

// ListThreadsResult is the read-side return shape. NextCursorTime /
// NextCursorID are the (updated_at, id) of the LAST returned row when
// HasMore=true; the API layer encodes them into the wire-format
// cursor string. TotalCount is only populated in legacy page mode
// because cursor mode skips the count-on-every-page cost.
//
// OffsetCapped signals that the requested (page, limit) combination
// exceeded MaxOffsetGuard — caller should serialize an empty-items
// response without issuing a Find.
type ListThreadsResult struct {
	Threads        []workagentModel.ChatThread
	HasMore        bool
	TotalCount     int64
	UsedCursor     bool
	OffsetCapped   bool
	NextCursorTime time.Time
	NextCursorID   uint
}

// ListByOwner reads a page of threads owned by opts.UID. Both legacy
// page+offset and (updated_at, id)-cursor modes are supported via the
// composite (uid, agent_type, updated_at) index. The repo handles:
//
//   - cursor decoding is the caller's job; we take pre-decoded values
//     to keep the wire format out of the service layer
//   - peek-one-extra (Limit+1) lookahead so HasMore comes from the
//     same SELECT, no second COUNT round-trip
//   - legacy page mode optionally returns TotalCount (one COUNT)
//   - OffsetCapped short-circuit before issuing a deep-offset Find
//
// uid==0 returns an empty list — same posture as LoadByIDForOwner;
// the caller should never have an unauthenticated path reach this.
func (r *ThreadRepository) ListByOwner(opts ListThreadsOptions) (*ListThreadsResult, error) {
	if opts.UID == 0 {
		return &ListThreadsResult{}, nil
	}
	limit := opts.Limit
	if limit < 1 {
		limit = 10
	}

	useCursor := !opts.CursorTime.IsZero()
	res := &ListThreadsResult{UsedCursor: useCursor}

	q := r.db.Where("uid = ? AND agent_type = ?", opts.UID, opts.AgentType)
	if opts.ProjectID > 0 {
		q = q.Where("project_id = ?", opts.ProjectID)
	}
	if useCursor {
		q = q.Where(
			"(updated_at < ? OR (updated_at = ? AND id < ?))",
			opts.CursorTime, opts.CursorTime, opts.CursorID,
		)
	} else {
		// Legacy page mode: compute total once. Skipped in cursor mode
		// because the count-on-every-page cost is the second-largest
		// at scale (~half the wall-time of the main SELECT for a user
		// with 10k+ threads).
		countQ := r.db.Model(&workagentModel.ChatThread{}).
			Where("uid = ? AND agent_type = ?", opts.UID, opts.AgentType)
		if opts.ProjectID > 0 {
			countQ = countQ.Where("project_id = ?", opts.ProjectID)
		}
		if err := countQ.Count(&res.TotalCount).Error; err != nil {
			return nil, fmt.Errorf("count threads: %w", err)
		}

		page := opts.Page
		if page < 1 {
			page = 1
		}
		offset := (page - 1) * limit
		if opts.MaxOffsetGuard > 0 && offset > opts.MaxOffsetGuard {
			res.OffsetCapped = true
			return res, nil
		}
		q = q.Offset(offset)
	}

	q = q.Order("updated_at DESC").Order("id DESC").Limit(limit + 1)

	var threads []workagentModel.ChatThread
	if err := q.Find(&threads).Error; err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}

	if len(threads) > limit {
		// Peek-one-extra succeeded — there's a next page. Trim the
		// extra row and surface the cursor anchor for the caller to
		// encode.
		res.HasMore = true
		threads = threads[:limit]
	}
	res.Threads = threads
	if res.HasMore && len(threads) > 0 {
		last := threads[len(threads)-1]
		res.NextCursorTime = last.UpdatedAt
		res.NextCursorID = last.Id
	}
	return res, nil
}
