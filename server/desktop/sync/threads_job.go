//go:build desktop

package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// ThreadsJobDeps bundles everything NewThreadsJob needs. Explicit
// struct (vs positional args) so the call site at main.go reads
// clearly + a future field addition doesn't break signatures.
type ThreadsJobDeps struct {
	// DB is the local SQLite. UpsertThreads writes through it.
	DB *gorm.DB

	// The sidecar talks to the configured Go Server /api/desktop/sync/* routes.
	Cloud *cloudproxy.Client

	// TokenStore owns the Keychain-backed access+refresh pair.
	// The job pulls a (possibly-refreshed) access token via
	// cloudproxy.AcquireAccessToken on every tick. UID is derived
	// from the access token's JWT claims each tick, so the JobFunc
	// can boot before the user signs in.
	TokenStore *cloudproxy.TokenStore

	// CursorStore persists the resume cursor across sidecar restarts.
	CursorStore *CursorStore

	// PageLimit caps the per-call page size we request from the
	// cloud. Defaults to 100 (cloud's DefaultLimit) when zero.
	PageLimit int

	// MaxPagesPerTick bounds how many pages a single tick drains.
	// Defends against a pathological "cursor never advances" loop
	// (e.g. cloud bug emitting items with identical timestamps).
	// Defaults to 50 (= 5000 threads/tick at default page size,
	// well above the power-user threshold in cloud-sync.md).
	MaxPagesPerTick int

	// MessagesSyncer — optional. When non-nil, AFTER the threads
	// drain completes, the job fires per-thread message-sync
	// triggers for the RecentThreadsToSync most-recently-updated
	// local threads that have non-zero message_count. This is the
	// "offline prep" path; the on-demand sync (P1.B.3.x.2) handles the
	// renderer-clicks-a-thread case; this handles the
	// user-wants-everything-cached case.
	//
	// Per-thread coalesce in the syncer means we don't spawn
	// duplicate goroutines for threads already syncing.
	MessagesSyncer *MessagesSyncer

	// RecentThreadsToSync caps how many local threads get a
	// post-tick message-sync trigger. Set to 0 to disable the
	// post-tick message-sync entirely even if MessagesSyncer is
	// non-nil. The desktop main wiring uses 20, covering the active
	// set for most users without fanning out N goroutines for power
	// users with 1000+ threads.
	RecentThreadsToSync int
}

// NewThreadsJob returns a JobFunc the SyncWorker can invoke. Each
// tick the function:
//
//  1. Acquires a non-expired access token (refreshing if needed)
//  2. Derives and validates its positive UID, then reads that account's
//     persisted ThreadsCursorKey
//  3. Calls cloud.ListThreadsDelta(cursor) — loops while HasMore
//     until drained or MaxPagesPerTick exhausted
//  4. Upserts each page's items into local w_workagent_thread
//  5. Saves the latest cursor after each successful page
//
// Errors from any step short-circuit the tick + propagate to the
// worker, which engages its backoff. The cursor saved up to the
// last-fully-processed page survives — next tick resumes from
// there (no double-processing because upsert is idempotent;
// nothing-missed because cursor was saved per-page).
//
// Special handling: cloud's ErrSyncAuthExpired (HTTP 401) forces the shared
// revision-fenced refresh path immediately, even when the local access-token
// expiry is still in the future, and retries the rejected page exactly once.
func NewThreadsJob(deps ThreadsJobDeps) JobFunc {
	if deps.DB == nil || deps.Cloud == nil || deps.TokenStore == nil || deps.CursorStore == nil {
		panic("sync: NewThreadsJob requires non-nil DB, Cloud, TokenStore, CursorStore")
	}
	if deps.PageLimit <= 0 {
		deps.PageLimit = 100
	}
	if deps.MaxPagesPerTick <= 0 {
		deps.MaxPagesPerTick = 50
	}

	return func(ctx context.Context) error {
		// 1. Token. ErrNoSession means the user hasn't (or no
		//    longer has) authenticated — silently skip this tick
		//    rather than spam errors; the LoginPage / OAuth flow
		//    will trigger a new sync once the user signs in again.
		pair, lease, err := cloudproxy.AcquireAccessTokenWithLease(ctx, deps.TokenStore, deps.Cloud)
		if err != nil {
			if errors.Is(err, cloudproxy.ErrNoSession) {
				return nil // no-op tick; not a failure
			}
			return fmt.Errorf("threads sync: acquire token: %w", err)
		}
		sessionCtx, releaseLease := lease.BindContext(ctx)
		defer releaseLease()
		if err := checkSessionContext(sessionCtx, lease); err != nil {
			return fmt.Errorf("threads sync: bind session: %w", err)
		}

		// 2. UID from the access token's JWT claims. The same positive UID
		//    scopes both thread rows and the resume cursor. Never fall back
		//    to the legacy global cursor: a subsequent account must start
		//    from its own empty cursor rather than inherit another account's
		//    position.
		uid, err := cloudproxy.ExtractUIDFromAccessToken(pair.AccessToken)
		if err != nil {
			return fmt.Errorf("threads sync: parse uid: %w", err)
		}
		cursorKey, err := ThreadsCursorKey(uid)
		if err != nil {
			return fmt.Errorf("threads sync: invalid uid: %w", err)
		}

		// 3. Drain pages from the stored cursor.
		cursorStore := deps.CursorStore.WithContext(sessionCtx)
		cursor, err := cursorStore.Get(cursorKey)
		if err != nil {
			if leaseErr := lease.Check(); leaseErr != nil {
				return fmt.Errorf("threads sync: load cursor: %w", leaseErr)
			}
			return fmt.Errorf("threads sync: load cursor: %w", err)
		}

		totalUpserted := 0
		for page := 0; page < deps.MaxPagesPerTick; page++ {
			if err := checkSessionContext(sessionCtx, lease); err != nil {
				return err
			}

			result, err := deps.Cloud.ListThreadsDelta(sessionCtx, pair.AccessToken, cursor, deps.PageLimit)
			if err != nil {
				if leaseErr := lease.Check(); leaseErr != nil {
					return fmt.Errorf("threads sync: list page %d: %w", page, leaseErr)
				}
				if errors.Is(err, cloudproxy.ErrSyncAuthExpired) {
					freshPair, refreshErr := recoverSyncAccessToken(
						sessionCtx, deps.TokenStore, deps.Cloud, pair, lease, uid,
					)
					if refreshErr != nil {
						return fmt.Errorf("threads sync: auth recovery failed: %w", refreshErr)
					}
					pair = freshPair
					result, err = deps.Cloud.ListThreadsDelta(
						sessionCtx, pair.AccessToken, cursor, deps.PageLimit,
					)
					if err != nil {
						if leaseErr := lease.Check(); leaseErr != nil {
							return fmt.Errorf("threads sync: list page %d after auth recovery: %w",
								page, leaseErr)
						}
						return fmt.Errorf("threads sync: list page %d after auth recovery: %w", page, err)
					}
				} else {
					return fmt.Errorf("threads sync: list page %d: %w", page, err)
				}
			}

			pageUpserted := 0
			err = runSessionTransaction(sessionCtx, deps.DB, lease, func(tx *gorm.DB) error {
				txCursorStore := cursorStore.withDB(tx)
				var writeErr error
				pageUpserted, writeErr = UpsertThreads(
					tx, result.Items, int(uid), txCursorStore,
				)
				if writeErr != nil {
					return writeErr
				}
				if result.NextCursor != "" {
					if err := txCursorStore.Set(cursorKey, result.NextCursor); err != nil {
						return fmt.Errorf("threads sync: save cursor: %w", err)
					}
				}
				return nil
			})
			if err != nil {
				// Partial-page failure: save the cursor at where we
				// got to (a SUCCESSFULLY-processed last page would
				// have updated cursor already; here we DON'T advance
				// because n < len(items) means the cursor's next-
				// page implication is ahead of where we actually
				// committed). Just return.
				return err
			}
			totalUpserted += pageUpserted

			// Advance cursor if we actually wrote items + cloud gave
			// us a next pointer. NextCursor empty = we caught up
			// (HasMore=false → cursor unchanged per service contract).
			if result.NextCursor != "" {
				cursor = result.NextCursor
			}

			if !result.HasMore {
				break
			}

			// Defensive: if the cloud returns HasMore=true but
			// NextCursor is empty (would be a cloud bug), we'd
			// loop forever. Treat it as a retryable page failure so
			// the worker backs off and the cursor is not advanced.
			if result.NextCursor == "" {
				return fmt.Errorf("threads sync: cloud returned has_more=true with empty next_cursor")
			}
		}

		if totalUpserted > 0 {
			log.Printf("threads sync: tick complete, upserted %d row(s)", totalUpserted)
		}

		// P1.B.3.x.3: fan out periodic message-sync triggers for
		// the most-recently-updated local threads. The on-demand
		// path (P1.B.3.x.2) handles "user opened a thread"; this
		// closes the "user wants everything cached for offline"
		// gap. MessagesSyncer's per-thread coalesce makes back-
		// to-back ticks safe — repeat triggers for the same
		// thread while one is in flight collapse to a no-op.
		if deps.MessagesSyncer != nil && deps.RecentThreadsToSync > 0 {
			if err := triggerPeriodicMessageSyncForSession(
				sessionCtx, lease, deps.DB, int(uid), deps.MessagesSyncer, deps.RecentThreadsToSync,
			); err != nil {
				return err
			}
		}
		return nil
	}
}

// triggerPeriodicMessageSync queries the N most-recently-updated
// local non-paused threads with non-zero message_count and a valid
// cloud_thread_id, then fires a MessagesSyncer.Trigger for each.
// Returns immediately — the
// syncer handles the work async; per-thread coalesce keeps it safe.
//
// Filters by uid to defense-in-depth the future multi-account
// case; today uuid is globally unique so the filter is redundant
// but harmless.
//
// message_count > 0 filter: pointless to sync messages for a
// thread that has none. Skipping these avoids N empty 200 OK
// round-trips per tick on a power user's mostly-empty threads.
func triggerPeriodicMessageSyncForSession(
	ctx context.Context,
	lease cloudproxy.SessionLease,
	db *gorm.DB,
	uid int,
	syncer *MessagesSyncer,
	limit int,
) error {
	if db == nil || syncer == nil || uid <= 0 || limit <= 0 {
		return nil
	}
	if err := checkSessionContext(ctx, lease); err != nil {
		return err
	}
	type row struct {
		UUID          string
		CloudThreadID string
	}
	var rows []row
	err := db.WithContext(ctx).Raw(
		`SELECT uuid, COALESCE(cloud_thread_id, '') AS cloud_thread_id
		   FROM w_workagent_thread
		  WHERE uid = ? AND message_count > 0
		    AND agent_type = 'general_agent'
		    AND COALESCE(cloud_thread_id, '') <> ''
		    AND COALESCE(cloud_sync_state, 'synced') <> 'paused'
		 ORDER BY updated_at DESC, id DESC
		 LIMIT ?`,
		uid, limit,
	).Scan(&rows).Error
	if err != nil {
		if leaseErr := lease.Check(); leaseErr != nil {
			return leaseErr
		}
		log.Printf("threads sync: periodic message-sync query failed: %v", err)
		return nil
	}
	triggered := 0
	for _, r := range rows {
		if err := checkSessionContext(ctx, lease); err != nil {
			return err
		}
		cloudThreadID, err := strconv.ParseUint(r.CloudThreadID, 10, 64)
		if err != nil || cloudThreadID == 0 {
			continue
		}
		if syncer.triggerForSession(lease, r.UUID, cloudThreadID, uint64(uid)) {
			triggered++
		}
	}
	if triggered > 0 {
		log.Printf("threads sync: fired %d periodic message-sync trigger(s)", triggered)
	}
	return lease.Check()
}

// triggerPeriodicMessageSync keeps the focused helper/test surface stable. It
// freezes the currently authenticated epoch before delegating to the same
// session-bound production path used by NewThreadsJob.
func triggerPeriodicMessageSync(db *gorm.DB, uid int, syncer *MessagesSyncer, limit int) {
	if db == nil || syncer == nil || uid <= 0 || limit <= 0 {
		return
	}
	lease, err := syncer.deps.TokenStore.AcquireSessionLease()
	if err != nil {
		return
	}
	ctx, release := lease.BindContext(syncer.deps.ParentCtx)
	defer release()
	_ = triggerPeriodicMessageSyncForSession(ctx, lease, db, uid, syncer, limit)
}
