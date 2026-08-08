package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	model "server/model/desktop/oauth"
)

// newCodeService returns a CodeService bound to the test DB, with
// the now-source pointable at any wall clock the test wants.
func newCodeService(t *testing.T) (*CodeService, *time.Time) {
	t.Helper()
	db := newTestDB(t)
	seedDesktopClient(t, db)
	now := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	clock := &now
	return &CodeService{
		db:     db,
		now:    func() time.Time { return *clock },
		random: rand.Reader,
	}, clock
}

func sampleInput() GenerateInput {
	return GenerateInput{
		ClientID:            model.DesktopClientID,
		UID:                 42,
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       "abc-challenge",
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
	}
}

func TestCodeService_Generate_RoundTrip(t *testing.T) {
	svc, clock := newCodeService(t)
	ctx := context.Background()

	g, err := svc.Generate(ctx, sampleInput())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if g.Code == "" {
		t.Error("expected non-empty code")
	}
	// 32 bytes → base64 raw url should be 43 chars.
	if len(g.Code) != 43 {
		t.Errorf("expected 43-char code (32 bytes base64url), got %d chars: %q", len(g.Code), g.Code)
	}
	// Expiry should be exactly now + AuthorizationCodeTTL.
	if !g.ExpiresAt.Equal(clock.Add(model.AuthorizationCodeTTL)) {
		t.Errorf("ExpiresAt: got %v, want %v", g.ExpiresAt, clock.Add(model.AuthorizationCodeTTL))
	}

	// Verify the row landed.
	var stored model.AuthorizationCode
	if err := svc.db.Where("code = ?", g.Code).First(&stored).Error; err != nil {
		t.Fatalf("lookup stored code: %v", err)
	}
	if stored.UID != 42 || stored.ClientID != model.DesktopClientID {
		t.Errorf("stored row mismatch: %+v", stored)
	}
	if stored.Used {
		t.Error("freshly generated code should have used=false")
	}
}

func TestCodeService_Consume_Happy(t *testing.T) {
	svc, _ := newCodeService(t)
	ctx := context.Background()

	g, err := svc.Generate(ctx, sampleInput())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	consumed, err := svc.Consume(ctx, g.Code)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if consumed.ClientID != model.DesktopClientID ||
		consumed.UID != 42 ||
		consumed.RedirectURI != "http://127.0.0.1:54321/oauth/callback" ||
		consumed.CodeChallenge != "abc-challenge" ||
		consumed.Scope != "workagent" {
		t.Errorf("ConsumedCode mismatch: %+v", consumed)
	}

	// Row should now be marked used.
	var stored model.AuthorizationCode
	if err := svc.db.Where("code = ?", g.Code).First(&stored).Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !stored.Used {
		t.Error("row should be used=true after Consume")
	}
}

func TestCodeService_Consume_RejectsReuse(t *testing.T) {
	svc, _ := newCodeService(t)
	ctx := context.Background()

	g, _ := svc.Generate(ctx, sampleInput())
	if _, err := svc.Consume(ctx, g.Code); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	_, err := svc.Consume(ctx, g.Code)
	if !errors.Is(err, ErrCodeAlreadyUsed) {
		t.Errorf("second Consume: expected ErrCodeAlreadyUsed, got %v", err)
	}
}

func TestCodeService_Consume_RejectsExpired(t *testing.T) {
	svc, clock := newCodeService(t)
	ctx := context.Background()

	g, _ := svc.Generate(ctx, sampleInput())
	// Advance clock past TTL.
	*clock = clock.Add(model.AuthorizationCodeTTL + time.Second)

	_, err := svc.Consume(ctx, g.Code)
	if !errors.Is(err, ErrCodeExpired) {
		t.Errorf("expected ErrCodeExpired, got %v", err)
	}
}

func TestCodeService_Consume_RejectsAtExactExpiryBoundary(t *testing.T) {
	svc, clock := newCodeService(t)
	g, _ := svc.Generate(context.Background(), sampleInput())
	*clock = g.ExpiresAt

	_, err := svc.Consume(context.Background(), g.Code)
	if !errors.Is(err, ErrCodeExpired) {
		t.Errorf("exact-expiry consume: expected ErrCodeExpired, got %v", err)
	}
}

func TestCodeService_Consume_RejectsUnknown(t *testing.T) {
	svc, _ := newCodeService(t)
	_, err := svc.Consume(context.Background(), "no-such-code-string")
	if !errors.Is(err, ErrCodeNotFound) {
		t.Errorf("expected ErrCodeNotFound, got %v", err)
	}
}

func TestCodeService_Generate_RandomSeedInjection(t *testing.T) {
	svc, _ := newCodeService(t)
	// Swap the random source for a deterministic byte stream so the
	// code string is predictable. This is the seam tests will use
	// when we need to assert on code value bytes.
	deterministic := bytes.NewReader([]byte("0123456789abcdefghijklmnopqrstuv")) // exactly 32 bytes
	svc.random = deterministic

	g, err := svc.Generate(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The bytes "0123456789abcdefghijklmnopqrstuv" base64url-encoded
	// (no padding) is deterministic — pin it.
	want := "MDEyMzQ1Njc4OWFiY2RlZmdoaWprbG1ub3BxcnN0dXY"
	if g.Code != want {
		t.Errorf("deterministic code: got %q, want %q", g.Code, want)
	}
}
