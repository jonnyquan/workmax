package oauth

import (
	"context"
	"errors"
	"testing"
	"time"

	model "server/model/desktop/oauth"
)

func TestDeviceSessionValidatesActiveRefreshChainBinding(t *testing.T) {
	refresh, clock, db := newRefreshService(t)
	issued, err := refresh.Issue(context.Background(), sampleIssue())
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewDeviceSessionService(db)
	sessions.now = func() time.Time { return *clock }

	if err := sessions.ValidateAccessSession(
		context.Background(),
		42,
		model.DesktopClientID,
		issued.DeviceID,
		issued.ChainID,
		model.DesktopOAuthScopeWorkAgent,
	); err != nil {
		t.Fatalf("active session rejected: %v", err)
	}

	bindings := []struct {
		name      string
		uid       uint
		clientID  string
		deviceID  string
		sessionID string
		scope     string
	}{
		{name: "wrong user", uid: 7, clientID: model.DesktopClientID, deviceID: issued.DeviceID, sessionID: issued.ChainID, scope: model.DesktopOAuthScopeWorkAgent},
		{name: "wrong client", uid: 42, clientID: "other", deviceID: issued.DeviceID, sessionID: issued.ChainID, scope: model.DesktopOAuthScopeWorkAgent},
		{name: "wrong device", uid: 42, clientID: model.DesktopClientID, deviceID: "other-device", sessionID: issued.ChainID, scope: model.DesktopOAuthScopeWorkAgent},
		{name: "wrong chain", uid: 42, clientID: model.DesktopClientID, deviceID: issued.DeviceID, sessionID: "other-chain", scope: model.DesktopOAuthScopeWorkAgent},
		{name: "wrong scope", uid: 42, clientID: model.DesktopClientID, deviceID: issued.DeviceID, sessionID: issued.ChainID, scope: "agent.run"},
	}
	for _, binding := range bindings {
		t.Run(binding.name, func(t *testing.T) {
			err := sessions.ValidateAccessSession(
				context.Background(),
				binding.uid,
				binding.clientID,
				binding.deviceID,
				binding.sessionID,
				binding.scope,
			)
			if !errors.Is(err, ErrDeviceSessionInactive) {
				t.Fatalf("binding error = %v, want ErrDeviceSessionInactive", err)
			}
		})
	}
}

func TestDeviceSessionTracksRotationRevocationExpiryAndClientState(t *testing.T) {
	refresh, clock, db := newRefreshService(t)
	issued, err := refresh.Issue(context.Background(), sampleIssue())
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewDeviceSessionService(db)
	sessions.now = func() time.Time { return *clock }
	validate := func() error {
		return sessions.ValidateAccessSession(
			context.Background(),
			42,
			model.DesktopClientID,
			issued.DeviceID,
			issued.ChainID,
			model.DesktopOAuthScopeWorkAgent,
		)
	}

	rotated, err := refresh.Rotate(context.Background(), RotateInput{
		PresentedToken: issued.Token,
		ClientID:       model.DesktopClientID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(); err != nil {
		t.Fatalf("rotation should keep the device session active: %v", err)
	}
	if _, err := refresh.RevokeByToken(context.Background(), rotated.Token, model.DesktopClientID); err != nil {
		t.Fatal(err)
	}
	if err := validate(); !errors.Is(err, ErrDeviceSessionInactive) {
		t.Fatalf("revoked chain error = %v, want inactive", err)
	}

	second := sampleIssue()
	second.ChainID = "chain-uuid-002"
	secondIssued, err := refresh.Issue(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	*clock = secondIssued.ExpiresAt
	if err := sessions.ValidateAccessSession(context.Background(), 42, model.DesktopClientID, secondIssued.DeviceID, secondIssued.ChainID, model.DesktopOAuthScopeWorkAgent); !errors.Is(err, ErrDeviceSessionInactive) {
		t.Fatalf("expired chain error = %v, want inactive", err)
	}

	*clock = secondIssued.ExpiresAt.Add(-model.RefreshTokenTTL)
	if err := db.Model(&model.Client{}).Where("client_id = ?", model.DesktopClientID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := sessions.ValidateAccessSession(context.Background(), 42, model.DesktopClientID, secondIssued.DeviceID, secondIssued.ChainID, model.DesktopOAuthScopeWorkAgent); !errors.Is(err, ErrDeviceSessionInactive) {
		t.Fatalf("disabled client error = %v, want inactive", err)
	}
}

func TestDeviceSessionRejectsIncompleteBinding(t *testing.T) {
	sessions := NewDeviceSessionService(nil)
	if err := sessions.ValidateAccessSession(context.Background(), 42, model.DesktopClientID, "device", "session", model.DesktopOAuthScopeWorkAgent); !errors.Is(err, ErrDeviceSessionInvalid) {
		t.Fatalf("nil store error = %v, want ErrDeviceSessionInvalid", err)
	}
}
