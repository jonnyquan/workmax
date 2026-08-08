package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	model "server/model/desktop/oauth"
)

// AuthCodeBytes is the byte length of the random token before
// base64url encoding. 32 bytes = 256 bits of entropy = 43 chars
// base64-encoded, comfortably above the OAuth spec's "long enough
// not to be guessable" requirement.
const AuthCodeBytes = 32

// Authorization-code service errors.
var (
	ErrCodeNotFound    = errors.New("auth_code: not found")
	ErrCodeAlreadyUsed = errors.New("auth_code: code has already been consumed")
	ErrCodeExpired     = errors.New("auth_code: code has expired")
	ErrCodeBinding     = errors.New("auth_code: request binding does not match")
	ErrCodePKCE        = errors.New("auth_code: PKCE verification failed")
)

// CodeService issues and consumes single-use OAuth authorization
// codes. Each code is a row in oauth_authorization_code; consuming
// is a transaction that flips `used=true` so a parallel attempt
// loses (the SELECT inside the txn re-checks the flag).
//
// The `now` and `random` fields are pulled out as injection seams so
// tests can run with fixed timestamps and deterministic byte streams
// without having to monkey-patch package globals.
type CodeService struct {
	db     *gorm.DB
	now    func() time.Time
	random io.Reader
}

// NewCodeService returns a service backed by the given DB. Uses
// time.Now (UTC) and crypto/rand.Reader in production.
func NewCodeService(db *gorm.DB) *CodeService {
	return &CodeService{
		db:     db,
		now:    func() time.Time { return time.Now().UTC() },
		random: rand.Reader,
	}
}

// GenerateInput is everything the /authorize handler captured from
// the request that the /token handler will need to re-verify on
// code consumption. Stored verbatim in the row.
type GenerateInput struct {
	ClientID            string
	UID                 int
	DeviceID            *string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
}

// Generated bundles what the /authorize handler returns to the caller
// (the code itself + its expiry, so the caller can include max-age
// hints in any cache headers, etc.).
type Generated struct {
	Code      string
	ExpiresAt time.Time
}

// Generate creates a new code row and returns the random opaque
// string the /authorize endpoint should redirect with.
//
// Idempotency: codes are UNIQUE in DB. The chance of two codes
// colliding within their 10-min lifetime is astronomically low
// (256-bit space, base64url encoding); we don't retry on collision —
// if it ever happens we'd rather surface the DB error than mask it.
func (s *CodeService) Generate(ctx context.Context, in GenerateInput) (Generated, error) {
	code, err := generateOpaqueCode(s.random, AuthCodeBytes)
	if err != nil {
		return Generated{}, fmt.Errorf("auth_code: generate random: %w", err)
	}
	now := s.now()
	expires := now.Add(model.AuthorizationCodeTTL)

	row := model.AuthorizationCode{
		Code:                code,
		ClientID:            in.ClientID,
		UID:                 in.UID,
		DeviceID:            in.DeviceID,
		RedirectURI:         in.RedirectURI,
		CodeChallenge:       in.CodeChallenge,
		CodeChallengeMethod: in.CodeChallengeMethod,
		Scope:               in.Scope,
		Used:                false,
		ExpiresAt:           expires,
		CreatedAt:           &now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return Generated{}, fmt.Errorf("auth_code: insert: %w", err)
	}
	return Generated{Code: code, ExpiresAt: expires}, nil
}

// ConsumedCode is everything the /token handler needs from the
// just-consumed row to (a) re-validate redirect_uri + client_id from
// the token request match the auth request, and (b) re-verify PKCE.
type ConsumedCode struct {
	ClientID            string
	UID                 int
	DeviceID            *string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
}

// Consume atomically marks a code as used and returns the metadata
// the /token handler needs. Re-consume attempts return
// ErrCodeAlreadyUsed; lookup misses return ErrCodeNotFound;
// expired codes return ErrCodeExpired.
//
// All three negative cases map to OAuth's "invalid_grant" error
// at the wire level — the /token handler is responsible for the
// conversion. We keep the distinct sentinels here for log clarity
// (a flood of ErrCodeAlreadyUsed is a different operational signal
// than a flood of ErrCodeNotFound).
//
// The transaction uses SELECT FOR UPDATE plus an UPDATE guarded by used=false:
// the lock serializes MySQL/InnoDB consumers while the CAS remains the final
// correctness boundary for dialects that ignore the locking clause.
func (s *CodeService) Consume(ctx context.Context, code string) (*ConsumedCode, error) {
	return s.consume(ctx, code, nil)
}

// ConsumeValidated verifies every caller-controlled authorization-code
// binding before atomically flipping the row to used. A mismatched client,
// redirect URI, or PKCE verifier must not burn a legitimate code: otherwise
// possession of a leaked code value alone becomes a denial-of-service vector.
//
// The SELECT uses FOR UPDATE on databases that support it, and the final
// UPDATE also carries a used=false CAS guard. The latter is the portable
// correctness boundary used by SQLite tests and protects against a future
// dialect/configuration change silently dropping the lock clause.
type ConsumeValidatedInput struct {
	Code         string
	ClientID     string
	RedirectURI  string
	CodeVerifier string
	DeviceID     string
}

func (s *CodeService) ConsumeValidated(
	ctx context.Context,
	in ConsumeValidatedInput,
) (*ConsumedCode, error) {
	return s.consume(ctx, in.Code, func(row *model.AuthorizationCode) error {
		if row.ClientID != in.ClientID || row.RedirectURI != in.RedirectURI {
			return ErrCodeBinding
		}
		if row.DeviceID != nil && *row.DeviceID != in.DeviceID {
			return ErrCodeBinding
		}
		if err := VerifyPKCE(in.CodeVerifier, row.CodeChallenge, row.CodeChallengeMethod); err != nil {
			return ErrCodePKCE
		}
		return nil
	})
}

func (s *CodeService) consume(
	ctx context.Context,
	code string,
	validate func(*model.AuthorizationCode) error,
) (*ConsumedCode, error) {
	var out *ConsumedCode
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.AuthorizationCode
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("code = ?", code).
			First(&row).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCodeNotFound
			}
			return fmt.Errorf("auth_code: select: %w", err)
		}
		if row.Used {
			return ErrCodeAlreadyUsed
		}
		if !s.now().Before(row.ExpiresAt) {
			return ErrCodeExpired
		}
		if validate != nil {
			if err := validate(&row); err != nil {
				return err
			}
		}
		result := tx.Model(&model.AuthorizationCode{}).
			Where("id = ? AND used = ?", row.ID, false).
			UpdateColumn("used", true)
		if result.Error != nil {
			return fmt.Errorf("auth_code: update used: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrCodeAlreadyUsed
		}
		out = &ConsumedCode{
			ClientID:            row.ClientID,
			UID:                 row.UID,
			DeviceID:            row.DeviceID,
			RedirectURI:         row.RedirectURI,
			CodeChallenge:       row.CodeChallenge,
			CodeChallengeMethod: row.CodeChallengeMethod,
			Scope:               row.Scope,
		}
		return nil
	})
	return out, err
}

// generateOpaqueCode reads `n` bytes from the given source and
// returns the base64url (no padding) encoding. Errors propagate
// from the source read.
func generateOpaqueCode(r io.Reader, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
