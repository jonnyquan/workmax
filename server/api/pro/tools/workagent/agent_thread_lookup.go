package workagent

// agent_thread_lookup.go owns the per-thread cache + singleflight
// fetch path for ChatThread rows. Pulled out of agent_api.go (B1
// chunk 5) — getChatThread is its own concern (caching, singleflight,
// dual fast-path / cache-miss flow with strict CWE-639 defence) and
// belongs alongside its singleflight-group var rather than mixed
// into the handler file.
//
// Same package as agent_api.go; the receiver method on AIChatApiNew
// makes this just a file-organisation extraction.

import (
	"errors"
	"fmt"

	"server/globals"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"

	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// chatThreadSingleflight coalesces concurrent cache-miss DB lookups
// for the same threadId into one query. Hot scenario: after
// ClearMessages / ClearConversation invalidates ThreadCache, every
// concurrent HandleAgentChat for that thread used to fire its own
// SELECT WHERE id=? — N redundant queries for the same row. The
// singleflight key is just threadId because the row contents don't
// depend on uid; the per-caller uid check still happens on the
// shared result.
var chatThreadSingleflight singleflight.Group

// getChatThread gets chat thread with caching, returning the row only
// when the caller owns it.
//
// Returns one of three error shapes — caller does errors.Is on the
// sentinel to render the right HTTP status:
//
//   - gorm.ErrRecordNotFound — thread doesn't exist OR caller's uid
//     doesn't match. Same sentinel for both so we don't leak the
//     existence of other users' threads through the auth layer
//     (CWE-639 / IDOR defence). Maps to 404.
//   - wrapped DB error — DB connection failed / query crashed / etc.
//     Logged here with full context so ops can see the underlying
//     cause; caller sees a non-sentinel error and should map to 5xx.
//   - nil — thread loaded and ownership checked.
//
// Previously the cache-hit and DB-fetch paths reported errors
// inconsistently (cache-hit-on-mismatch returned the sentinel, but
// DB-miss-on-anything returned the raw err — so a transient DB
// hiccup looked like a not-found). Now both paths cleanly funnel
// "not found OR forbidden" through gorm.ErrRecordNotFound and
// surface real DB failures as wrapped errors.
//
// Embedding the uid check here (instead of leaving it to callers)
// is defence-in-depth — a future caller can't inherit a CWE-639
// IDOR by forgetting the follow-up comparison.
func (api *AIChatApiNew) getChatThread(threadId string, uid uint) (*workagentModel.ChatThread, error) {
	threadCache := workagentService.GetThreadCache()

	// Fast path: cache hit, no group coordination.
	if cachedThread, ok := threadCache.Get(threadId); ok {
		if uid != 0 && uint(cachedThread.UID) != uid {
			return nil, gorm.ErrRecordNotFound
		}
		return cachedThread, nil
	}

	// Cache miss: dedupe the DB fetch via singleflight so a thundering
	// herd on the same threadId (post-cache-invalidation, popular
	// shared thread, concurrent first-turn submits) collapses to one
	// SELECT. The singleflight key is just threadId — the row is uid-
	// independent and the per-caller ownership check happens after.
	//
	// We deliberately do NOT Set the cache inside the Do body: the
	// pre-singleflight design only cached after the caller's uid
	// check passed, ensuring a cross-tenant attacker landing on a
	// cold cache slot couldn't poison subsequent reads with a row we
	// have no business caching for them. Preserving that property
	// matters for both the IDOR defence-in-depth and for test
	// isolation (the busy-path SSE test fails noisily if a prior
	// cross-tenant test populated the cache from the singleflight).
	v, err, _ := chatThreadSingleflight.Do(threadId, func() (interface{}, error) {
		// Re-check cache inside the singleflight: a concurrent
		// resolver may have populated it between our top-of-function
		// check and acquiring the slot.
		if cachedThread, ok := threadCache.Get(threadId); ok {
			return cachedThread, nil
		}
		chatThread := &workagentModel.ChatThread{}
		if err := globals.GraDBs["system"].Where("id = ?", threadId).First(chatThread).Error; err != nil {
			return nil, err
		}
		return chatThread, nil
	})

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		// Real DB error (connection failure, timeout, malformed
		// query, etc.). Log with context — the caller's branch on
		// errors.Is(err, gorm.ErrRecordNotFound) will be false so
		// it'll return 5xx, but ops needs the underlying error
		// here to diagnose.
		globals.Error(fmt.Sprintf("[ChatThread] DB lookup failed for thread %s: %v", threadId, err))
		return nil, fmt.Errorf("getChatThread DB lookup: %w", err)
	}

	chatThread := v.(*workagentModel.ChatThread)
	if uid != 0 && uint(chatThread.UID) != uid {
		return nil, gorm.ErrRecordNotFound
	}

	threadCache.Set(threadId, chatThread)
	return chatThread, nil
}
