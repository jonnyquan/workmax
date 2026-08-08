package oauth

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	model "server/model/desktop/oauth"
)

func newRefreshService(t *testing.T) (*RefreshChainService, *time.Time, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	seedDesktopClient(t, db)
	now := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	clock := &now
	return &RefreshChainService{
		db:     db,
		now:    func() time.Time { return *clock },
		random: rand.Reader,
	}, clock, db
}

func sampleIssue() IssueInput {
	return IssueInput{
		ChainID:  "chain-uuid-001",
		DeviceID: "device-uuid-001",
		ClientID: model.DesktopClientID,
		UID:      42,
		Scope:    "workagent",
	}
}

func TestRefreshChain_Issue_FirstTokenRootsChain(t *testing.T) {
	svc, clock, db := newRefreshService(t)
	ctx := context.Background()

	issued, err := svc.Issue(ctx, sampleIssue())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.Token == "" {
		t.Error("expected non-empty token")
	}
	if issued.ChainID != "chain-uuid-001" || issued.DeviceID != "device-uuid-001" {
		t.Fatalf("issued credential binding drifted: chain=%q device=%q", issued.ChainID, issued.DeviceID)
	}
	if !issued.ExpiresAt.Equal(clock.Add(model.RefreshTokenTTL)) {
		t.Errorf("ExpiresAt: got %v, want now+90d", issued.ExpiresAt)
	}

	var stored model.RefreshToken
	if err := db.First(&stored, issued.ID).Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.ParentID != nil {
		t.Errorf("first token in chain: ParentID must be nil, got %v", stored.ParentID)
	}
	if stored.ChainID != "chain-uuid-001" {
		t.Errorf("ChainID: got %q", stored.ChainID)
	}
	if stored.Revoked {
		t.Error("fresh token must not be revoked")
	}
}

func TestRefreshChain_Rotate_Happy(t *testing.T) {
	svc, _, db := newRefreshService(t)
	ctx := context.Background()

	issued, _ := svc.Issue(ctx, sampleIssue())

	rotated, err := svc.Rotate(ctx, RotateInput{
		PresentedToken: issued.Token,
		ClientID:       model.DesktopClientID,
	})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated.Token == issued.Token {
		t.Error("rotated token must differ from old")
	}
	if rotated.ChainID != "chain-uuid-001" || rotated.DeviceID != "device-uuid-001" {
		t.Fatalf("rotated credential binding drifted: chain=%q device=%q", rotated.ChainID, rotated.DeviceID)
	}

	// Old row: revoked=rotated, last_used_at set.
	var old model.RefreshToken
	if err := db.First(&old, issued.ID).Error; err != nil {
		t.Fatalf("re-read old: %v", err)
	}
	if !old.Revoked {
		t.Error("old token should be revoked after rotation")
	}
	if old.RevokedReason == nil || *old.RevokedReason != model.RevokedReasonRotated {
		t.Errorf("RevokedReason: got %v, want %q", old.RevokedReason, model.RevokedReasonRotated)
	}
	if old.LastUsedAt == nil {
		t.Error("LastUsedAt should be set on rotated row")
	}

	// New row: parent_id points to old.
	var newRow model.RefreshToken
	if err := db.First(&newRow, rotated.ID).Error; err != nil {
		t.Fatalf("re-read new: %v", err)
	}
	if newRow.ParentID == nil || *newRow.ParentID != issued.ID {
		t.Errorf("ParentID: got %v, want %d", newRow.ParentID, issued.ID)
	}
	if newRow.ChainID != "chain-uuid-001" {
		t.Errorf("ChainID drift: got %q", newRow.ChainID)
	}
	if newRow.DeviceID != "device-uuid-001" {
		t.Errorf("DeviceID drift: got %q (must inherit from parent)", newRow.DeviceID)
	}
	if newRow.Revoked {
		t.Error("fresh rotated token must not be revoked")
	}
}

// TestRefreshChain_ThreeRotations checks the chain linkage after
// multiple rotations: 4 rows total, 3 revoked='rotated', last active.
func TestRefreshChain_ThreeRotations(t *testing.T) {
	svc, _, db := newRefreshService(t)
	ctx := context.Background()

	issued, _ := svc.Issue(ctx, sampleIssue())

	current := issued
	tokenIDs := []uint{issued.ID}
	for i := 0; i < 3; i++ {
		next, err := svc.Rotate(ctx, RotateInput{
			PresentedToken: current.Token,
			ClientID:       model.DesktopClientID,
		})
		if err != nil {
			t.Fatalf("Rotate #%d: %v", i+1, err)
		}
		tokenIDs = append(tokenIDs, next.ID)
		current = next
	}

	var all []model.RefreshToken
	if err := db.Where("chain_id = ?", "chain-uuid-001").Order("id ASC").Find(&all).Error; err != nil {
		t.Fatalf("query chain: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 rows in chain, got %d", len(all))
	}

	// First 3 should be revoked='rotated', last active.
	for i, row := range all {
		if i < 3 {
			if !row.Revoked || row.RevokedReason == nil || *row.RevokedReason != model.RevokedReasonRotated {
				t.Errorf("row[%d]: expected revoked='rotated', got revoked=%v reason=%v", i, row.Revoked, row.RevokedReason)
			}
		} else {
			if row.Revoked {
				t.Errorf("row[%d] (newest): should be active, got revoked=%v", i, row.Revoked)
			}
		}
		// All share device_id from the root.
		if row.DeviceID != "device-uuid-001" {
			t.Errorf("row[%d]: DeviceID drifted to %q", i, row.DeviceID)
		}
	}

	// Chain linkage: row[1].parent_id == row[0].id, etc.
	for i := 1; i < len(all); i++ {
		if all[i].ParentID == nil || *all[i].ParentID != all[i-1].ID {
			t.Errorf("row[%d].ParentID: got %v, want %d", i, all[i].ParentID, all[i-1].ID)
		}
	}
}

// TestRefreshChain_Replay verifies the critical security flow: if an
// already-rotated token is re-presented, the WHOLE chain (including
// the currently-active token) is killed.
func TestRefreshChain_Replay_RevokesEntireChain(t *testing.T) {
	svc, _, db := newRefreshService(t)
	ctx := context.Background()

	issued, _ := svc.Issue(ctx, sampleIssue())

	rotated, err := svc.Rotate(ctx, RotateInput{
		PresentedToken: issued.Token,
		ClientID:       model.DesktopClientID,
	})
	if err != nil {
		t.Fatalf("first Rotate: %v", err)
	}

	// Attacker (or buggy client) replays the original token.
	_, err = svc.Rotate(ctx, RotateInput{
		PresentedToken: issued.Token,
		ClientID:       model.DesktopClientID,
	})
	if !errors.Is(err, ErrRefreshReplayDetected) {
		t.Fatalf("expected ErrRefreshReplayDetected, got %v", err)
	}

	// Verify state:
	//   - issued: revoked='rotated' (still — replay sweep doesn't
	//     touch already-revoked rows)
	//   - rotated: revoked='replay_detected' (was active, now killed)
	var issuedRow, rotatedRow model.RefreshToken
	_ = db.First(&issuedRow, issued.ID).Error
	_ = db.First(&rotatedRow, rotated.ID).Error

	if !issuedRow.Revoked || *issuedRow.RevokedReason != model.RevokedReasonRotated {
		t.Errorf("issued row reason: got %v, want %q (history preserved)", issuedRow.RevokedReason, model.RevokedReasonRotated)
	}
	if !rotatedRow.Revoked || rotatedRow.RevokedReason == nil || *rotatedRow.RevokedReason != model.RevokedReasonReplayDetected {
		t.Errorf("rotated (active) row should be revoked='replay_detected', got revoked=%v reason=%v", rotatedRow.Revoked, rotatedRow.RevokedReason)
	}

	// Subsequent Rotate of the (now-killed) `rotated` token also
	// triggers the replay sweep (it's already revoked).
	_, err = svc.Rotate(ctx, RotateInput{
		PresentedToken: rotated.Token,
		ClientID:       model.DesktopClientID,
	})
	if !errors.Is(err, ErrRefreshReplayDetected) {
		t.Errorf("replay on killed-chain token: expected ErrRefreshReplayDetected, got %v", err)
	}
}

func TestRefreshChain_Rotate_RejectsUnknown(t *testing.T) {
	svc, _, _ := newRefreshService(t)
	_, err := svc.Rotate(context.Background(), RotateInput{
		PresentedToken: "no-such-token",
		ClientID:       model.DesktopClientID,
	})
	if !errors.Is(err, ErrRefreshTokenNotFound) {
		t.Errorf("expected ErrRefreshTokenNotFound, got %v", err)
	}
}

func TestRefreshChain_Rotate_RejectsExpired(t *testing.T) {
	svc, clock, _ := newRefreshService(t)
	ctx := context.Background()

	issued, _ := svc.Issue(ctx, sampleIssue())
	*clock = clock.Add(model.RefreshTokenTTL + time.Hour)

	_, err := svc.Rotate(ctx, RotateInput{
		PresentedToken: issued.Token,
		ClientID:       model.DesktopClientID,
	})
	if !errors.Is(err, ErrRefreshTokenExpired) {
		t.Errorf("expected ErrRefreshTokenExpired, got %v", err)
	}
}

func TestRefreshChain_Rotate_RejectsClientMismatch(t *testing.T) {
	svc, _, _ := newRefreshService(t)
	ctx := context.Background()

	issued, _ := svc.Issue(ctx, sampleIssue())

	_, err := svc.Rotate(ctx, RotateInput{
		PresentedToken: issued.Token,
		ClientID:       "different-client",
	})
	if !errors.Is(err, ErrRefreshClientMismatch) {
		t.Errorf("expected ErrRefreshClientMismatch, got %v", err)
	}
	// Crucially, mismatch is NOT escalated to replay revocation.
	// Legitimate client should still be able to refresh.
	rotated, err := svc.Rotate(context.Background(), RotateInput{
		PresentedToken: issued.Token,
		ClientID:       model.DesktopClientID,
	})
	if err != nil {
		t.Errorf("after mismatch, correct client should still succeed: %v", err)
	}
	if rotated.Token == "" {
		t.Error("expected new token")
	}
}

// TestRefreshChain_Revoke_UserInitiated covers the explicit logout
// path (P1 /oauth/revoke endpoint will call this).
func TestRefreshChain_Revoke_UserInitiated(t *testing.T) {
	svc, _, db := newRefreshService(t)
	ctx := context.Background()

	issued, _ := svc.Issue(ctx, sampleIssue())
	// Rotate twice so we have one rotated + one active.
	rot1, _ := svc.Rotate(ctx, RotateInput{PresentedToken: issued.Token, ClientID: model.DesktopClientID})
	_, _ = svc.Rotate(ctx, RotateInput{PresentedToken: rot1.Token, ClientID: model.DesktopClientID})

	n, err := svc.Revoke(ctx, "chain-uuid-001")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row revoked (only the active one); got %d", n)
	}

	var newest model.RefreshToken
	if err := db.Where("chain_id = ?", "chain-uuid-001").Order("id DESC").First(&newest).Error; err != nil {
		t.Fatalf("query newest: %v", err)
	}
	if !newest.Revoked || newest.RevokedReason == nil || *newest.RevokedReason != model.RevokedReasonUserRevoked {
		t.Errorf("newest should be revoked='user_revoked'; got revoked=%v reason=%v", newest.Revoked, newest.RevokedReason)
	}

	// Idempotency: re-revoke does nothing.
	n2, _ := svc.Revoke(ctx, "chain-uuid-001")
	if n2 != 0 {
		t.Errorf("re-revoke: expected 0 affected, got %d", n2)
	}
}
