package identity

import (
	"context"
	"crypto/md5" // #nosec G501 -- read-only compatibility for legacy stored hashes; successful login upgrades to bcrypt.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"server/model"
	logintransaction "server/service/desktop/logintransaction"
)

const (
	maxLoginEmailBytes    = 320
	maxLoginPasswordBytes = 1024
	prehashedBcryptPrefix = "$workmax-bcrypt-sha256$"
)

// PasswordAuthenticator is the account-identity adapter used by the Desktop
// Login Transaction. It deliberately does not mint a generic JWT, set a
// browser cookie, or depend on a Gin handler.
//
// Existing accounts may still contain the historical unsalted MD5 digest.
// A successful comparison upgrades that exact row to bcrypt with a CAS update.
// New code must never create another legacy digest.
type PasswordAuthenticator struct {
	db        *gorm.DB
	dummyHash []byte
}

// NewPasswordAuthenticator constructs a production adapter. Preparing one
// dummy bcrypt digest up front lets an unknown-account request take the same
// expensive verification path as a wrong-password request.
func NewPasswordAuthenticator(db *gorm.DB) (*PasswordAuthenticator, error) {
	if db == nil {
		return nil, fmt.Errorf("identity password authenticator: database is required")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("workmax-desktop-login-dummy-credential"), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("identity password authenticator: initialize comparison: %w", err)
	}
	return &PasswordAuthenticator{db: db, dummyHash: dummy}, nil
}

// AuthenticatePassword implements logintransaction.PasswordAuthenticator.
// Missing users, disabled users, malformed credentials, and password mismatch
// all collapse to the same public sentinel to avoid account enumeration.
func (a *PasswordAuthenticator) AuthenticatePassword(
	ctx context.Context,
	email string,
	password string,
) (logintransaction.Principal, error) {
	if ctx == nil {
		return logintransaction.Principal{}, logintransaction.ErrAuthenticationFailed
	}
	if err := ctx.Err(); err != nil {
		return logintransaction.Principal{}, err
	}
	if !validCredentialInput(email, password) {
		a.burnComparison(password)
		return logintransaction.Principal{}, logintransaction.ErrAuthenticationFailed
	}

	var user model.User
	err := a.db.WithContext(ctx).
		Select("id", "password", "ban").
		Where("email = ?", email).
		First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			a.burnComparison(password)
			return logintransaction.Principal{}, logintransaction.ErrAuthenticationFailed
		}
		return logintransaction.Principal{}, fmt.Errorf("identity password authenticator: load account: %w", err)
	}

	// Historical MD5 rows and malformed stored values would otherwise return
	// much faster than either an unknown account or a normal bcrypt mismatch.
	// Always pay one bcrypt comparison for those exceptional representations.
	needsDummyComparison := isLegacyMD5Digest(user.Password)
	if !needsDummyComparison {
		_, costErr := storedBcryptCost(user.Password)
		// bcrypt rejects inputs above 72 bytes before doing the expensive
		// comparison. Untagged historical bcrypt rows can never authenticate
		// such a password, but still need one dummy comparison so that this
		// fast rejection does not become an account-enumeration signal.
		needsDummyComparison = costErr != nil ||
			(len(password) > 72 && !strings.HasPrefix(user.Password, prehashedBcryptPrefix))
	}
	valid, legacy := verifyStoredPassword(password, user.Password)
	if needsDummyComparison {
		a.burnComparison(password)
	}
	if !valid || user.Ban || user.Id == 0 {
		return logintransaction.Principal{}, logintransaction.ErrAuthenticationFailed
	}
	if legacy {
		if err := a.upgradeLegacyHash(ctx, user.Id, user.Password, password); err != nil {
			return logintransaction.Principal{}, err
		}
	}

	return logintransaction.Principal{UserID: user.Id}, nil
}

func validCredentialInput(email, password string) bool {
	return email != "" && password != "" &&
		email == strings.TrimSpace(email) &&
		len(email) <= maxLoginEmailBytes && len(password) <= maxLoginPasswordBytes &&
		utf8.ValidString(email) && utf8.ValidString(password) &&
		!containsCredentialControl(email)
}

func containsCredentialControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func verifyStoredPassword(password, stored string) (valid bool, legacy bool) {
	if isLegacyMD5Digest(stored) {
		digest := md5.Sum([]byte(password)) // #nosec G401 -- compatibility comparison only; immediately upgraded after success.
		presented := hex.EncodeToString(digest[:])
		return subtle.ConstantTimeCompare([]byte(strings.ToLower(stored)), []byte(presented)) == 1, true
	}
	if strings.HasPrefix(stored, prehashedBcryptPrefix) {
		digest := sha256.Sum256([]byte(password))
		return bcrypt.CompareHashAndPassword(
			[]byte(strings.TrimPrefix(stored, prehashedBcryptPrefix)),
			digest[:],
		) == nil, false
	}
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil, false
}

func storedBcryptCost(stored string) (int, error) {
	return bcrypt.Cost([]byte(strings.TrimPrefix(stored, prehashedBcryptPrefix)))
}

func isLegacyMD5Digest(value string) bool {
	if len(value) != md5.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (a *PasswordAuthenticator) upgradeLegacyHash(
	ctx context.Context,
	userID uint,
	legacyHash string,
	password string,
) error {
	upgraded, err := generateUpgradedPasswordHash(password)
	if err != nil {
		return fmt.Errorf("identity password authenticator: upgrade password hash: %w", err)
	}
	result := a.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ? AND password = ?", userID, legacyHash).
		Update("password", upgraded)
	if result.Error != nil {
		return fmt.Errorf("identity password authenticator: persist password upgrade: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}

	// A concurrent password change won the CAS. Re-read and only admit this
	// attempt if the presented password still verifies against the winner.
	var current model.User
	if err := a.db.WithContext(ctx).
		Select("id", "password", "ban").
		Where("id = ?", userID).
		First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return logintransaction.ErrAuthenticationFailed
		}
		return fmt.Errorf("identity password authenticator: reload concurrent password update: %w", err)
	}
	valid, _ := verifyStoredPassword(password, current.Password)
	if !valid || current.Ban {
		return logintransaction.ErrAuthenticationFailed
	}
	return nil
}

func generateUpgradedPasswordHash(password string) (string, error) {
	input := []byte(password)
	prefix := ""
	if len(input) > 72 {
		digest := sha256.Sum256(input)
		input = digest[:]
		prefix = prehashedBcryptPrefix
	}
	hash, err := bcrypt.GenerateFromPassword(input, bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return prefix + string(hash), nil
}

func (a *PasswordAuthenticator) burnComparison(password string) {
	if a == nil || len(a.dummyHash) == 0 {
		return
	}
	_ = bcrypt.CompareHashAndPassword(a.dummyHash, dummyComparisonInput(password))
}

func dummyComparisonInput(password string) []byte {
	input := []byte(password)
	if len(input) <= 72 {
		return input
	}
	digest := sha256.Sum256(input)
	return digest[:]
}

var _ logintransaction.PasswordAuthenticator = (*PasswordAuthenticator)(nil)
