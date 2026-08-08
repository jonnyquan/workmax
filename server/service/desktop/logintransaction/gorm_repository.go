package logintransaction

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"server/service/secrets"
)

// GORMRepository is the shared, multi-instance repository for Desktop login
// transactions. Bearer capabilities are stored only as SHA-256 digests. The
// OAuth state and Google PKCE verifier must be recoverable after a Server
// restart, so they are encrypted with the platform secrets key before storage.
type GORMRepository struct {
	db *gorm.DB
}

func NewGORMRepository(db *gorm.DB) (*GORMRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("desktop login transaction repository: database is required")
	}
	return &GORMRepository{db: db}, nil
}

type loginTransactionRow struct {
	ID            uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	TransactionID string `gorm:"column:transaction_id;type:varchar(64);not null;uniqueIndex:uk_w_desktop_login_tx_transaction"`
	Version       uint64 `gorm:"column:version;not null"`
	Status        string `gorm:"column:status;type:varchar(32);not null;index:idx_w_desktop_login_tx_status_expiry,priority:1"`

	SecretHash []byte `gorm:"column:secret_hash;type:binary(32);not null;uniqueIndex:uk_w_desktop_login_tx_secret"`

	ClientID             string `gorm:"column:client_id;type:varchar(64);not null"`
	DeviceID             string `gorm:"column:device_id;type:varchar(64);not null"`
	RedirectURI          string `gorm:"column:redirect_uri;type:varchar(500);not null"`
	OAuthStateHash       []byte `gorm:"column:oauth_state_digest;type:binary(32);not null"`
	OAuthStateCiphertext string `gorm:"column:oauth_state_ciphertext;type:varchar(1536);not null"`
	CodeChallenge        string `gorm:"column:code_challenge;type:varchar(128);not null"`
	CodeChallengeMethod  string `gorm:"column:code_challenge_method;type:varchar(10);not null"`
	Scope                string `gorm:"column:scope;type:varchar(255);not null"`

	GoogleStateHash          []byte  `gorm:"column:provider_state_digest;type:binary(32);uniqueIndex:uk_w_desktop_login_tx_provider_state"`
	GoogleVerifierCiphertext *string `gorm:"column:provider_pkce_ciphertext;type:varchar(1024)"`
	ExchangeTokenHash        []byte  `gorm:"column:exchange_token_digest;type:binary(32)"`
	UserID                   *uint   `gorm:"column:uid"`
	AuthMethod               *string `gorm:"column:identity_method;type:varchar(20)"`
	PasswordFailures         uint16  `gorm:"column:failed_attempts;not null"`

	CreatedAt           time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;not null"`
	ExpiresAt           time.Time  `gorm:"column:expires_at;not null;index:idx_w_desktop_login_tx_status_expiry,priority:2"`
	AuthenticatedAt     *time.Time `gorm:"column:authenticated_at"`
	ExchangedAt         *time.Time `gorm:"column:consumed_at"`
	LastPasswordFailure *time.Time `gorm:"column:last_failed_at"`
}

func (loginTransactionRow) TableName() string { return "w_desktop_login_transaction" }

type mutableLoginTransactionRow struct {
	Version                  uint64
	Status                   string
	GoogleStateHash          []byte
	GoogleVerifierCiphertext *string
	ExchangeTokenHash        []byte
	UserID                   *uint
	AuthMethod               *string
	PasswordFailures         uint16
	UpdatedAt                time.Time
	AuthenticatedAt          *time.Time
	ExchangedAt              *time.Time
	LastPasswordFailure      *time.Time
}

func (r *GORMRepository) Create(ctx context.Context, record Record) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := validateRecordInvariant(record); err != nil {
		return err
	}
	row, err := encodeLoginTransactionRow(record)
	if err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		var count int64
		lookupErr := r.db.WithContext(ctx).Model(&loginTransactionRow{}).
			Where("transaction_id = ?", record.ID).
			Count(&count).Error
		if lookupErr == nil && count != 0 {
			return ErrRecordExists
		}
		return fmt.Errorf("desktop login transaction repository: create: %w", err)
	}
	return nil
}

func (r *GORMRepository) Get(ctx context.Context, id string) (Record, error) {
	if ctx == nil {
		return Record{}, context.Canceled
	}
	var row loginTransactionRow
	if err := r.db.WithContext(ctx).Where("transaction_id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Record{}, ErrRecordNotFound
		}
		return Record{}, fmt.Errorf("desktop login transaction repository: get: %w", err)
	}
	return decodeLoginTransactionRow(row)
}

func (r *GORMRepository) CompareAndSwap(
	ctx context.Context,
	id string,
	expectedVersion uint64,
	mutate Mutation,
) (Record, error) {
	if ctx == nil {
		return Record{}, context.Canceled
	}
	if mutate == nil {
		return Record{}, ErrInvariantViolation
	}

	var committed Record
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row loginTransactionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("transaction_id = ?", id).
			First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRecordNotFound
			}
			return fmt.Errorf("desktop login transaction repository: select for update: %w", err)
		}
		if row.Version != expectedVersion {
			return ErrVersionConflict
		}

		current, err := decodeLoginTransactionRow(row)
		if err != nil {
			return err
		}
		next := current
		if err := mutate(&next); err != nil {
			return err
		}
		if immutableRecordFieldsChanged(current, next) {
			return ErrInvariantViolation
		}
		next.Version = current.Version + 1
		if err := validateRecordInvariant(next); err != nil {
			return err
		}
		encoded, err := encodeMutableLoginTransactionRow(next)
		if err != nil {
			return err
		}

		result := tx.Model(&loginTransactionRow{}).
			Where("transaction_id = ? AND version = ?", id, expectedVersion).
			Updates(map[string]any{
				"version":                  encoded.Version,
				"status":                   encoded.Status,
				"provider_state_digest":    nullableDigest(encoded.GoogleStateHash),
				"provider_pkce_ciphertext": encoded.GoogleVerifierCiphertext,
				"exchange_token_digest":    nullableDigest(encoded.ExchangeTokenHash),
				"uid":                      encoded.UserID,
				"identity_method":          encoded.AuthMethod,
				"failed_attempts":          encoded.PasswordFailures,
				"updated_at":               encoded.UpdatedAt,
				"authenticated_at":         encoded.AuthenticatedAt,
				"consumed_at":              encoded.ExchangedAt,
				"last_failed_at":           encoded.LastPasswordFailure,
			})
		if result.Error != nil {
			return fmt.Errorf("desktop login transaction repository: compare and swap: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrVersionConflict
		}
		committed = next
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return committed, nil
}

func encodeLoginTransactionRow(record Record) (loginTransactionRow, error) {
	oauthStateCiphertext, err := secrets.Encrypt([]byte(record.Request.OAuthState))
	if err != nil {
		return loginTransactionRow{}, fmt.Errorf("desktop login transaction repository: seal OAuth state: %w", err)
	}
	mutable, err := encodeMutableLoginTransactionRow(record)
	if err != nil {
		return loginTransactionRow{}, err
	}
	return loginTransactionRow{
		TransactionID:            record.ID,
		Version:                  mutable.Version,
		Status:                   mutable.Status,
		SecretHash:               digestBytes(record.SecretHash),
		ClientID:                 record.Request.ClientID,
		DeviceID:                 record.Request.DeviceID,
		RedirectURI:              record.Request.RedirectURI,
		OAuthStateHash:           digestBytes(hashSecret(record.Request.OAuthState)),
		OAuthStateCiphertext:     oauthStateCiphertext,
		CodeChallenge:            record.Request.CodeChallenge,
		CodeChallengeMethod:      record.Request.CodeChallengeMethod,
		Scope:                    record.Request.Scope,
		GoogleStateHash:          mutable.GoogleStateHash,
		GoogleVerifierCiphertext: mutable.GoogleVerifierCiphertext,
		ExchangeTokenHash:        mutable.ExchangeTokenHash,
		UserID:                   mutable.UserID,
		AuthMethod:               mutable.AuthMethod,
		PasswordFailures:         mutable.PasswordFailures,
		CreatedAt:                record.CreatedAt,
		UpdatedAt:                mutable.UpdatedAt,
		ExpiresAt:                record.ExpiresAt,
		AuthenticatedAt:          mutable.AuthenticatedAt,
		ExchangedAt:              mutable.ExchangedAt,
		LastPasswordFailure:      mutable.LastPasswordFailure,
	}, nil
}

// encodeMutableLoginTransactionRow deliberately excludes the frozen OAuth
// request. CAS updates must not reseal immutable OAuth state and introduce an
// otherwise unrelated RNG/encryption failure into every state transition.
func encodeMutableLoginTransactionRow(record Record) (mutableLoginTransactionRow, error) {
	var googleVerifierCiphertext *string
	if record.GoogleCodeVerifier != "" {
		sealed, err := secrets.Encrypt([]byte(record.GoogleCodeVerifier))
		if err != nil {
			return mutableLoginTransactionRow{}, fmt.Errorf("desktop login transaction repository: seal provider verifier: %w", err)
		}
		googleVerifierCiphertext = &sealed
	}

	var userID *uint
	if record.UserID != 0 {
		value := record.UserID
		userID = &value
	}
	var authMethod *string
	if record.AuthMethod != "" {
		value := string(record.AuthMethod)
		authMethod = &value
	}
	return mutableLoginTransactionRow{
		Version:                  record.Version,
		Status:                   string(record.State),
		GoogleStateHash:          nullableDigest(digestBytes(record.GoogleStateHash)),
		GoogleVerifierCiphertext: googleVerifierCiphertext,
		ExchangeTokenHash:        nullableDigest(digestBytes(record.ExchangeTokenHash)),
		UserID:                   userID,
		AuthMethod:               authMethod,
		PasswordFailures:         record.PasswordFailures,
		UpdatedAt:                record.UpdatedAt,
		AuthenticatedAt:          timePointer(record.AuthenticatedAt),
		ExchangedAt:              timePointer(record.ExchangedAt),
		LastPasswordFailure:      timePointer(record.LastPasswordFailure),
	}, nil
}

func decodeLoginTransactionRow(row loginTransactionRow) (Record, error) {
	secretHash, err := digestArray(row.SecretHash, false)
	if err != nil {
		return Record{}, fmt.Errorf("desktop login transaction repository: secret digest: %w", err)
	}
	googleStateHash, err := digestArray(row.GoogleStateHash, true)
	if err != nil {
		return Record{}, fmt.Errorf("desktop login transaction repository: provider state digest: %w", err)
	}
	exchangeTokenHash, err := digestArray(row.ExchangeTokenHash, true)
	if err != nil {
		return Record{}, fmt.Errorf("desktop login transaction repository: exchange digest: %w", err)
	}
	oauthStateHash, err := digestArray(row.OAuthStateHash, false)
	if err != nil {
		return Record{}, fmt.Errorf("desktop login transaction repository: OAuth state digest: %w", err)
	}

	oauthState, err := secrets.Decrypt(row.OAuthStateCiphertext)
	if err != nil {
		return Record{}, fmt.Errorf("desktop login transaction repository: unseal OAuth state: %w", err)
	}
	actualStateHash := hashSecret(string(oauthState))
	if subtle.ConstantTimeCompare(oauthStateHash[:], actualStateHash[:]) != 1 {
		return Record{}, ErrInvariantViolation
	}
	googleVerifier := ""
	if row.GoogleVerifierCiphertext != nil {
		plaintext, err := secrets.Decrypt(*row.GoogleVerifierCiphertext)
		if err != nil {
			return Record{}, fmt.Errorf("desktop login transaction repository: unseal provider verifier: %w", err)
		}
		googleVerifier = string(plaintext)
	}

	var userID uint
	if row.UserID != nil {
		userID = *row.UserID
	}
	var method AuthMethod
	if row.AuthMethod != nil {
		method = AuthMethod(*row.AuthMethod)
	}
	record := Record{
		ID:      row.TransactionID,
		Version: row.Version,
		State:   State(row.Status),
		Request: CreateInput{
			ClientID:            row.ClientID,
			RedirectURI:         row.RedirectURI,
			OAuthState:          string(oauthState),
			CodeChallenge:       row.CodeChallenge,
			CodeChallengeMethod: row.CodeChallengeMethod,
			Scope:               row.Scope,
			DeviceID:            row.DeviceID,
		},
		SecretHash:          secretHash,
		GoogleStateHash:     googleStateHash,
		GoogleCodeVerifier:  googleVerifier,
		ExchangeTokenHash:   exchangeTokenHash,
		UserID:              userID,
		AuthMethod:          method,
		PasswordFailures:    row.PasswordFailures,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		ExpiresAt:           row.ExpiresAt,
		AuthenticatedAt:     timeValue(row.AuthenticatedAt),
		ExchangedAt:         timeValue(row.ExchangedAt),
		LastPasswordFailure: timeValue(row.LastPasswordFailure),
	}
	if err := validateRecordInvariant(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateRecordInvariant(record Record) error {
	zeroDigest := [32]byte{}
	if record.Version == 0 || record.PasswordFailures > MaxPasswordFailures ||
		(record.PasswordFailures == 0) != record.LastPasswordFailure.IsZero() {
		return ErrInvariantViolation
	}
	if (record.UserID == 0) != (record.AuthMethod == "") {
		return ErrInvariantViolation
	}
	if record.AuthMethod != "" && record.AuthMethod != AuthMethodPassword && record.AuthMethod != AuthMethodGoogle {
		return ErrInvariantViolation
	}

	switch record.State {
	case StatePending, StatePasswordAuthenticating:
		if record.UserID != 0 || record.GoogleStateHash != zeroDigest ||
			record.GoogleCodeVerifier != "" || record.ExchangeTokenHash != zeroDigest {
			return ErrInvariantViolation
		}
	case StateGooglePending:
		if record.UserID != 0 || record.GoogleStateHash == zeroDigest ||
			record.GoogleCodeVerifier == "" || record.ExchangeTokenHash != zeroDigest {
			return ErrInvariantViolation
		}
	case StateGoogleExchanging:
		if record.UserID != 0 || record.GoogleStateHash != zeroDigest ||
			record.GoogleCodeVerifier == "" || record.ExchangeTokenHash != zeroDigest {
			return ErrInvariantViolation
		}
	case StateAuthenticated:
		if record.UserID == 0 || record.ExchangeTokenHash == zeroDigest || record.AuthenticatedAt.IsZero() ||
			record.GoogleStateHash != zeroDigest || record.GoogleCodeVerifier != "" {
			return ErrInvariantViolation
		}
	case StateExchanged:
		if record.UserID == 0 || record.ExchangeTokenHash != zeroDigest ||
			record.AuthenticatedAt.IsZero() || record.ExchangedAt.IsZero() ||
			record.GoogleStateHash != zeroDigest || record.GoogleCodeVerifier != "" {
			return ErrInvariantViolation
		}
	case StateFailed:
		if record.UserID != 0 || record.ExchangeTokenHash != zeroDigest ||
			record.GoogleStateHash != zeroDigest || record.GoogleCodeVerifier != "" {
			return ErrInvariantViolation
		}
	case StateExpired:
		if record.ExchangeTokenHash != zeroDigest || record.GoogleStateHash != zeroDigest || record.GoogleCodeVerifier != "" {
			return ErrInvariantViolation
		}
	default:
		return ErrInvariantViolation
	}
	return nil
}

func digestBytes(value [32]byte) []byte {
	return append([]byte(nil), value[:]...)
}

func digestArray(value []byte, nullable bool) ([32]byte, error) {
	var out [32]byte
	if len(value) == 0 && nullable {
		return out, nil
	}
	if len(value) != len(out) {
		return out, fmt.Errorf("got %d bytes, want %d", len(value), len(out))
	}
	copy(out[:], value)
	return out, nil
}

func nullableDigest(value []byte) []byte {
	for _, b := range value {
		if b != 0 {
			return value
		}
	}
	return nil
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

var _ Repository = (*GORMRepository)(nil)
