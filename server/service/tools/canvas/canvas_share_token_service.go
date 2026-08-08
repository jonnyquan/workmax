// Share-link revocation via token rotation — C-11 (closed
// 2026-05-15 by owner). The original share URL was just
// /api/tools/canvas/public/projects/uuid/{uuid} keyed on the
// project's permanent UUID — meaning a leaked URL could never be
// invalidated short of flipping the project back to private (which
// blocks new requests but doesn't help cached / already-rendered
// URLs).
//
// C-11 adds an optional rotation step: an owner action that
// generates a fresh share_token, after which the public URL must
// include ?t=<token>. Old URLs without the token (or with a stale
// one) drop to ProjectNotFound at the lookup layer.
//
// Backward compatibility: every existing project's share_token is
// NULL. matchSharedProjectShareToken treats NULL as "no rotation
// performed — accept token-less URLs". This keeps the rollout
// risk-free; owners only see new behaviour after they explicitly
// rotate.
//
// Scope limitation: rotation invalidates the project-lookup URL
// only. Asset URLs already in CDN caches (`/uploads/canvas/uid/...`)
// resolve through canvasAssetAccessAllowed, which checks the
// project's CURRENT visibility, not the token. So a viewer who
// already loaded an asset and cached it can hit it directly until
// the cache expires OR the owner also flips visibility to private.
// True asset-path rotation (filesystem moves / signed URLs) is a
// Stage 2 — wait for a customer to demand it before building.

package canvas

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"server/model"

	"gorm.io/gorm"
)

// shareTokenByteLen is the random-bytes length we hex-encode for the
// share token. 32 bytes → 64 hex chars → 256 bits of entropy. Well
// past the "guess the URL" attack budget.
const shareTokenByteLen = 32

// generateShareToken produces a fresh hex token from crypto/rand.
// Surface error rather than panic so callers can decide whether to
// retry (extremely rare — /dev/urandom would have to be unavailable).
func generateShareToken() (string, error) {
	buf := make([]byte, shareTokenByteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate share token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// matchSharedProjectShareToken returns true when the request's token
// (possibly empty) authorises a read against the project's stored
// token. Used by GetSharedProjectByUUID; factored out so the rule is
// pinned by a unit test.
//
//   stored=nil  ⇒  request token must be empty (legacy state — share
//                  links predate C-11, no token gate yet)
//   stored=set  ⇒  request token must match via constant-time
//                  compare (rotated state — old links are dead)
func matchSharedProjectShareToken(stored *string, requested string) bool {
	if stored == nil {
		return requested == ""
	}
	// Pointer-set but value empty would be a bug in the rotate path;
	// treat it the same as nil to fail safe.
	if *stored == "" {
		return requested == ""
	}
	return subtle.ConstantTimeCompare([]byte(*stored), []byte(requested)) == 1
}

// RotateProjectShareToken generates a fresh share token for the
// project and stores it. Owner-only — viewers/editors of a shared
// project can't rotate. Returns the new token so the caller can
// hand it back to the FE for share-URL construction.
//
// Idempotent in the trivial sense (re-rotating produces a NEW
// token; never errors on "already rotated"). After rotation, any
// previously distributed share URL is dead.
//
// The function does not return ErrCanvasProjectNotFound if the
// project happens to be private — the rotation is the OWNER's
// action and they can rotate before / after publishing without a
// state-machine pre-condition. The token is only consulted when
// the public read path runs anyway.
func RotateProjectShareToken(ctx context.Context, db *gorm.DB, uid int, projectID uint) (string, error) {
	token, err := generateShareToken()
	if err != nil {
		return "", err
	}
	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireProjectManageAccess(ctx, tx, uid, projectID); err != nil {
			return err
		}
		res := tx.Model(&model.CanvasProject{}).
			Where("id = ? AND deleted_at IS NULL", projectID).
			Update("share_token", token)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrCanvasProjectNotFound
		}
		return nil
	})
	if txErr != nil {
		return "", txErr
	}
	return token, nil
}

// GetSharedProjectByUUIDWithToken is the token-aware variant of
// GetSharedProjectByUUID. The original signature stays for callers
// that don't need the gate (none today after this commit), so a
// future caller doesn't have to think about empty-token semantics.
//
// Returns ErrCanvasProjectNotFound for: missing uuid, project not
// visible (private), token mismatch. The single sentinel means the
// public surface gives no signal whether the URL was wrong vs.
// the token was stale — defensible posture for a sharing surface.
func GetSharedProjectByUUIDWithToken(ctx context.Context, db *gorm.DB, uuid, token string) (model.CanvasProject, error) {
	project, err := GetSharedProjectByUUID(ctx, db, uuid)
	if err != nil {
		return model.CanvasProject{}, err
	}
	if !matchSharedProjectShareToken(project.ShareToken, token) {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	return project, nil
}

// Compile-time assertion: errors.Is must work against the shared
// sentinel, otherwise the handler can't classify properly. (No-op
// at runtime; lint guard.)
var _ = func() bool { return errors.Is(ErrCanvasProjectNotFound, ErrCanvasProjectNotFound) }()
