package oauth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"

	model "server/model/desktop/oauth"
)

var (
	ErrDeviceSessionInvalid  = errors.New("device_session: invalid binding")
	ErrDeviceSessionInactive = errors.New("device_session: session is not active")
)

// DeviceSessionService validates the signed access-token binding against the
// active OAuth client and refresh chain. It is the stateful half of Device
// Session admission; checking signed claim presence alone cannot observe
// logout, refresh replay revocation or an administratively disabled client.
type DeviceSessionService struct {
	db  *gorm.DB
	now func() time.Time
}

func NewDeviceSessionService(db *gorm.DB) *DeviceSessionService {
	return &DeviceSessionService{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// ValidateAccessSession deliberately returns the same inactive sentinel for
// every binding miss so a resource endpoint cannot be used to enumerate users,
// devices or refresh-chain IDs.
func (s *DeviceSessionService) ValidateAccessSession(
	ctx context.Context,
	uid uint,
	clientID, deviceID, deviceSessionID, grantedScope string,
) error {
	if ctx == nil || s == nil || s.db == nil || uid == 0 || uint64(uid) > uint64(math.MaxInt) ||
		strings.TrimSpace(clientID) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceSessionID) == "" ||
		strings.TrimSpace(grantedScope) == "" || grantedScope != strings.TrimSpace(grantedScope) {
		return ErrDeviceSessionInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var client model.Client
	err := s.db.WithContext(ctx).
		Where("client_id = ? AND is_active = ?", clientID, true).
		First(&client).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDeviceSessionInactive
		}
		return fmt.Errorf("device_session: load client: %w", err)
	}

	var refresh model.RefreshToken
	err = s.db.WithContext(ctx).
		Where(
			"chain_id = ? AND device_id = ? AND client_id = ? AND uid = ? AND scope = ? AND revoked = ? AND expires_at > ?",
			deviceSessionID,
			deviceID,
			clientID,
			int(uid),
			grantedScope,
			false,
			s.now().UTC(),
		).
		First(&refresh).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDeviceSessionInactive
		}
		return fmt.Errorf("device_session: load refresh chain: %w", err)
	}
	return nil
}
