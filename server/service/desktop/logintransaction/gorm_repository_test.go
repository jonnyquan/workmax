package logintransaction

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	ormlogger "gorm.io/gorm/logger"

	"server/service/secrets"
)

func newGORMLoginRepository(t *testing.T) (*GORMRepository, *gorm.DB) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x6d
	}
	secrets.SetKeyForTesting(key)
	t.Cleanup(secrets.ClearKeyForTesting)

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: ormlogger.Default.LogMode(ormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&loginTransactionRow{}); err != nil {
		t.Fatalf("migrate login transaction: %v", err)
	}
	repo, err := NewGORMRepository(db)
	if err != nil {
		t.Fatalf("NewGORMRepository: %v", err)
	}
	return repo, db
}

func persistentRecordFixture() Record {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	return Record{
		ID:      "transaction_1",
		Version: 1,
		State:   StatePending,
		Request: CreateInput{
			ClientID:            "workmax-desktop",
			RedirectURI:         "http://127.0.0.1:49152/oauth/callback",
			OAuthState:          "oauth-state-value",
			CodeChallenge:       strings.Repeat("a", 43),
			CodeChallengeMethod: "S256",
			Scope:               "workagent",
			DeviceID:            "2825400e4ecb442f7b842f022cd40d4e",
		},
		SecretHash: hashSecret("transaction-secret"),
		CreatedAt:  now,
		UpdatedAt:  now,
		ExpiresAt:  now.Add(10 * time.Minute),
	}
}

func TestGORMRepositoryRoundTripSealsRecoverableValues(t *testing.T) {
	repo, db := newGORMLoginRepository(t)
	record := persistentRecordFixture()
	if err := repo.Create(context.Background(), record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var row loginTransactionRow
	if err := db.Where("transaction_id = ?", record.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.OAuthStateCiphertext, record.Request.OAuthState) || !secrets.IsEnvelope(row.OAuthStateCiphertext) {
		t.Fatal("OAuth state was not stored as an encrypted envelope")
	}

	loaded, err := repo.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded != record {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", loaded, record)
	}
}

func TestGORMRepositoryCompareAndSwapPersistsAndRejectsStaleVersion(t *testing.T) {
	repo, _ := newGORMLoginRepository(t)
	record := persistentRecordFixture()
	if err := repo.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	updated, err := repo.CompareAndSwap(context.Background(), record.ID, 1, func(next *Record) error {
		next.State = StateGooglePending
		next.GoogleStateHash = hashSecret("provider-state")
		next.GoogleCodeVerifier = "provider-verifier"
		next.PasswordFailures = 1
		next.LastPasswordFailure = next.UpdatedAt.Add(500 * time.Millisecond)
		next.UpdatedAt = next.UpdatedAt.Add(time.Second)
		return nil
	})
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if updated.Version != 2 || updated.State != StateGooglePending {
		t.Fatalf("updated = %+v", updated)
	}
	loaded, err := repo.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.GoogleCodeVerifier != "provider-verifier" || loaded.GoogleStateHash != hashSecret("provider-state") ||
		loaded.PasswordFailures != 1 || loaded.LastPasswordFailure.IsZero() {
		t.Fatalf("sensitive mutation did not round-trip: %+v", loaded)
	}

	_, err = repo.CompareAndSwap(context.Background(), record.ID, 1, func(*Record) error { return nil })
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale CAS error = %v, want ErrVersionConflict", err)
	}
}

func TestGORMRepositoryRejectsTamperedSealedState(t *testing.T) {
	repo, db := newGORMLoginRepository(t)
	record := persistentRecordFixture()
	if err := repo.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&loginTransactionRow{}).
		Where("transaction_id = ?", record.ID).
		UpdateColumn("oauth_state_digest", digestBytes(hashSecret("different-state"))).Error; err != nil {
		t.Fatal(err)
	}
	_, err := repo.Get(context.Background(), record.ID)
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("tampered row error = %v, want ErrInvariantViolation", err)
	}
}

func TestGORMRepositoryRejectsImpossibleStateCombination(t *testing.T) {
	repo, db := newGORMLoginRepository(t)
	record := persistentRecordFixture()
	if err := repo.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&loginTransactionRow{}).
		Where("transaction_id = ?", record.ID).
		UpdateColumn("status", string(StateAuthenticated)).Error; err != nil {
		t.Fatal(err)
	}
	_, err := repo.Get(context.Background(), record.ID)
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("impossible state error = %v, want ErrInvariantViolation", err)
	}
}

func TestGORMRepositoryPreservesImmutableRequest(t *testing.T) {
	repo, _ := newGORMLoginRepository(t)
	record := persistentRecordFixture()
	if err := repo.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	_, err := repo.CompareAndSwap(context.Background(), record.ID, 1, func(next *Record) error {
		next.Request.RedirectURI = "http://127.0.0.1:49153/oauth/callback"
		return nil
	})
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("immutable mutation error = %v", err)
	}
}

func TestGORMRepositoryAllowsOAuthStateReuseAcrossIndependentTransactions(t *testing.T) {
	repo, _ := newGORMLoginRepository(t)
	first := persistentRecordFixture()
	if err := repo.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "transaction_2"
	second.SecretHash = hashSecret("another-transaction-secret")
	if err := repo.Create(context.Background(), second); err != nil {
		t.Fatalf("OAuth state equality must not be a global uniqueness constraint: %v", err)
	}
}
