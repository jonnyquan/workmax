package workagent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// ThreadRepository tests pin two contracts that the previous inline
// Updates() call sites kept getting wrong:
//
//   1. State-changing methods MUST invalidate the ThreadCache. The
//      original mode-switch shipped without the invalidate twice;
//      both times the next turn resumed the SDK against the old
//      mode's session and the agent answered as if the toggle didn't
//      happen. The repo bakes the invalidate in.
//
//   2. Empty-string args are no-ops, never clobbers. A naive
//      Updates(map{"name": ""}) blanks the column; the repo refuses
//      that input so a caller passing through an unset field can't
//      accidentally erase data.
//
// Each test seeds a fresh thread + a per-test ThreadCache so cache
// assertions don't bleed across cases. SQLite via testutil.NewTestDB.

func newRepo(t *testing.T) (*ThreadRepository, *gorm.DB, *ThreadCache) {
	t.Helper()
	db := testutil.NewTestDB(t)
	cache := NewThreadCache(16, time.Minute)
	return NewThreadRepositoryWithCache(db, cache), db, cache
}

func seedRepoThread(t *testing.T, db *gorm.DB, override func(*workagentModel.ChatThread)) uint {
	t.Helper()
	thread := workagentModel.ChatThread{
		UID:       1,
		UUID:      "test-uuid-" + t.Name(),
		Name:      "seed",
		AgentMode: "ppt",
		Model:     "work-pro",
	}
	if override != nil {
		override(&thread)
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return thread.Id
}

func loadThread(t *testing.T, db *gorm.DB, threadID uint) workagentModel.ChatThread {
	t.Helper()
	var row workagentModel.ChatThread
	if err := db.Where("id = ?", threadID).First(&row).Error; err != nil {
		t.Fatalf("load thread: %v", err)
	}
	return row
}

// PersistAgentMode is the most failure-prone path of the bunch — it
// must (a) write the new mode, (b) clear agent_session_id AND
// agent_session_created_at so the SDK bootstraps fresh, and (c)
// invalidate the cache. The previous inline form forgot any one of
// those three on at least one production patch.
func TestThreadRepository_PersistAgentMode_WritesAndResetsAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	now := time.Now()
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.AgentMode = "ppt"
		c.AgentSessionID = "session-old"
		c.AgentSessionCreatedAt = &now
	})

	// Prime the cache so we can assert the invalidate cleared it.
	cache.Set(fmt.Sprint(threadID), &workagentModel.ChatThread{Name: "stale"})
	if _, ok := cache.Get(fmt.Sprint(threadID)); !ok {
		t.Fatalf("cache prime: expected thread cached")
	}

	if err := repo.PersistAgentMode(threadID, "marketingPoster"); err != nil {
		t.Fatalf("persist: %v", err)
	}

	row := loadThread(t, db, threadID)
	if row.AgentMode != "marketingPoster" {
		t.Errorf("agent_mode = %q, want 'marketingPoster'", row.AgentMode)
	}
	if row.AgentSessionID != "" {
		t.Errorf("agent_session_id should be reset, got %q", row.AgentSessionID)
	}
	if row.AgentSessionCreatedAt != nil {
		t.Errorf("agent_session_created_at should be nil, got %v", row.AgentSessionCreatedAt)
	}
	if _, ok := cache.Get(fmt.Sprint(threadID)); ok {
		t.Errorf("ThreadCache must be invalidated after PersistAgentMode — stale row would resume the old session")
	}
}

func TestThreadRepository_PersistAgentMode_EmptyIsNoOp(t *testing.T) {
	repo, db, _ := newRepo(t)
	now := time.Now()
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.AgentMode = "ppt"
		c.AgentSessionID = "session-keep"
		c.AgentSessionCreatedAt = &now
	})

	if err := repo.PersistAgentMode(threadID, ""); err != nil {
		t.Fatalf("empty mode returned error: %v", err)
	}

	row := loadThread(t, db, threadID)
	// Empty mode must not clobber the row — the previous mode AND its
	// agent_session_id stay put. A naive Updates(map{"agent_mode": ""})
	// would blank both.
	if row.AgentMode != "ppt" {
		t.Errorf("agent_mode mutated by empty input: got %q", row.AgentMode)
	}
	if row.AgentSessionID != "session-keep" {
		t.Errorf("agent_session_id was reset by empty input: got %q", row.AgentSessionID)
	}
}

func TestThreadRepository_UpdateModelTier_WritesAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.Model = "work-pro"
	})
	cache.Set(fmt.Sprint(threadID), &workagentModel.ChatThread{Name: "stale"})

	if err := repo.UpdateModelTier(threadID, "work-plus"); err != nil {
		t.Fatalf("update: %v", err)
	}

	if got := loadThread(t, db, threadID).Model; got != "work-plus" {
		t.Errorf("model = %q, want work-plus", got)
	}
	if _, ok := cache.Get(fmt.Sprint(threadID)); ok {
		t.Errorf("ThreadCache must be invalidated after UpdateModelTier")
	}
}

func TestThreadRepository_UpdateModelTier_EmptyIsNoOp(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.Model = "work-pro"
	})

	if err := repo.UpdateModelTier(threadID, ""); err != nil {
		t.Fatalf("empty model returned error: %v", err)
	}
	if got := loadThread(t, db, threadID).Model; got != "work-pro" {
		t.Errorf("model mutated by empty input: got %q", got)
	}
}

func TestThreadRepository_TouchTimestamp_OnlyBumpsUpdatedAt(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.AgentMode = "ppt"
		c.Model = "work-pro"
		c.Name = "preserve me"
	})
	before := loadThread(t, db, threadID)

	pinned := before.UpdatedAt.Add(2 * time.Hour)
	if err := repo.TouchTimestamp(threadID, pinned); err != nil {
		t.Fatalf("touch: %v", err)
	}

	after := loadThread(t, db, threadID)
	// Timestamp moved.
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at not bumped: before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}
	// Other columns did NOT move — touch is a single-column write.
	if after.AgentMode != "ppt" || after.Model != "work-pro" || after.Name != "preserve me" {
		t.Errorf("touch should not modify other columns: got mode=%q model=%q name=%q",
			after.AgentMode, after.Model, after.Name)
	}
}

func TestThreadRepository_RenameThread_WritesAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.Name = "Untitled"
	})
	cache.Set(fmt.Sprint(threadID), &workagentModel.ChatThread{Name: "Untitled"})

	if err := repo.RenameThread(threadID, "AI generated title"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := loadThread(t, db, threadID).Name; got != "AI generated title" {
		t.Errorf("name = %q, want 'AI generated title'", got)
	}
	if _, ok := cache.Get(fmt.Sprint(threadID)); ok {
		t.Errorf("ThreadCache must be invalidated after RenameThread")
	}
}

func TestThreadRepository_RenameThread_EmptyIsNoOp(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.Name = "preserve me"
	})

	if err := repo.RenameThread(threadID, ""); err != nil {
		t.Fatalf("empty rename returned error: %v", err)
	}
	if got := loadThread(t, db, threadID).Name; got != "preserve me" {
		t.Errorf("empty rename clobbered name: %q", got)
	}
}

func TestThreadRepository_SetProjectForOwner_WritesAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 11
		c.ProjectID = 1
	})
	cache.Set(fmt.Sprint(threadID), &workagentModel.ChatThread{ProjectID: 1})

	updatedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := repo.SetProjectForOwner(threadID, 11, 99, updatedAt); err != nil {
		t.Fatalf("set project: %v", err)
	}

	row := loadThread(t, db, threadID)
	if row.ProjectID != 99 {
		t.Errorf("ProjectID = %d, want 99", row.ProjectID)
	}
	if !row.UpdatedAt.Equal(updatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", row.UpdatedAt, updatedAt)
	}
	if _, ok := cache.Get(fmt.Sprint(threadID)); ok {
		t.Errorf("ThreadCache must be invalidated after SetProjectForOwner")
	}
}

func TestThreadRepository_SetProjectForOwner_CrossTenantNoWrite(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 11
		c.ProjectID = 1
	})

	err := repo.SetProjectForOwner(threadID, 99, 77, time.Now())
	if !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant err = %v, want ErrRecordNotFound", err)
	}

	row := loadThread(t, db, threadID)
	if row.ProjectID != 1 {
		t.Errorf("ProjectID was overwritten to %d under cross-tenant request", row.ProjectID)
	}
}

func TestThreadRepository_SetStatistics_WritesAllFourColumnsAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	threadID := seedRepoThread(t, db, nil)
	cache.Set(fmt.Sprint(threadID), &workagentModel.ChatThread{Name: "stale"})

	pinned := time.Date(2026, 5, 1, 10, 30, 0, 0, time.UTC)
	preview := strings.Repeat("x", 50)
	if err := repo.SetStatistics(threadID, ThreadStatistics{
		MessageCount: 42,
		Preview:      preview,
		FileCount:    7,
		UpdatedAt:    pinned,
	}); err != nil {
		t.Fatalf("set stats: %v", err)
	}

	row := loadThread(t, db, threadID)
	if row.MessageCount != 42 {
		t.Errorf("message_count = %d, want 42", row.MessageCount)
	}
	if row.MsgPreview != preview {
		t.Errorf("msg_preview mismatch: got %q", row.MsgPreview)
	}
	if row.FileCount != 7 {
		t.Errorf("file_count = %d, want 7", row.FileCount)
	}
	if !row.UpdatedAt.Equal(pinned) {
		t.Errorf("updated_at = %v, want %v", row.UpdatedAt, pinned)
	}
	if _, ok := cache.Get(fmt.Sprint(threadID)); ok {
		t.Errorf("ThreadCache must be invalidated after SetStatistics")
	}
}

// LoadByIDForOwner: happy path returns the row for the rightful owner.
func TestThreadRepository_LoadByIDForOwner_HappyPath(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 42
		c.Name = "owned"
	})

	got, err := repo.LoadByIDForOwner(threadID, 42)
	if err != nil {
		t.Fatalf("LoadByIDForOwner: %v", err)
	}
	if got.Id != threadID {
		t.Errorf("Id = %d, want %d", got.Id, threadID)
	}
	if got.Name != "owned" {
		t.Errorf("Name = %q, want owned", got.Name)
	}
}

// LoadByIDForOwner: cross-tenant returns ErrRecordNotFound, NOT a
// permission error — same generic shape as missing row so an attacker
// can't distinguish "this thread exists, just not yours" from "this
// thread doesn't exist". Defends against CWE-639 enumeration.
func TestThreadRepository_LoadByIDForOwner_CrossTenantReturnsNotFound(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 100
	})

	_, err := repo.LoadByIDForOwner(threadID, 99)
	if err == nil {
		t.Fatal("cross-tenant load returned nil, want gorm.ErrRecordNotFound")
	}
	if !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("err = %v, want gorm.ErrRecordNotFound", err)
	}
}

// LoadByIDForOwner: uid=0 means "no caller" — refuses to short-circuit
// the ownership predicate even if the row exists.
func TestThreadRepository_LoadByIDForOwner_RefusesZeroUID(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, nil)

	_, err := repo.LoadByIDForOwner(threadID, 0)
	if !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("uid=0 must return ErrRecordNotFound, got %v", err)
	}
}

// LoadByUUIDForOwner: same defence as LoadByIDForOwner, keyed by UUID.
func TestThreadRepository_LoadByUUIDForOwner_FiltersByOwnerAndAgentType(t *testing.T) {
	repo, db, _ := newRepo(t)
	uuidStr := "uuid-LBUFO-" + t.Name()
	seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 7
		c.UUID = uuidStr
		c.AgentType = "general_agent"
	})

	// Right owner, right agent type → load.
	got, err := repo.LoadByUUIDForOwner(uuidStr, 7, "general_agent")
	if err != nil {
		t.Fatalf("LoadByUUIDForOwner happy: %v", err)
	}
	if got.UUID != uuidStr {
		t.Errorf("UUID mismatch: %q vs %q", got.UUID, uuidStr)
	}

	// Wrong owner → not found.
	if _, err := repo.LoadByUUIDForOwner(uuidStr, 8, "general_agent"); !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("wrong-owner err = %v, want ErrRecordNotFound", err)
	}

	// Wrong agent_type → not found.
	if _, err := repo.LoadByUUIDForOwner(uuidStr, 7, "other_agent"); !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("wrong-agent-type err = %v, want ErrRecordNotFound", err)
	}

	// Empty UUID short-circuits without DB hit.
	if _, err := repo.LoadByUUIDForOwner("", 7, "general_agent"); !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("empty UUID err = %v, want ErrRecordNotFound", err)
	}
}

// ApplyUpdatesForOwner: writes the updates AND invalidates cache, in
// the same atomic flow that the previous inline shape kept botching.
func TestThreadRepository_ApplyUpdatesForOwner_WritesAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 11
		c.Name = "before"
	})
	cache.Set(fmt.Sprint(threadID), &workagentModel.ChatThread{Name: "stale"})

	if err := repo.ApplyUpdatesForOwner(threadID, 11, map[string]interface{}{
		"name": "after",
	}); err != nil {
		t.Fatalf("apply updates: %v", err)
	}

	row := loadThread(t, db, threadID)
	if row.Name != "after" {
		t.Errorf("Name = %q, want after", row.Name)
	}
	if _, ok := cache.Get(fmt.Sprint(threadID)); ok {
		t.Errorf("ThreadCache must be invalidated after ApplyUpdatesForOwner")
	}
}

// ApplyUpdatesForOwner: cross-tenant request must NOT write the row.
// Returns ErrRecordNotFound and the on-disk value is unchanged.
func TestThreadRepository_ApplyUpdatesForOwner_CrossTenantNoWrite(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 100
		c.Name = "owner-only"
	})

	err := repo.ApplyUpdatesForOwner(threadID, 99, map[string]interface{}{
		"name": "hijacked",
	})
	if !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant err = %v, want ErrRecordNotFound", err)
	}
	row := loadThread(t, db, threadID)
	if row.Name != "owner-only" {
		t.Errorf("name was overwritten to %q under cross-tenant request — IDOR regression", row.Name)
	}
}

// ApplyUpdatesForOwner: empty updates map is a no-op, NOT an error.
func TestThreadRepository_ApplyUpdatesForOwner_EmptyMapIsNoOp(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 5
	})

	if err := repo.ApplyUpdatesForOwner(threadID, 5, map[string]interface{}{}); err != nil {
		t.Errorf("empty map: got %v, want nil", err)
	}
}

// SetVisibility: delegates to ApplyUpdatesForOwner so it inherits the
// cache-invalidation contract; pin that inheritance with a focused
// test so a future refactor can't accidentally drop it.
func TestThreadRepository_SetVisibility_FlipsAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 3
	})
	cache.Set(fmt.Sprint(threadID), &workagentModel.ChatThread{Name: "stale"})

	if err := repo.SetVisibility(threadID, 3, true); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}
	row := loadThread(t, db, threadID)
	if !row.IsPublic {
		t.Errorf("IsPublic = false, want true")
	}
	if _, ok := cache.Get(fmt.Sprint(threadID)); ok {
		t.Errorf("ThreadCache must be invalidated after SetVisibility")
	}

	// Cross-tenant must not flip.
	if err := repo.SetVisibility(threadID, 99, false); !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant flip err = %v, want ErrRecordNotFound", err)
	}
	row = loadThread(t, db, threadID)
	if !row.IsPublic {
		t.Errorf("IsPublic regressed to false under cross-tenant call — IDOR regression")
	}
}

// LoadByUUID and LoadByID are the no-uid public-share variants.
// They return any thread that matches the key — IsPublic gating is
// the caller's job. Pin the no-uid contract so a future caller can't
// "consolidate" by adding a uid filter and silently breaking the
// share endpoints.

func TestThreadRepository_LoadByUUID_AnyOwner(t *testing.T) {
	repo, db, _ := newRepo(t)
	uuidStr := "uuid-LBU-" + t.Name()
	seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 100
		c.UUID = uuidStr
	})

	got, err := repo.LoadByUUID(uuidStr)
	if err != nil {
		t.Fatalf("LoadByUUID: %v", err)
	}
	if got.UUID != uuidStr {
		t.Errorf("UUID mismatch: %q", got.UUID)
	}
	if got.UID != 100 {
		t.Errorf("UID = %d, want 100 (no-uid lookup must return any matching row)", got.UID)
	}

	// Empty input short-circuits to ErrRecordNotFound — no DB hit.
	if _, err := repo.LoadByUUID(""); !errors_Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("empty UUID err = %v, want ErrRecordNotFound", err)
	}
}

func TestThreadRepository_LoadByID_AnyOwner(t *testing.T) {
	repo, db, _ := newRepo(t)
	threadID := seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 200
	})

	got, err := repo.LoadByID(threadID)
	if err != nil {
		t.Fatalf("LoadByID: %v", err)
	}
	if got.Id != threadID || got.UID != 200 {
		t.Errorf("LoadByID got Id=%d UID=%d, want %d/200", got.Id, got.UID, threadID)
	}
}

// errors_Is is a thin wrapper to avoid pulling stdlib `errors` into
// every test — gorm.ErrRecordNotFound is a sentinel so a direct equality
// check works, but errors.Is is the future-proof shape.
func errors_Is(err, target error) bool {
	return err == target
}

// ListByOwner: legacy page-mode returns paginated rows + total count
// from one repo call. Verifies the peek-one-extra HasMore signal is
// accurate and TotalCount matches the underlying row count.
func TestThreadRepository_ListByOwner_LegacyPageMode(t *testing.T) {
	repo, db, _ := newRepo(t)
	for i := 0; i < 5; i++ {
		seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
			c.UID = 7
			c.AgentType = "general_agent"
			c.Name = fmt.Sprintf("thread-%d", i)
		})
	}
	// Spoiler row from a different user — must not bleed into results.
	seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 99
		c.AgentType = "general_agent"
	})

	res, err := repo.ListByOwner(ListThreadsOptions{
		UID:       7,
		AgentType: "general_agent",
		Page:      1,
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(res.Threads) != 2 {
		t.Errorf("page 1 size = %d, want 2", len(res.Threads))
	}
	if !res.HasMore {
		t.Errorf("HasMore = false on page 1 of 5; want true (peek-one-extra contract)")
	}
	if res.TotalCount != 5 {
		t.Errorf("TotalCount = %d, want 5 (legacy mode includes count)", res.TotalCount)
	}
	if res.UsedCursor {
		t.Errorf("UsedCursor = true; want false in legacy page mode")
	}
}

// ListByOwner: cursor mode skips the count and walks the (updated_at,
// id) tuple. Verifies that the second page returned via the
// NextCursor* fields doesn't overlap the first.
func TestThreadRepository_ListByOwner_CursorMode(t *testing.T) {
	repo, db, _ := newRepo(t)
	// Pre-stagger updated_at so cursor walks deterministically.
	base := time.Now()
	ids := make([]uint, 6)
	for i := 0; i < 6; i++ {
		ids[i] = seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
			c.UID = 11
			c.AgentType = "general_agent"
		})
		// Force unique updated_at per row in reverse-creation order so
		// the highest-id thread also has the latest updated_at; cursor
		// pagination should walk down through them.
		stamp := base.Add(time.Duration(i) * time.Second)
		if err := db.Model(&workagentModel.ChatThread{}).
			Where("id = ?", ids[i]).
			Update("updated_at", stamp).Error; err != nil {
			t.Fatalf("backfill updated_at: %v", err)
		}
	}

	page1, err := repo.ListByOwner(ListThreadsOptions{
		UID:       11,
		AgentType: "general_agent",
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1.Threads) != 3 || !page1.HasMore {
		t.Fatalf("page 1 unexpected: len=%d hasMore=%v", len(page1.Threads), page1.HasMore)
	}
	if page1.TotalCount != 6 {
		t.Errorf("legacy page-mode page 1 should still surface TotalCount=6, got %d", page1.TotalCount)
	}

	page2, err := repo.ListByOwner(ListThreadsOptions{
		UID:        11,
		AgentType:  "general_agent",
		CursorTime: page1.NextCursorTime,
		CursorID:   page1.NextCursorID,
		Limit:      3,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if !page2.UsedCursor {
		t.Errorf("page 2 UsedCursor = false; want true")
	}
	if page2.TotalCount != 0 {
		t.Errorf("cursor mode TotalCount = %d, want 0 (skipped by design)", page2.TotalCount)
	}

	// Pages must not overlap. Check by ID.
	seen := map[uint]bool{}
	for _, th := range page1.Threads {
		seen[th.Id] = true
	}
	for _, th := range page2.Threads {
		if seen[th.Id] {
			t.Errorf("thread %d appears on both pages — cursor walk overlapped", th.Id)
		}
	}
}

// ListByOwner: MaxOffsetGuard refuses deep-offset queries before they
// reach the DB, surfacing OffsetCapped so the caller can serialise an
// empty-page response without the slow seek.
func TestThreadRepository_ListByOwner_OffsetCapped(t *testing.T) {
	repo, db, _ := newRepo(t)
	seedRepoThread(t, db, func(c *workagentModel.ChatThread) {
		c.UID = 13
		c.AgentType = "general_agent"
	})

	// Page 100 with limit 10 → offset 990 > guard 500 → cap fires.
	res, err := repo.ListByOwner(ListThreadsOptions{
		UID:            13,
		AgentType:      "general_agent",
		Page:           100,
		Limit:          10,
		MaxOffsetGuard: 500,
	})
	if err != nil {
		t.Fatalf("offset-capped: %v", err)
	}
	if !res.OffsetCapped {
		t.Errorf("OffsetCapped = false; want true at page=100 limit=10 guard=500")
	}
	if len(res.Threads) != 0 {
		t.Errorf("threads returned with OffsetCapped: %d; want 0", len(res.Threads))
	}
}

// ListByOwner: uid=0 returns an empty result without touching the DB.
// Defence-in-depth — no path should reach this with uid=0, but if one
// does, we don't want to silently iterate all users' rows.
func TestThreadRepository_ListByOwner_RefusesZeroUID(t *testing.T) {
	repo, db, _ := newRepo(t)
	seedRepoThread(t, db, nil)

	res, err := repo.ListByOwner(ListThreadsOptions{
		UID:       0,
		AgentType: "general_agent",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("uid=0 errored: %v", err)
	}
	if len(res.Threads) != 0 || res.TotalCount != 0 {
		t.Errorf("uid=0 should return empty: len=%d total=%d", len(res.Threads), res.TotalCount)
	}
}
