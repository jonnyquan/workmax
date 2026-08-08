//go:build desktop

package sync

import (
	"context"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// runSessionTransaction makes one downloaded page an all-or-nothing local
// commit bound to the authenticated session epoch. Entity changes and the
// page cursor share one explicit SQLite transaction, and the entire short
// Begin/write/Commit section runs under SessionLease.WithCurrent.
//
// TokenStore's production tombstone uses this same SQLite database and orders
// login/logout as TokenStore.mu -> SQLite. Taking WithCurrent before Begin
// preserves that order; acquiring it only for Commit would invert the order
// (SQLite write tx -> TokenStore.mu) and could deadlock with Save/Clear.
func runSessionTransaction(
	ctx context.Context,
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	fn func(tx *gorm.DB) error,
) error {
	return runSessionTransactionWithCommit(ctx, db, lease, fn, func(tx *gorm.DB) error {
		return tx.Commit().Error
	})
}

// runSessionTransactionWithCommit exposes only the final commit action to
// focused tests. commit is called inside SessionLease.WithCurrent and must obey
// that API's no-TokenStore/no-cloud-I/O rule.
func runSessionTransactionWithCommit(
	ctx context.Context,
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	fn func(tx *gorm.DB) error,
	commit func(tx *gorm.DB) error,
) error {
	if err := lease.Check(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if commit == nil {
		panic("sync: runSessionTransactionWithCommit requires commit callback")
	}
	return lease.WithCurrent(func() error {
		// WithCurrent holds TokenStore.mu. Neither fn nor commit may call any
		// TokenStore/SessionLease method or perform cloud I/O.
		if err := ctx.Err(); err != nil {
			return err
		}
		tx := db.WithContext(ctx).Begin()
		if tx.Error != nil {
			return tx.Error
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback().Error
			}
		}()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := commit(tx); err != nil {
			return err
		}
		committed = true
		return nil
	})
}

// checkSessionContext gives epoch invalidation priority over the merged
// context error. This keeps logout/re-login observable as ErrSessionChanged,
// while ordinary worker shutdown/deadline still returns ctx.Err().
func checkSessionContext(ctx context.Context, lease cloudproxy.SessionLease) error {
	if err := lease.Check(); err != nil {
		return err
	}
	return ctx.Err()
}
