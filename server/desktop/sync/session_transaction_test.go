//go:build desktop

package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

const sameDBTombstoneKey = "test_session_tombstone"

// sameDBTombstone mirrors the production lock-relevant behavior: TokenStore
// Mark/Unmark operations use the exact SQLite database that sync transactions
// write. The fixed value is non-sensitive.
type sameDBTombstone struct {
	db *gorm.DB
}

func (m *sameDBTombstone) IsMarked() (bool, error) {
	var value string
	err := m.db.Raw(`SELECT value FROM _local_meta WHERE key = ? LIMIT 1`, sameDBTombstoneKey).
		Row().Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value == "1", nil
}

func (m *sameDBTombstone) Mark() error {
	return m.db.Exec(`
		INSERT INTO _local_meta (key, value, updated_at) VALUES (?, '1', ?)
		ON CONFLICT(key) DO UPDATE SET value = '1', updated_at = excluded.updated_at`,
		sameDBTombstoneKey, time.Now().UTC().Format(time.RFC3339Nano),
	).Error
}

func (m *sameDBTombstone) Unmark() error {
	return m.db.Exec(`DELETE FROM _local_meta WHERE key = ?`, sameDBTombstoneKey).Error
}

func TestRunSessionTransaction_ReplacementFirstSkipsEntityAndCursorTransaction(t *testing.T) {
	tests := []struct {
		name   string
		retire func(*cloudproxy.TokenStore) error
	}{
		{
			name: "logout",
			retire: func(store *cloudproxy.TokenStore) error {
				return store.Clear()
			},
		},
		{
			name: "same uid login",
			retire: func(store *cloudproxy.TokenStore) error {
				return store.Save(cloudproxy.TokenPair{
					AccessToken:      mintJWTWithUID(42),
					AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
					RefreshToken:     "replacement-refresh",
					RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
					Scope:            "workagent",
				})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openCursorTestDB(t)
			if err := db.Exec(`CREATE TABLE session_rows (id INTEGER PRIMARY KEY, value TEXT)`).Error; err != nil {
				t.Fatal(err)
			}
			store := cloudproxy.NewTokenStore(newMemKeychainForJob())
			if err := store.Save(cloudproxy.TokenPair{
				AccessToken:      mintJWTWithUID(42),
				AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
				RefreshToken:     "initial-refresh",
				RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
				Scope:            "workagent",
			}); err != nil {
				t.Fatal(err)
			}
			lease, err := store.AcquireSessionLease()
			if err != nil {
				t.Fatalf("acquire lease: %v", err)
			}
			ctx, release := lease.BindContext(context.Background())
			defer release()
			cursorStore := NewCursorStore(db)
			cursorKey := fmt.Sprintf("session_transaction_%s", tc.name)

			if err := tc.retire(store); err != nil {
				t.Fatalf("replace session before transaction: %v", err)
			}
			callbackCalled := false
			err = runSessionTransaction(ctx, db, lease, func(tx *gorm.DB) error {
				callbackCalled = true
				if err := tx.Exec(`INSERT INTO session_rows (id, value) VALUES (1, 'old-session')`).Error; err != nil {
					return err
				}
				return cursorStore.withDB(tx).Set(cursorKey, "old-cursor")
			})
			if !errors.Is(err, cloudproxy.ErrSessionChanged) {
				t.Fatalf("transaction error = %v, want ErrSessionChanged", err)
			}
			if callbackCalled {
				t.Fatal("replacement-first transaction callback was invoked")
			}

			var rows int64
			if err := db.Raw(`SELECT count(*) FROM session_rows`).Row().Scan(&rows); err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if rows != 0 {
				t.Fatalf("retired session committed %d entity row(s)", rows)
			}
			cursor, err := cursorStore.Get(cursorKey)
			if err != nil {
				t.Fatalf("read cursor: %v", err)
			}
			if cursor != "" {
				t.Fatalf("retired session committed cursor %q", cursor)
			}
		})
	}
}

func TestRunSessionTransaction_CommitLinearizesBeforeSaveOrClear(t *testing.T) {
	tests := []struct {
		name   string
		retire func(*cloudproxy.TokenStore) error
	}{
		{
			name: "logout waits for commit",
			retire: func(store *cloudproxy.TokenStore) error {
				return store.Clear()
			},
		},
		{
			name: "same uid login waits for commit",
			retire: func(store *cloudproxy.TokenStore) error {
				return store.Save(cloudproxy.TokenPair{
					AccessToken:      mintJWTWithUID(42),
					AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
					RefreshToken:     "replacement-after-commit",
					RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
					Scope:            "workagent",
				})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openCursorTestDB(t)
			if err := db.Exec(`CREATE TABLE session_rows (id INTEGER PRIMARY KEY, value TEXT)`).Error; err != nil {
				t.Fatal(err)
			}
			store := cloudproxy.NewTokenStore(newMemKeychainForJob())
			if err := store.Save(cloudproxy.TokenPair{
				AccessToken:      mintJWTWithUID(42),
				AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
				RefreshToken:     "initial-refresh",
				RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
				Scope:            "workagent",
			}); err != nil {
				t.Fatal(err)
			}
			lease, err := store.AcquireSessionLease()
			if err != nil {
				t.Fatalf("acquire lease: %v", err)
			}
			ctx, release := lease.BindContext(context.Background())
			defer release()
			cursorStore := NewCursorStore(db)
			cursorKey := "session_commit_first"
			commitEntered := make(chan struct{})
			releaseCommit := make(chan struct{})
			txDone := make(chan error, 1)

			go func() {
				txDone <- runSessionTransactionWithCommit(
					ctx,
					db,
					lease,
					func(tx *gorm.DB) error {
						if err := tx.Exec(`INSERT INTO session_rows (id, value) VALUES (1, 'old-session')`).Error; err != nil {
							return err
						}
						return cursorStore.withDB(tx).Set(cursorKey, "old-cursor")
					},
					func(tx *gorm.DB) error {
						close(commitEntered)
						<-releaseCommit
						return tx.Commit().Error
					},
				)
			}()

			select {
			case <-commitEntered:
			case <-time.After(time.Second):
				t.Fatal("transaction did not reach guarded commit")
			}
			retireStarted := make(chan struct{})
			retireDone := make(chan error, 1)
			go func() {
				close(retireStarted)
				retireDone <- tc.retire(store)
			}()
			<-retireStarted
			select {
			case err := <-retireDone:
				t.Fatalf("session replacement escaped guarded commit, err=%v", err)
			case <-time.After(30 * time.Millisecond):
			}

			close(releaseCommit)
			select {
			case err := <-txDone:
				if err != nil {
					t.Fatalf("commit transaction: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("guarded commit did not finish")
			}
			select {
			case err := <-retireDone:
				if err != nil {
					t.Fatalf("replace session after commit: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("session replacement did not resume after commit")
			}

			var rows int64
			if err := db.Raw(`SELECT count(*) FROM session_rows`).Row().Scan(&rows); err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if rows != 1 {
				t.Fatalf("commit-first transaction left %d entity row(s), want 1", rows)
			}
			cursor, err := cursorStore.Get(cursorKey)
			if err != nil {
				t.Fatalf("read cursor: %v", err)
			}
			if cursor != "old-cursor" {
				t.Fatalf("commit-first cursor = %q, want old-cursor", cursor)
			}
			if err := lease.Check(); !errors.Is(err, cloudproxy.ErrSessionChanged) {
				t.Fatalf("old lease after replacement = %v, want ErrSessionChanged", err)
			}
		})
	}
}

func TestRunSessionTransaction_TombstoneOnSameDBKeepsTokenStoreBeforeSQLiteOrder(t *testing.T) {
	db := openCursorTestDB(t)
	if err := db.Exec(`CREATE TABLE session_rows (id INTEGER PRIMARY KEY, value TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	marker := &sameDBTombstone{db: db}
	store := cloudproxy.NewTokenStoreWithTombstone(newMemKeychainForJob(), marker)
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "initial-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed tombstone-backed store: %v", err)
	}
	lease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	ctx, release := lease.BindContext(context.Background())
	defer release()
	cursorStore := NewCursorStore(db)
	bodyEntered := make(chan struct{})
	releaseBody := make(chan struct{})
	txDone := make(chan error, 1)

	go func() {
		txDone <- runSessionTransaction(ctx, db, lease, func(tx *gorm.DB) error {
			if err := tx.Exec(`INSERT INTO session_rows (id, value) VALUES (1, 'old-session')`).Error; err != nil {
				return err
			}
			close(bodyEntered)
			<-releaseBody
			return cursorStore.withDB(tx).Set("same_db_cursor", "committed")
		})
	}()
	select {
	case <-bodyEntered:
	case <-time.After(time.Second):
		t.Fatal("sync transaction did not acquire guard and SQLite transaction")
	}

	clearStarted := make(chan struct{})
	clearDone := make(chan error, 1)
	go func() {
		close(clearStarted)
		clearDone <- store.Clear()
	}()
	<-clearStarted
	select {
	case err := <-clearDone:
		t.Fatalf("Clear bypassed guarded sync transaction, err=%v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseBody)
	select {
	case err := <-txDone:
		if err != nil {
			t.Fatalf("sync transaction: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sync transaction deadlocked with same-DB tombstone Clear")
	}
	select {
	case err := <-clearDone:
		if err != nil {
			t.Fatalf("Clear after sync transaction: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-DB tombstone Clear did not resume after sync transaction")
	}

	var rows int64
	if err := db.Raw(`SELECT count(*) FROM session_rows`).Row().Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("transaction rows = %d, want 1", rows)
	}
	cursor, err := cursorStore.Get("same_db_cursor")
	if err != nil || cursor != "committed" {
		t.Fatalf("cursor = %q, err=%v; want committed", cursor, err)
	}
	marked, err := marker.IsMarked()
	if err != nil || !marked {
		t.Fatalf("logout tombstone marked=%v err=%v, want true", marked, err)
	}
}
