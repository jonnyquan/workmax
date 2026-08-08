//go:build desktop

package sync

import (
	"context"
	"errors"
	"fmt"
	"log"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// MessagesJobDeps bundles what NewMessagesJob needs.
//
// Distinct from ThreadsJobDeps: the messages job is parameterized
// by a single thread (UUID + cloud thread ID), so deps include
// those rather than the entire local-thread set. A future
// "sync messages for ALL local threads" worker can either wrap
// NewMessagesJob in a loop OR add a NewAllMessagesJob that takes
// no thread params. For P1.B.3.x first cut we ship per-thread
// only.
type MessagesJobDeps struct {
	DB          *gorm.DB
	Cloud       *cloudproxy.Client
	TokenStore  *cloudproxy.TokenStore
	CursorStore *CursorStore

	// ThreadUUID is the cross-cloud-stable id. Used to compute
	// the per-thread cursor key (CursorKeyMessagesPrefix+uuid).
	ThreadUUID string

	// CloudThreadID is the cloud's PK for this thread. The
	// sidecar's local SQLite has its OWN autoincrement id which
	// differs; cloud endpoints scope by their PK. The threads
	// sync (P1.B.3) stores both — caller passes the cloud one.
	CloudThreadID uint64

	// ExpectedUID is frozen by MessagesSyncer.Trigger from the account that
	// selected the local thread. The goroutine must never let a later login use
	// that old trigger to read cloud data or write another account's rows.
	ExpectedUID uint64

	// ExpectedLease is frozen synchronously by MessagesSyncer.Trigger. Context
	// cancellation is still used to interrupt I/O, but it is not the identity
	// authority: context.AfterFunc delivery is asynchronous, so a same-UID
	// re-login could otherwise be acquired in the narrow window before the
	// cancellation callback runs. A non-zero lease must exactly match the lease
	// returned with the access token before this job may touch cloud/cache state.
	// Direct synchronous callers may leave it zero and use the lease acquired by
	// the job itself.
	ExpectedLease cloudproxy.SessionLease

	PageLimit       int // default 100
	MaxPagesPerTick int // default 50
}

// NewMessagesJob returns a JobFunc that drains the messages delta
// for one thread. Pattern mirrors NewThreadsJob (P1.B.3) — same
// token acquisition, uid-from-JWT, cursor advance, and bounded
// pagination semantics — but scoped to a single thread.
//
// Per-thread design rationale: the cloud's /sync/messages endpoint
// is scoped by thread_id; pagination is per-thread. Walking all
// local threads in a single JobFunc would be N*M HTTP calls per
// tick at the worst case. Per-thread jobs let a future worker
// fan out N threads as N triggers with single-job-running
// semantics intact (each JobFunc invocation handles one thread).
//
// Special paths
//   - ErrNoSession      → return nil (silent no-op tick)
//   - JWT UID mismatch  → fail before any messages cloud call or local write
//   - ErrSyncAuthExpired → force one revision-fenced refresh and retry the
//     rejected page exactly once
//   - ErrThreadNotOwnedOrMissing → return nil; the thread might
//     not have synced locally yet, OR
//     the user lost access. Either way
//     spamming the cloud doesn't help;
//     next worker tick can re-evaluate.
func NewMessagesJob(deps MessagesJobDeps) JobFunc {
	if deps.DB == nil || deps.Cloud == nil || deps.TokenStore == nil || deps.CursorStore == nil {
		panic("sync: NewMessagesJob requires non-nil DB, Cloud, TokenStore, CursorStore")
	}
	if deps.ThreadUUID == "" {
		panic("sync: NewMessagesJob requires non-empty ThreadUUID")
	}
	if deps.CloudThreadID == 0 {
		panic("sync: NewMessagesJob requires positive CloudThreadID")
	}
	if deps.ExpectedUID == 0 {
		panic("sync: NewMessagesJob requires positive ExpectedUID")
	}
	if deps.PageLimit <= 0 {
		deps.PageLimit = 100
	}
	if deps.MaxPagesPerTick <= 0 {
		deps.MaxPagesPerTick = 50
	}

	cursorKey := CursorKeyMessagesPrefix + deps.ThreadUUID

	return func(ctx context.Context) error {
		if deps.ExpectedLease.Epoch() != 0 {
			if err := deps.ExpectedLease.Check(); err != nil {
				return fmt.Errorf("messages sync (%s): frozen session: %w", deps.ThreadUUID, err)
			}
		}
		pair, lease, err := cloudproxy.AcquireAccessTokenWithLease(ctx, deps.TokenStore, deps.Cloud)
		if err != nil {
			if errors.Is(err, cloudproxy.ErrNoSession) {
				return nil
			}
			return fmt.Errorf("messages sync (%s): acquire token: %w", deps.ThreadUUID, err)
		}
		if deps.ExpectedLease.Epoch() != 0 {
			if err := deps.ExpectedLease.Check(); err != nil {
				return fmt.Errorf("messages sync (%s): frozen session after token acquire: %w",
					deps.ThreadUUID, err)
			}
			if !lease.SameSession(deps.ExpectedLease) {
				return fmt.Errorf("messages sync (%s): frozen session after token acquire: %w",
					deps.ThreadUUID, errSyncSessionChanged)
			}
		}
		sessionCtx, releaseLease := lease.BindContext(ctx)
		defer releaseLease()
		if err := checkSessionContext(sessionCtx, lease); err != nil {
			return fmt.Errorf("messages sync (%s): bind session: %w", deps.ThreadUUID, err)
		}

		uid, err := cloudproxy.ExtractUIDFromAccessToken(pair.AccessToken)
		if err != nil {
			return fmt.Errorf("messages sync (%s): parse uid: %w", deps.ThreadUUID, err)
		}
		if uid == 0 || uint64(uid) != deps.ExpectedUID {
			return fmt.Errorf("messages sync (%s): %w", deps.ThreadUUID, errSyncSessionChanged)
		}

		cursorStore := deps.CursorStore.WithContext(sessionCtx)
		cursor, err := cursorStore.Get(cursorKey)
		if err != nil {
			if leaseErr := lease.Check(); leaseErr != nil {
				return fmt.Errorf("messages sync (%s): load cursor: %w", deps.ThreadUUID, leaseErr)
			}
			return fmt.Errorf("messages sync (%s): load cursor: %w", deps.ThreadUUID, err)
		}

		totalUpserted := 0
		for page := 0; page < deps.MaxPagesPerTick; page++ {
			if err := checkSessionContext(sessionCtx, lease); err != nil {
				return err
			}

			result, err := deps.Cloud.ListMessagesDelta(
				sessionCtx, pair.AccessToken, deps.CloudThreadID, cursor, deps.PageLimit,
			)
			if err != nil {
				if leaseErr := lease.Check(); leaseErr != nil {
					return fmt.Errorf("messages sync (%s) page %d: %w", deps.ThreadUUID, page, leaseErr)
				}
				if errors.Is(err, cloudproxy.ErrSyncAuthExpired) {
					freshPair, refreshErr := recoverSyncAccessToken(
						sessionCtx, deps.TokenStore, deps.Cloud, pair, lease, uint(deps.ExpectedUID),
					)
					if refreshErr != nil {
						return fmt.Errorf("messages sync (%s): auth recovery failed: %w",
							deps.ThreadUUID, refreshErr)
					}
					pair = freshPair
					result, err = deps.Cloud.ListMessagesDelta(
						sessionCtx, pair.AccessToken, deps.CloudThreadID, cursor, deps.PageLimit,
					)
					if err != nil {
						if leaseErr := lease.Check(); leaseErr != nil {
							return fmt.Errorf("messages sync (%s) page %d after auth recovery: %w",
								deps.ThreadUUID, page, leaseErr)
						}
						return fmt.Errorf("messages sync (%s) page %d after auth recovery: %w",
							deps.ThreadUUID, page, err)
					}
				} else if errors.Is(err, cloudproxy.ErrThreadNotOwnedOrMissing) {
					// Threads sync hasn't landed this thread yet,
					// OR user lost access. Either way, no point
					// retrying immediately; let the worker's next
					// tick re-evaluate.
					return nil
				} else {
					return fmt.Errorf("messages sync (%s) page %d: %w",
						deps.ThreadUUID, page, err)
				}
			}

			pageUpserted := 0
			err = runSessionTransaction(sessionCtx, deps.DB, lease, func(tx *gorm.DB) error {
				var writeErr error
				pageUpserted, writeErr = UpsertMessages(tx, result.Items, int(uid))
				if writeErr != nil {
					return writeErr
				}
				if result.NextCursor != "" {
					if err := cursorStore.withDB(tx).Set(cursorKey, result.NextCursor); err != nil {
						return fmt.Errorf("messages sync (%s): save cursor: %w",
							deps.ThreadUUID, err)
					}
				}
				return nil
			})
			if err != nil {
				// Partial-page failure: do NOT advance the cursor
				// past this page; idempotent upsert will re-process
				// on the next tick.
				return err
			}
			totalUpserted += pageUpserted

			if result.NextCursor != "" {
				cursor = result.NextCursor
			}

			if !result.HasMore {
				break
			}
			if result.NextCursor == "" {
				// Defensive: cloud bug emitting HasMore=true with
				// empty NextCursor would infinite-loop us. Treat it
				// as a retryable page failure so the worker backs off
				// and the cursor is not advanced.
				return fmt.Errorf("messages sync (%s): cloud returned has_more=true with empty next_cursor",
					deps.ThreadUUID)
			}
		}

		if totalUpserted > 0 {
			log.Printf("messages sync (%s): tick complete, upserted %d row(s)",
				deps.ThreadUUID, totalUpserted)
		}
		return nil
	}
}
