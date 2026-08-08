package model

import (
	"strings"
	"testing"

	"server/service/secrets"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func setupKey(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x42
	}
	secrets.SetKeyForTesting(key)
	t.Cleanup(secrets.ClearKeyForTesting)
}

type encryptedRow struct {
	Id      uint             `gorm:"primaryKey"`
	Payload EncryptedJSONMap `gorm:"type:text"`
}

func (encryptedRow) TableName() string { return "test_encrypted_row" }

func ensureSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS test_encrypted_row (id INTEGER PRIMARY KEY AUTOINCREMENT, payload TEXT)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func TestEncryptedJSONMap_RoundTrip(t *testing.T) {
	setupKey(t)
	db := testutil.NewTestDB(t)
	ensureSchema(t, db)

	in := EncryptedJSONMap{"API_KEY": "super-secret", "LOG": "info"}
	row := encryptedRow{Payload: in}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var got encryptedRow
	if err := db.First(&got, row.Id).Error; err != nil {
		t.Fatalf("first: %v", err)
	}
	if got.Payload["API_KEY"] != "super-secret" {
		t.Errorf("API_KEY round-trip lost: %v", got.Payload["API_KEY"])
	}
	if got.Payload["LOG"] != "info" {
		t.Errorf("LOG round-trip lost: %v", got.Payload["LOG"])
	}
}

func TestEncryptedJSONMap_DBStorageIsCiphertext(t *testing.T) {
	// The DB-on-disk shape must NOT contain the plaintext.
	// Read the raw payload column and assert (a) it's v1
	// envelope-shaped, and (b) the secret string is absent.
	setupKey(t)
	db := testutil.NewTestDB(t)
	ensureSchema(t, db)

	row := encryptedRow{Payload: EncryptedJSONMap{"API_KEY": "super-secret-canary-12345"}}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var raw string
	if err := db.Raw(`SELECT payload FROM test_encrypted_row WHERE id = ?`, row.Id).Scan(&raw).Error; err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if strings.Contains(raw, "super-secret-canary-12345") {
		t.Error("plaintext leaked into ciphertext column")
	}
	if !strings.HasPrefix(raw, "v1:") {
		t.Errorf("expected v1: envelope, got %q", raw[:min(len(raw), 40)])
	}
}

func TestEncryptedJSONMap_NilRoundTrip(t *testing.T) {
	setupKey(t)
	db := testutil.NewTestDB(t)
	ensureSchema(t, db)

	row := encryptedRow{Payload: nil}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got encryptedRow
	if err := db.First(&got, row.Id).Error; err != nil {
		t.Fatalf("first: %v", err)
	}
	if got.Payload != nil {
		t.Errorf("nil should round-trip as nil, got %v", got.Payload)
	}
}

func TestEncryptedJSONMap_EmptyMapRoundTrip(t *testing.T) {
	// Empty map should also be a no-op at the DB layer
	// (Value() short-circuits to NULL for len == 0).
	setupKey(t)
	db := testutil.NewTestDB(t)
	ensureSchema(t, db)

	row := encryptedRow{Payload: EncryptedJSONMap{}}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got encryptedRow
	if err := db.First(&got, row.Id).Error; err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(got.Payload) != 0 {
		t.Errorf("empty map round-trip gained entries: %v", got.Payload)
	}
}

func TestEncryptedJSONMap_LegacyPlainJSONReadable(t *testing.T) {
	// Back-compat path: Phase B1 rows wrote plain JSON. The
	// Scan() must still decode them so users don't lose their
	// existing configurations on the deploy that flips the
	// column to encrypted.
	setupKey(t)
	db := testutil.NewTestDB(t)
	ensureSchema(t, db)

	if err := db.Exec(
		`INSERT INTO test_encrypted_row (id, payload) VALUES (?, ?)`,
		99, `{"legacy":"value"}`,
	).Error; err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	var got encryptedRow
	if err := db.First(&got, 99).Error; err != nil {
		t.Fatalf("first: %v", err)
	}
	if got.Payload["legacy"] != "value" {
		t.Errorf("legacy plain-JSON read failed: %v", got.Payload)
	}
}

func TestEncryptedJSONMap_WrongKeyFailsScan(t *testing.T) {
	// Encrypt with one key; read with another. Scan returns
	// the decrypt error; GORM propagates that as the First()
	// error so the caller sees the row is unreadable.
	setupKey(t)
	db := testutil.NewTestDB(t)
	ensureSchema(t, db)

	row := encryptedRow{Payload: EncryptedJSONMap{"x": "y"}}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	// Rotate to a different key.
	other := make([]byte, 32)
	for i := range other {
		other[i] = 0x55
	}
	secrets.SetKeyForTesting(other)
	defer secrets.ClearKeyForTesting()

	var got encryptedRow
	err := db.First(&got, row.Id).Error
	if err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("expected decrypt error on wrong key, got %v", err)
	}
}

func TestEncryptedJSONMap_AsJSONMap(t *testing.T) {
	e := EncryptedJSONMap{"a": 1, "b": "two"}
	got := e.AsJSONMap()
	if got["a"] != 1 || got["b"] != "two" {
		t.Errorf("AsJSONMap lost data: %v", got)
	}

	var nilMap EncryptedJSONMap
	if nilMap.AsJSONMap() != nil {
		t.Error("nil should cast to nil")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
