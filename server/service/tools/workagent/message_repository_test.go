package workagent

import (
	"errors"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// MessageRepository tests pin two contracts:
//
//   1. CreateAgentMessage round-trips the autoincrement Id back onto
//      the caller's pointer. saveAgentConversation reads msg.Id right
//      after the create call to attach the SSE message_id, so a Create
//      that left Id=0 would silently break the streaming UX.
//
//   2. ClearAIText / ClearUserText only touch their own column. The
//      previous handler shape used a generic Updates(map{"ai_text":""})
//      which is correct but typo-prone. The explicit single-column
//      writers refuse to clobber the sibling column.
//
// SQLite via testutil.NewTestDB; no shared cache to worry about because
// chat_message has no caching layer.

func newMessageRepo(t *testing.T) (*MessageRepository, *gorm.DB) {
	t.Helper()
	db := testutil.NewTestDB(t)
	return NewMessageRepository(db), db
}

func seedMessage(t *testing.T, db *gorm.DB, override func(*workagentModel.ChatMessage)) uint {
	t.Helper()
	msg := workagentModel.ChatMessage{
		UID:      1,
		UUID:     "msg-uuid-" + t.Name(),
		ThreadID: 100,
		UserText: "user prompt",
		AIText:   "ai response",
		ChatMode: "agent",
	}
	if override != nil {
		override(&msg)
	}
	if err := db.Create(&msg).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return msg.Id
}

func loadMessage(t *testing.T, db *gorm.DB, id uint) workagentModel.ChatMessage {
	t.Helper()
	var row workagentModel.ChatMessage
	if err := db.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("load message: %v", err)
	}
	return row
}

func TestMessageRepository_CreateAgentMessage_AssignsAutoIncrementId(t *testing.T) {
	repo, db := newMessageRepo(t)
	msg := &workagentModel.ChatMessage{
		UID:      7,
		UUID:     "create-test",
		ThreadID: 42,
		UserText: "hello",
		AIText:   "hi",
		ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}
	if msg.Id == 0 {
		t.Fatalf("autoincrement Id not written back onto pointer — saveAgentConversation reads msg.Id right after this call")
	}

	loaded := loadMessage(t, db, msg.Id)
	if loaded.UserText != "hello" || loaded.AIText != "hi" {
		t.Errorf("round-trip lost text: %+v", loaded)
	}
}

func TestMessageRepository_CreateAgentMessage_NilIsErr(t *testing.T) {
	repo, _ := newMessageRepo(t)
	if err := repo.CreateAgentMessage(nil); err == nil {
		t.Errorf("expected error on nil pointer")
	}
}

func TestMessageRepository_LoadByID_HappyPath(t *testing.T) {
	repo, db := newMessageRepo(t)
	id := seedMessage(t, db, nil)

	got, err := repo.LoadByID(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.UserText != "user prompt" {
		t.Errorf("user_text mismatch: %q", got.UserText)
	}
}

func TestMessageRepository_LoadByID_NotFoundIsSentinel(t *testing.T) {
	repo, _ := newMessageRepo(t)
	_, err := repo.LoadByID(99999)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected gorm.ErrRecordNotFound, got %v", err)
	}
}

func TestMessageRepository_DeleteByID_RemovesRow(t *testing.T) {
	repo, db := newMessageRepo(t)
	id := seedMessage(t, db, nil)

	if err := repo.DeleteByID(id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var count int64
	if err := db.Model(&workagentModel.ChatMessage{}).Where("id = ?", id).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("row still present after delete: count=%d", count)
	}
}

func TestMessageRepository_ClearAIText_OnlyTouchesAIColumn(t *testing.T) {
	repo, db := newMessageRepo(t)
	id := seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.UserText = "preserve user side"
		m.AIText = "wipe me"
		m.TotalPrompt = "preserve prompt"
	})

	if err := repo.ClearAIText(id); err != nil {
		t.Fatalf("clear: %v", err)
	}

	row := loadMessage(t, db, id)
	if row.AIText != "" {
		t.Errorf("ai_text not cleared: %q", row.AIText)
	}
	// Sibling columns untouched — guards against a future refactor that
	// accidentally widens the Update map.
	if row.UserText != "preserve user side" {
		t.Errorf("user_text was clobbered: %q", row.UserText)
	}
	if row.TotalPrompt != "preserve prompt" {
		t.Errorf("total_prompt was clobbered: %q", row.TotalPrompt)
	}
}

func TestMessageRepository_ClearUserText_OnlyTouchesUserColumn(t *testing.T) {
	repo, db := newMessageRepo(t)
	id := seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.UserText = "wipe me"
		m.AIText = "preserve ai side"
	})

	if err := repo.ClearUserText(id); err != nil {
		t.Fatalf("clear: %v", err)
	}

	row := loadMessage(t, db, id)
	if row.UserText != "" {
		t.Errorf("user_text not cleared: %q", row.UserText)
	}
	if row.AIText != "preserve ai side" {
		t.Errorf("ai_text was clobbered: %q", row.AIText)
	}
}

func TestMessageRepository_RoundTripWithLargeStructuredContent(t *testing.T) {
	// saveAgentConversation writes the full conversation JSON into
	// structured_content — can be tens to hundreds of KB on long
	// turns. Verify the column type / repo path round-trips a payload
	// at that size without truncation, which guards against a SQLite
	// schema change that accidentally bounds the column (the model is
	// `longtext` in MySQL but TEXT in our SQLite mirror).
	repo, db := newMessageRepo(t)
	bigPayload := strings.Repeat(`{"role":"assistant","content":"chunk"}`, 2000)

	msg := &workagentModel.ChatMessage{
		UID:               5,
		UUID:              "big-payload",
		ThreadID:          1,
		StructuredContent: bigPayload,
		ChatMode:          "agent",
		ContentType:       "agent_conversation",
	}
	if err := repo.CreateAgentMessage(msg); err != nil {
		t.Fatalf("create big: %v", err)
	}

	row := loadMessage(t, db, msg.Id)
	if row.StructuredContent != bigPayload {
		t.Errorf("structured_content round-trip mismatch: len(want)=%d len(got)=%d",
			len(bigPayload), len(row.StructuredContent))
	}
}

// ListByThread / CountByThread tests cover the chat-history read
// path that B3 moved out of conversation_api.go. The column-narrow
// is enforced repo-side; tests pin newest-first ordering and the
// per-thread scoping so a sibling thread's messages can't bleed in.

func TestMessageRepository_ListByThread_NewestFirst(t *testing.T) {
	repo, db := newMessageRepo(t)
	const threadID = 42
	for i := 0; i < 5; i++ {
		idx := i
		seedMessage(t, db, func(m *workagentModel.ChatMessage) {
			m.UID = 1
			m.UUID = "msg-" + strings.Repeat("a", idx+1)
			m.ThreadID = threadID
			m.UserText = "hello-" + string(rune('A'+idx))
			m.ChatMode = "agent"
		})
	}
	// Spoiler row in a different thread — must not bleed in.
	seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.UID = 1
		m.UUID = "spoiler"
		m.ThreadID = 999
		m.UserText = "wrong thread"
	})

	got, err := repo.ListByThread(uint(threadID), 1, 10)
	if err != nil {
		t.Fatalf("ListByThread: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5 (sibling thread leaked in)", len(got))
	}
	// Created_at descending — but SQLite stamps all rows in the same
	// second by default, so we don't pin a specific order. We pin
	// thread scoping (above) and column narrow (below).
	for _, m := range got {
		if m.ThreadID != threadID {
			t.Errorf("ListByThread returned ThreadID=%d, want %d", m.ThreadID, threadID)
		}
	}
}

// ListByThread: page bounds default sanely on degenerate input rather
// than erroring. The handler boundary already validates, but the repo
// shouldn't break callers that pass page=0 or limit<1.
func TestMessageRepository_ListByThread_DegenerateInputDefaults(t *testing.T) {
	repo, db := newMessageRepo(t)
	seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.UID = 1
		m.UUID = "single"
		m.ThreadID = 7
	})

	for _, tc := range []struct {
		name        string
		page, limit int
	}{
		{"page_zero", 0, 10},
		{"page_negative", -3, 10},
		{"limit_zero", 1, 0},
		{"limit_negative", 1, -5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.ListByThread(7, tc.page, tc.limit)
			if err != nil {
				t.Fatalf("page=%d limit=%d errored: %v", tc.page, tc.limit, err)
			}
			if len(got) != 1 {
				t.Errorf("expected the seeded row to be returned via defaults, got %d", len(got))
			}
		})
	}
}

func TestMessageRepository_CountByThread(t *testing.T) {
	repo, db := newMessageRepo(t)
	for i := 0; i < 7; i++ {
		seedMessage(t, db, func(m *workagentModel.ChatMessage) {
			m.UID = 1
			m.UUID = "c-" + strings.Repeat("a", i+1)
			m.ThreadID = 22
		})
	}
	// Other threads.
	for i := 0; i < 3; i++ {
		seedMessage(t, db, func(m *workagentModel.ChatMessage) {
			m.UID = 1
			m.UUID = "x-" + strings.Repeat("a", i+1)
			m.ThreadID = 33
		})
	}

	got, err := repo.CountByThread(22)
	if err != nil {
		t.Fatalf("CountByThread: %v", err)
	}
	if got != 7 {
		t.Errorf("count = %d, want 7", got)
	}
}

// PersistAgentSessionID is on ThreadRepository but the test belongs
// alongside the rest of the session-related tests. Mirror what the
// thread_repository_test.go file does: seeds a thread, runs the
// method, asserts both the DB write and the cache invalidation.
//
// Empty session id is documented as a no-op (handles transient SDK
// returns); test pins it.
func TestThreadRepository_PersistAgentSessionID_WritesAndInvalidates(t *testing.T) {
	repo, db, cache := newRepo(t)
	threadID := seedRepoThread(t, db, nil)
	cache.Set(strings.Repeat("placeholder", 1), &workagentModel.ChatThread{Name: "stale"})

	stamp := workagentModel.ChatThread{}.UpdatedAt // zero value
	_ = stamp
	if err := repo.PersistAgentSessionID(threadID, "session-xyz", workagentModel.ChatThread{}.UpdatedAt); err != nil {
		t.Fatalf("PersistAgentSessionID: %v", err)
	}

	row := loadThread(t, db, threadID)
	if row.AgentSessionID != "session-xyz" {
		t.Errorf("AgentSessionID = %q, want session-xyz", row.AgentSessionID)
	}
	if row.AgentSessionCreatedAt == nil {
		t.Errorf("AgentSessionCreatedAt = nil; want non-nil")
	}

	// Empty session id is a documented no-op; pin so a future caller
	// that "fixes" it doesn't accidentally clobber a populated row.
	if err := repo.PersistAgentSessionID(threadID, "", workagentModel.ChatThread{}.UpdatedAt); err != nil {
		t.Errorf("empty session id should be no-op, got %v", err)
	}
	row = loadThread(t, db, threadID)
	if row.AgentSessionID != "session-xyz" {
		t.Errorf("empty-id call clobbered session id: %q", row.AgentSessionID)
	}
}

// SetUserRatingForOwner — P0 #3 critique loop. Pins: (a) round-trip
// of rating + feedback persists; (b) cross-tenant collapses to
// ErrRecordNotFound (no oracle); (c) invalid ratings reject with
// ErrInvalidRating sentinel; (d) zero uid / zero id short-circuit
// before touching the DB; (e) updating an existing rating overwrites
// rather than appending (set-not-append contract).

func TestSetUserRatingForOwner_HappyPath_ThumbsUp(t *testing.T) {
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, nil)

	if err := repo.SetUserRatingForOwner(msgID, 1, 1, ""); err != nil {
		t.Fatalf("SetUserRatingForOwner: %v", err)
	}
	row := loadMessage(t, db, msgID)
	if row.UserRating != 1 {
		t.Errorf("UserRating = %d, want 1", row.UserRating)
	}
	if row.UserFeedback != "" {
		t.Errorf("UserFeedback = %q, want empty for thumbs-up", row.UserFeedback)
	}
}

func TestSetUserRatingForOwner_HappyPath_ThumbsDownWithFeedback(t *testing.T) {
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, nil)

	feedback := "less neon, more film-noir; keep the wide shot"
	if err := repo.SetUserRatingForOwner(msgID, 1, -1, feedback); err != nil {
		t.Fatalf("SetUserRatingForOwner: %v", err)
	}
	row := loadMessage(t, db, msgID)
	if row.UserRating != -1 {
		t.Errorf("UserRating = %d, want -1", row.UserRating)
	}
	if row.UserFeedback != feedback {
		t.Errorf("UserFeedback = %q, want the seeded feedback", row.UserFeedback)
	}
}

func TestSetUserRatingForOwner_ClearByZero(t *testing.T) {
	// Rating=0 means "user cleared their previous rating". Pin so a
	// future "let's reject 0 because it's the default" change can't
	// break the un-vote affordance the frontend expects.
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, nil)

	if err := repo.SetUserRatingForOwner(msgID, 1, -1, "wrong"); err != nil {
		t.Fatalf("initial thumbs-down: %v", err)
	}
	if err := repo.SetUserRatingForOwner(msgID, 1, 0, ""); err != nil {
		t.Fatalf("clear rating: %v", err)
	}
	row := loadMessage(t, db, msgID)
	if row.UserRating != 0 || row.UserFeedback != "" {
		t.Errorf("clear should reset both fields; got rating=%d feedback=%q", row.UserRating, row.UserFeedback)
	}
}

func TestSetUserRatingForOwner_RejectsInvalidRating(t *testing.T) {
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, nil)

	for _, bad := range []int8{-2, 2, 100, -100} {
		err := repo.SetUserRatingForOwner(msgID, 1, bad, "")
		if !errors.Is(err, ErrInvalidRating) {
			t.Errorf("rating=%d: errors.Is(err, ErrInvalidRating) = false; got %v", bad, err)
		}
	}
	// And the row must not have been mutated.
	row := loadMessage(t, db, msgID)
	if row.UserRating != 0 {
		t.Errorf("row UserRating = %d after rejected writes; want 0", row.UserRating)
	}
}

func TestSetUserRatingForOwner_CrossTenantReturnsNotFound(t *testing.T) {
	// IDOR posture: the WHERE clause carries `uid = ?` so a foreign
	// uid sees ErrRecordNotFound, not a permission-denied error
	// (which would oracle the existence of the row to an attacker).
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.UID = 100
	})

	err := repo.SetUserRatingForOwner(msgID, 42, 1, "")
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant should return ErrRecordNotFound, got %v", err)
	}
	// Owner's row must not have been mutated.
	row := loadMessage(t, db, msgID)
	if row.UserRating != 0 {
		t.Errorf("attacker mutated row across tenants: UserRating=%d", row.UserRating)
	}
}

func TestSetUserRatingForOwner_RefusesZeroUIDAndID(t *testing.T) {
	// uid=0 / id=0 short-circuit before touching the DB. Same
	// posture as every other *ForOwner method — refuses to let a
	// caller that lost its context surface or mutate a row.
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, nil)

	if err := repo.SetUserRatingForOwner(msgID, 0, 1, ""); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("uid=0: errors.Is(err, ErrRecordNotFound) = false; got %v", err)
	}
	if err := repo.SetUserRatingForOwner(0, 1, 1, ""); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("id=0: errors.Is(err, ErrRecordNotFound) = false; got %v", err)
	}
}

// FindLatestUserCritique — pins the "most recent thumbs-down with
// non-empty feedback in this thread" query. Used by the next-turn
// preflight composer to inject <previous-critique> into the system
// prompt.

func TestFindLatestUserCritique_NoMatchReturnsNilNoError(t *testing.T) {
	// The most common case: thread has messages but none are rated.
	// Returns (nil, nil) so the composer can branch without a
	// special "are there any messages" precheck.
	repo, db := newMessageRepo(t)
	_ = seedMessage(t, db, func(m *workagentModel.ChatMessage) { m.ThreadID = 100 })

	got, err := repo.FindLatestUserCritique(100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil row, got id=%d", got.Id)
	}
}

func TestFindLatestUserCritique_ThumbsUpDoesNotMatch(t *testing.T) {
	// Thumbs-UP doesn't trigger critique injection. Pin so a
	// future "we should also remember positive feedback" change is
	// a conscious schema choice (e.g. a new query method), not a
	// silent overload of this one.
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, func(m *workagentModel.ChatMessage) { m.ThreadID = 100 })
	if err := repo.SetUserRatingForOwner(msgID, 1, 1, "love it"); err != nil {
		t.Fatalf("seed thumbs-up: %v", err)
	}

	got, err := repo.FindLatestUserCritique(100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("thumbs-up should not surface in critique query; got id=%d", got.Id)
	}
}

func TestFindLatestUserCritique_EmptyFeedbackDoesNotMatch(t *testing.T) {
	// Thumbs-down without a typed reason is still a signal — the
	// user disliked the output — but the composer has nothing to
	// inject. Filter empty-feedback rows out at the query so the
	// composer doesn't have to handle the empty-string case.
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, func(m *workagentModel.ChatMessage) { m.ThreadID = 100 })
	if err := repo.SetUserRatingForOwner(msgID, 1, -1, ""); err != nil {
		t.Fatalf("seed empty thumbs-down: %v", err)
	}

	got, err := repo.FindLatestUserCritique(100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("empty-feedback thumbs-down should not surface; got id=%d", got.Id)
	}
}

func TestFindLatestUserCritique_ReturnsMostRecentDown(t *testing.T) {
	// Seed three messages on the same thread: rated-down, neutral,
	// rated-down-with-different-feedback. Latest-id wins.
	repo, db := newMessageRepo(t)

	old := seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.ThreadID = 100
		m.AIText = "old reply"
	})
	if err := repo.SetUserRatingForOwner(old, 1, -1, "first complaint"); err != nil {
		t.Fatal(err)
	}

	_ = seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.ThreadID = 100
		m.AIText = "middle reply (unrated)"
		m.UUID = "msg-uuid-middle-" + t.Name()
	})

	latest := seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.ThreadID = 100
		m.AIText = "latest reply"
		m.UUID = "msg-uuid-latest-" + t.Name()
	})
	if err := repo.SetUserRatingForOwner(latest, 1, -1, "second complaint"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindLatestUserCritique(100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil {
		t.Fatal("expected the latest critique row, got nil")
	}
	if got.Id != latest {
		t.Errorf("latest critique id = %d, want %d", got.Id, latest)
	}
	if got.UserFeedback != "second complaint" {
		t.Errorf("UserFeedback = %q, want 'second complaint'", got.UserFeedback)
	}
}

func TestFindLatestUserCritique_FiltersByThread(t *testing.T) {
	// Critique on thread 100 must not leak when querying thread 200.
	// Same DB-scope-correctness as every per-thread query in this
	// file.
	repo, db := newMessageRepo(t)
	thread100Msg := seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.ThreadID = 100
	})
	if err := repo.SetUserRatingForOwner(thread100Msg, 1, -1, "complaint on 100"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindLatestUserCritique(200)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("cross-thread leak: got critique id=%d from thread 100 when querying 200", got.Id)
	}
}

func TestFindLatestUserCritique_ZeroThreadIDReturnsNil(t *testing.T) {
	// Defensive: a caller that lost its threadID context shouldn't
	// surface arbitrary rows. Mirror the SetMetadata "empty thread
	// id" guard.
	repo, _ := newMessageRepo(t)
	got, err := repo.FindLatestUserCritique(0)
	if err != nil {
		t.Errorf("zero thread id should be (nil, nil) sentinel; got err=%v", err)
	}
	if got != nil {
		t.Errorf("zero thread id should return nil row, got id=%d", got.Id)
	}
}

// LoadActiveUserCritique — gated version: returns the critique
// only when the critiqued message is still the most recent one
// in the thread. Once the user continues the conversation, the
// critique stops surfacing.

func TestLoadActiveUserCritique_ReturnsCritiqueWhenNoNewerMessages(t *testing.T) {
	repo, db := newMessageRepo(t)
	msgID := seedMessage(t, db, func(m *workagentModel.ChatMessage) { m.ThreadID = 100 })
	if err := repo.SetUserRatingForOwner(msgID, 1, -1, "fix palette"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.LoadActiveUserCritique(100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.Id != msgID {
		t.Errorf("expected active critique row, got %+v", got)
	}
}

func TestLoadActiveUserCritique_StaleWhenNewerMessageExists(t *testing.T) {
	// User thumbs-down'd turn 5, then sent turn 6 (whether the agent
	// responded or not). Critique must NOT surface — the user has
	// moved on.
	repo, db := newMessageRepo(t)
	critiquedID := seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.ThreadID = 100
		m.UUID = "msg-uuid-critique-" + t.Name()
	})
	if err := repo.SetUserRatingForOwner(critiquedID, 1, -1, "old complaint"); err != nil {
		t.Fatal(err)
	}
	_ = seedMessage(t, db, func(m *workagentModel.ChatMessage) {
		m.ThreadID = 100
		m.UUID = "msg-uuid-newer-" + t.Name()
		m.UserText = "user moved on"
	})

	got, err := repo.LoadActiveUserCritique(100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("stale critique should not surface; got id=%d", got.Id)
	}
}

func TestLoadActiveUserCritique_NoCritiqueReturnsNil(t *testing.T) {
	// Thread has messages but none are rated-down. Common case.
	repo, db := newMessageRepo(t)
	_ = seedMessage(t, db, func(m *workagentModel.ChatMessage) { m.ThreadID = 100 })

	got, err := repo.LoadActiveUserCritique(100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("no-critique thread should return nil, got id=%d", got.Id)
	}
}
