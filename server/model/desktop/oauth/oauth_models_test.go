package oauth

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// TestTableNames pins the explicit table names so a careless rename of
// the Go struct can't silently break the model ↔ migration contract.
func TestTableNames(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{Client{}.TableName(), "w_desktop_oauth_client"},
		{AuthorizationCode{}.TableName(), "w_desktop_oauth_authorization_code"},
		{RefreshToken{}.TableName(), "w_desktop_oauth_refresh_token"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("TableName(): got %q, want %q", c.got, c.want)
		}
	}
}

// TestConstants pins the well-known string constants the service layer
// will look up by name. Catches accidental rename of e.g. DesktopClientID.
func TestConstants(t *testing.T) {
	if DesktopClientID != "workmax-desktop" {
		t.Errorf("DesktopClientID: got %q, want %q", DesktopClientID, "workmax-desktop")
	}
	if ClientTypePublic != "public" {
		t.Errorf("ClientTypePublic: got %q", ClientTypePublic)
	}
	if ClientTypeConfidential != "confidential" {
		t.Errorf("ClientTypeConfidential: got %q", ClientTypeConfidential)
	}
	if CodeChallengeMethodS256 != "S256" {
		t.Errorf("CodeChallengeMethodS256: got %q", CodeChallengeMethodS256)
	}
	if AuthorizationCodeTTL != 10*time.Minute {
		t.Errorf("AuthorizationCodeTTL: got %v, want 10m", AuthorizationCodeTTL)
	}
	if RefreshTokenTTL != 90*24*time.Hour {
		t.Errorf("RefreshTokenTTL: got %v, want 90d", RefreshTokenTTL)
	}
	wantReasons := map[string]bool{
		RevokedReasonRotated:        true,
		RevokedReasonReplayDetected: true,
		RevokedReasonUserRevoked:    true,
		RevokedReasonExpired:        true,
	}
	if len(wantReasons) != 4 {
		t.Errorf("expected 4 distinct revoked reason constants, got %d", len(wantReasons))
	}
}

// TestAutoMigrateRoundTrip exercises the GORM tags by running the
// three models through AutoMigrate against an in-memory SQLite. If
// any tag is malformed, AutoMigrate panics or returns an error here
// before the model ever sees production MySQL.
//
// We can't validate every MySQL-specific concern this way (JSON type,
// FK constraints, etc.) but it catches the bulk of typos in struct
// tags — the most common breakage in practice.
func TestAutoMigrateRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	if err := db.AutoMigrate(&Client{}, &AuthorizationCode{}, &RefreshToken{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Verify each table actually came into being.
	for _, table := range []string{"w_desktop_oauth_client", "w_desktop_oauth_authorization_code", "w_desktop_oauth_refresh_token"} {
		if !db.Migrator().HasTable(table) {
			t.Errorf("AutoMigrate did not create table %q", table)
		}
	}

	// Round-trip a Client row. RedirectURIs is stored as raw JSON
	// string at this layer; service layer owns Marshal/Unmarshal.
	now := time.Now().UTC()
	client := Client{
		ClientID:      DesktopClientID,
		ClientName:    "WorkMax Desktop",
		ClientType:    ClientTypePublic,
		RedirectURIs:  `["http://127.0.0.1:*/oauth/callback"]`,
		AllowedScopes: `["workagent"]`,
		IsActive:      true,
		CreatedAt:     &now,
		UpdatedAt:     &now,
	}
	if err := db.Create(&client).Error; err != nil {
		t.Fatalf("create Client: %v", err)
	}
	if client.ID == 0 {
		t.Error("expected auto-incremented ID, got 0")
	}

	var got Client
	if err := db.Where("client_id = ?", DesktopClientID).First(&got).Error; err != nil {
		t.Fatalf("lookup Client: %v", err)
	}
	if got.ClientName != "WorkMax Desktop" || got.ClientType != ClientTypePublic {
		t.Errorf("round-trip mismatch: got %+v", got)
	}

	// Round-trip an AuthorizationCode row with TTL expiry.
	authCode := AuthorizationCode{
		Code:                "test-code-12345",
		ClientID:            DesktopClientID,
		UID:                 1,
		DeviceID:            stringPointer("2825400e4ecb442f7b842f022cd40d4e"),
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       "test-challenge",
		CodeChallengeMethod: CodeChallengeMethodS256,
		Scope:               "workagent",
		ExpiresAt:           now.Add(AuthorizationCodeTTL),
		CreatedAt:           &now,
	}
	if err := db.Create(&authCode).Error; err != nil {
		t.Fatalf("create AuthorizationCode: %v", err)
	}
	var storedCode AuthorizationCode
	if err := db.First(&storedCode, authCode.ID).Error; err != nil {
		t.Fatalf("re-read AuthorizationCode: %v", err)
	}
	if storedCode.DeviceID == nil || *storedCode.DeviceID != "2825400e4ecb442f7b842f022cd40d4e" {
		t.Fatalf("AuthorizationCode device binding did not round-trip: %+v", storedCode.DeviceID)
	}

	// Round-trip a RefreshToken with chain rotation pointers.
	refresh := RefreshToken{
		Token:     "test-refresh-token-12345",
		ChainID:   "chain-1",
		DeviceID:  "device-1",
		ClientID:  DesktopClientID,
		UID:       1,
		Scope:     "workagent",
		ExpiresAt: now.Add(RefreshTokenTTL),
		CreatedAt: &now,
	}
	if err := db.Create(&refresh).Error; err != nil {
		t.Fatalf("create RefreshToken: %v", err)
	}
	if refresh.ParentID != nil {
		t.Errorf("expected ParentID to be nil for new chain root, got %v", refresh.ParentID)
	}

	// Simulate rotation: create child pointing at refresh as parent.
	parentID := refresh.ID
	reason := RevokedReasonRotated
	child := RefreshToken{
		Token:     "test-refresh-token-67890",
		ChainID:   "chain-1",
		DeviceID:  "device-1",
		ClientID:  DesktopClientID,
		UID:       1,
		Scope:     "workagent",
		ParentID:  &parentID,
		ExpiresAt: now.Add(RefreshTokenTTL),
		CreatedAt: &now,
	}
	if err := db.Create(&child).Error; err != nil {
		t.Fatalf("create child RefreshToken: %v", err)
	}
	// Mark parent as rotated.
	if err := db.Model(&RefreshToken{}).Where("id = ?", refresh.ID).
		Updates(map[string]any{
			"revoked":        true,
			"revoked_reason": reason,
		}).Error; err != nil {
		t.Fatalf("rotate parent: %v", err)
	}

	var parentAfter RefreshToken
	if err := db.First(&parentAfter, refresh.ID).Error; err != nil {
		t.Fatalf("re-read parent: %v", err)
	}
	if !parentAfter.Revoked {
		t.Error("expected parent to be revoked after rotation")
	}
	if parentAfter.RevokedReason == nil || *parentAfter.RevokedReason != RevokedReasonRotated {
		t.Errorf("expected revoked_reason=%q, got %v", RevokedReasonRotated, parentAfter.RevokedReason)
	}
}

func stringPointer(value string) *string { return &value }
