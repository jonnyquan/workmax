package testutil

import (
	"context"
	"database/sql/driver"
	"testing"
)

func TestNewPersistentTestDBEnablesForeignKeysOnReplacementConnection(t *testing.T) {
	db := NewPersistentTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}

	ctx := context.Background()
	worker, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("open worker connection: %v", err)
	}
	if err := worker.Raw(func(any) error { return driver.ErrBadConn }); err != driver.ErrBadConn {
		_ = worker.Close()
		t.Fatalf("discard worker connection: %v", err)
	}
	_ = worker.Close()

	replacement, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("open replacement connection: %v", err)
	}
	defer replacement.Close()
	var enabled int
	if err := replacement.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatalf("read replacement foreign_keys pragma: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("replacement foreign_keys = %d, want 1", enabled)
	}
}
