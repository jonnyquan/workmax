package oauth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"gorm.io/gorm"

	model "server/model/desktop/oauth"
)

// RefreshTokenBytes is the byte length of the random token before
// base64url encoding. 32 bytes = 256 bits of entropy.
const RefreshTokenBytes = 32

// Refresh-chain service errors. /token handler maps all of these to
// the wire-level `invalid_grant` OAuth error; we keep the distinct
// sentinels so logs and metrics can separate the cases.
var (
	ErrRefreshTokenNotFound  = errors.New("refresh_chain: token not found")
	ErrRefreshTokenExpired   = errors.New("refresh_chain: token has expired")
	ErrRefreshClientMismatch = errors.New("refresh_chain: client_id does not match the token's chain")
	ErrRefreshReplayDetected = errors.New("refresh_chain: presented token was already revoked — chain marked as replay_detected")
)

// RefreshChainService owns the rotation + replay-detection mechanics
// for oauth_refresh_token. One chain represents one OAuth session;
// each successful refresh rotates the token (new row, parent_id
// pointing at the previous, old row marked revoked=true reason=rotated).
//
// Replay detection: if any code path presents a refresh token that
// is already revoked, we treat the chain as compromised — sweep every
// non-revoked row in the same chain_id to revoked=true reason=
// replay_detected. The attacker AND the legitimate client both lose
// access; the legitimate client must re-authenticate. That's the
// standard "rotation + replay" model in OAuth 2.1.
type RefreshChainService struct {
	db     *gorm.DB
	now    func() time.Time
	random io.Reader
}

// NewRefreshChainService returns a service backed by the given DB.
// Production uses time.Now (UTC) and crypto/rand.Reader.
func NewRefreshChainService(db *gorm.DB) *RefreshChainService {
	return &RefreshChainService{
		db:     db,
		now:    func() time.Time { return time.Now().UTC() },
		random: rand.Reader,
	}
}

// IssueInput is the payload for starting a brand-new refresh chain
// (called by /token's authorization_code grant after the code is
// consumed). The caller generates `chain_id` (any opaque string;
// we recommend a UUID).
type IssueInput struct {
	ChainID    string
	DeviceID   string
	ClientID   string
	UID        int
	Scope      string
	DeviceInfo *string // optional JSON blob (os, app_version, hostname)
}

// Issued is what the caller gets back: the new refresh token string
// + its expiry + the DB row's ID (handy for logging and for tying
// the corresponding access token's claims back to this chain).
//
// UID / ClientID / Scope echo the values stored on the row so the
// /token handler can mint a matching access token without a second
// DB read. They are always populated.
type Issued struct {
	Token     string
	ExpiresAt time.Time
	ID        uint
	ChainID   string
	DeviceID  string
	UID       int
	ClientID  string
	Scope     string
}

// Issue persists the first refresh token in a new chain. Used by the
// authorization_code grant; the refresh_token grant uses Rotate.
func (s *RefreshChainService) Issue(ctx context.Context, in IssueInput) (Issued, error) {
	return s.insertNew(ctx, in.ChainID, in.DeviceID, in.ClientID, in.UID, in.Scope, in.DeviceInfo, nil)
}

// RotateInput is the payload for refresh-token-grant rotation.
//
// ClientID comes from the request body and MUST match the token's
// stored ClientID. Mismatch is treated as invalid_grant (we don't
// escalate to replay revocation — could just be a confused client).
type RotateInput struct {
	PresentedToken string
	ClientID       string
}

// Rotate consumes the presented refresh token and issues a new one
// in the same chain. The old token is marked revoked=true,
// reason=rotated; the new token's parent_id points to the old.
//
// Failure modes (checked in this order — REVOKED is checked first so
// a replay attack on an expired+revoked token still triggers chain
// revocation):
//
//   - Lookup miss        → ErrRefreshTokenNotFound
//   - Already revoked    → ErrRefreshReplayDetected (chain swept,
//     sweep COMMITS — it must persist even
//     though we return a non-nil error)
//   - Past ExpiresAt     → ErrRefreshTokenExpired
//   - ClientID mismatch  → ErrRefreshClientMismatch
//
// Implementation note: the lookup and the replay-sweep run OUTSIDE
// any transaction so a returned-error from the rotation transaction
// doesn't roll the sweep back. The rotation itself is one transaction
// with a CAS guard (`WHERE id = ? AND revoked = ?`) so a concurrent
// rotation cannot produce two children.
func (s *RefreshChainService) Rotate(ctx context.Context, in RotateInput) (Issued, error) {
	// 1. Lookup the presented token. Outside any transaction; the
	//    rotation below will re-check liveness via the CAS update.
	var current model.RefreshToken
	err := s.db.WithContext(ctx).Where("token = ?", in.PresentedToken).First(&current).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Issued{}, ErrRefreshTokenNotFound
		}
		return Issued{}, fmt.Errorf("refresh_chain: select: %w", err)
	}

	// 2. Replay detection. Critically: this runs OUTSIDE any
	//    transaction so the sweep COMMITS. If we ran it inside a
	//    transaction that then returns ErrRefreshReplayDetected,
	//    GORM would roll the sweep back and leave the chain alive.
	if current.Revoked {
		if sweepErr := s.sweepChainAsReplay(s.db.WithContext(ctx), current.ChainID); sweepErr != nil {
			return Issued{}, fmt.Errorf("%w (chain sweep error: %v)", ErrRefreshReplayDetected, sweepErr)
		}
		return Issued{}, ErrRefreshReplayDetected
	}

	// 3. Remaining validations (cheap, no DB writes).
	now := s.now()
	if now.After(current.ExpiresAt) {
		return Issued{}, ErrRefreshTokenExpired
	}
	if current.ClientID != in.ClientID {
		return Issued{}, ErrRefreshClientMismatch
	}

	// 4. Atomic rotation: mark current rotated AND insert child in
	//    one transaction. CAS guard (`revoked = false`) defends
	//    against a concurrent rotate slipping in between our SELECT
	//    and this UPDATE.
	var issued Issued
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.RefreshToken{}).
			Where("id = ? AND revoked = ?", current.ID, false).
			Updates(map[string]any{
				"revoked":        true,
				"revoked_reason": model.RevokedReasonRotated,
				"last_used_at":   now,
			})
		if res.Error != nil {
			return fmt.Errorf("refresh_chain: mark rotated: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// Lost the race: another rotation/revoke happened between
			// our SELECT and this UPDATE. Safe default — refuse this
			// caller and force re-login. We don't sweep here: whoever
			// won the race already handled the chain state correctly.
			return ErrRefreshReplayDetected
		}

		parentID := current.ID
		newIssued, err := s.insertNewTx(
			tx,
			current.ChainID, current.DeviceID, current.ClientID,
			current.UID, current.Scope, current.DeviceInfo,
			&parentID,
		)
		if err != nil {
			return err
		}
		issued = newIssued
		return nil
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// Revoke marks all non-revoked tokens in the given chain as
// revoked=true reason=user_revoked. Used by /oauth/revoke (P1 endpoint)
// when the user explicitly signs out a device.
//
// Idempotent: re-revoking an already-revoked chain is a no-op.
func (s *RefreshChainService) Revoke(ctx context.Context, chainID string) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.RefreshToken{}).
		Where("chain_id = ? AND revoked = ?", chainID, false).
		Updates(map[string]any{
			"revoked":        true,
			"revoked_reason": model.RevokedReasonUserRevoked,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("refresh_chain: revoke chain: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// RevokeByToken looks up the chain owning the presented token and
// revokes every non-revoked row in it. Used by RFC 7009 /revoke
// endpoint where the client presents a token (not a chain id).
//
// Per RFC 7009 §2.2 the revoke endpoint MUST return 200 even when
// the token is unrecognized — the security goal is "don't leak token
// validity by varying response shape". This function returns
// ErrRefreshTokenNotFound for the unrecognized case so the caller
// can distinguish for telemetry, but should still respond 200 to
// the client.
//
// If the presented token is already revoked, returns the chain's
// row count (whether or not any rows actually changed) with no
// error — re-revoking is idempotent.
//
// clientID is an additional safety check: if the token belongs to
// a different client, return ErrRefreshClientMismatch to prevent
// one client from revoking another's chain. RFC 7009 §2.1 says the
// authorization server SHOULD check this.
func (s *RefreshChainService) RevokeByToken(ctx context.Context, presentedToken, clientID string) (int64, error) {
	if presentedToken == "" {
		return 0, ErrRefreshTokenNotFound
	}
	var row model.RefreshToken
	err := s.db.WithContext(ctx).
		Where("token = ?", presentedToken).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrRefreshTokenNotFound
		}
		return 0, fmt.Errorf("refresh_chain: revoke by token: lookup: %w", err)
	}
	if clientID != "" && row.ClientID != clientID {
		return 0, ErrRefreshClientMismatch
	}
	return s.Revoke(ctx, row.ChainID)
}

// insertNew is the public-facing wrapper around insertNewTx that
// opens its own implicit transaction. Used by Issue; Rotate prefers
// insertNewTx because it already owns a transaction.
func (s *RefreshChainService) insertNew(
	ctx context.Context,
	chainID, deviceID, clientID string,
	uid int,
	scope string,
	deviceInfo *string,
	parentID *uint,
) (Issued, error) {
	var issued Issued
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		issued, err = s.insertNewTx(tx, chainID, deviceID, clientID, uid, scope, deviceInfo, parentID)
		return err
	})
	return issued, err
}

// insertNewTx persists one new refresh-token row inside an existing
// transaction.
func (s *RefreshChainService) insertNewTx(
	tx *gorm.DB,
	chainID, deviceID, clientID string,
	uid int,
	scope string,
	deviceInfo *string,
	parentID *uint,
) (Issued, error) {
	token, err := generateOpaqueCode(s.random, RefreshTokenBytes)
	if err != nil {
		return Issued{}, fmt.Errorf("refresh_chain: generate token: %w", err)
	}
	now := s.now()
	expires := now.Add(model.RefreshTokenTTL)

	row := model.RefreshToken{
		Token:      token,
		ChainID:    chainID,
		DeviceID:   deviceID,
		ClientID:   clientID,
		UID:        uid,
		Scope:      scope,
		ParentID:   parentID,
		Revoked:    false,
		ExpiresAt:  expires,
		DeviceInfo: deviceInfo,
		CreatedAt:  &now,
	}
	if err := tx.Create(&row).Error; err != nil {
		return Issued{}, fmt.Errorf("refresh_chain: insert: %w", err)
	}
	return Issued{
		Token:     token,
		ExpiresAt: expires,
		ID:        row.ID,
		ChainID:   chainID,
		DeviceID:  deviceID,
		UID:       uid,
		ClientID:  clientID,
		Scope:     scope,
	}, nil
}

// sweepChainAsReplay marks every non-revoked token in the chain as
// revoked=true reason=replay_detected. Already-rotated tokens keep
// their 'rotated' reason — preserving forensic history of the
// rotation chain up to the breach.
func (s *RefreshChainService) sweepChainAsReplay(tx *gorm.DB, chainID string) error {
	return tx.Model(&model.RefreshToken{}).
		Where("chain_id = ? AND revoked = ?", chainID, false).
		Updates(map[string]any{
			"revoked":        true,
			"revoked_reason": model.RevokedReasonReplayDetected,
		}).Error
}
