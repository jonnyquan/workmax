package logintransaction

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	transactionIDBytes     = 16
	transactionSecretBytes = 32
	googleStateBytes       = 32
	googleVerifierBytes    = 32
	exchangeTokenBytes     = 32
	createCollisionRetries = 4
	claimCleanupTimeout    = 2 * time.Second
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// Service coordinates the Desktop login transaction state machine.
type Service struct {
	repo     Repository
	password PasswordAuthenticator
	google   GoogleAuthenticator
	ttl      time.Duration
	clock    Clock
	random   io.Reader

	// io.Reader does not promise concurrent safety. Serializing entropy reads
	// also makes deterministic test readers reliable under race tests.
	randomMu sync.Mutex
}

func NewService(
	repo Repository,
	password PasswordAuthenticator,
	google GoogleAuthenticator,
	opts Options,
) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("%w: repository is required", ErrInvalidInput)
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < 0 {
		return nil, fmt.Errorf("%w: TTL must be positive", ErrInvalidInput)
	}
	clock := opts.Clock
	if clock == nil {
		clock = systemClock{}
	}
	random := opts.Random
	if random == nil {
		random = rand.Reader
	}
	return &Service{
		repo:     repo,
		password: password,
		google:   google,
		ttl:      ttl,
		clock:    clock,
		random:   random,
	}, nil
}

// Create freezes one validated authorization request and returns the bearer
// capability required to continue it. The raw secret is never persisted.
func (s *Service) Create(ctx context.Context, in CreateInput) (TransactionHandle, error) {
	if err := validateCreateInput(in); err != nil {
		return TransactionHandle{}, err
	}
	if err := ctx.Err(); err != nil {
		return TransactionHandle{}, err
	}

	for attempt := 0; attempt < createCollisionRetries; attempt++ {
		id, err := s.randomToken(transactionIDBytes)
		if err != nil {
			return TransactionHandle{}, err
		}
		secret, err := s.randomToken(transactionSecretBytes)
		if err != nil {
			return TransactionHandle{}, err
		}
		now := s.now()
		record := Record{
			ID:         id,
			Version:    1,
			State:      StatePending,
			Request:    in,
			SecretHash: hashSecret(secret),
			CreatedAt:  now,
			UpdatedAt:  now,
			ExpiresAt:  now.Add(s.ttl),
		}
		if err := s.repo.Create(ctx, record); err != nil {
			if errors.Is(err, ErrRecordExists) {
				continue
			}
			return TransactionHandle{}, err
		}
		return TransactionHandle{
			TransactionID:     id,
			TransactionSecret: secret,
			ExpiresAt:         record.ExpiresAt,
		}, nil
	}
	return TransactionHandle{}, fmt.Errorf("%w: transaction id collision budget exhausted", ErrEntropyUnavailable)
}

// CompletePassword claims a pending transaction, delegates credential
// verification to the injected identity boundary, and returns a fresh
// one-time exchange capability on success.
func (s *Service) CompletePassword(
	ctx context.Context,
	in PasswordCompletionInput,
) (Completion, error) {
	if s.password == nil {
		return Completion{}, ErrAuthenticatorUnavailable
	}
	if err := validatePasswordCompletionInput(in); err != nil {
		return Completion{}, err
	}

	record, err := s.loadWithTransactionSecret(ctx, in.TransactionID, in.TransactionSecret)
	if err != nil {
		return Completion{}, err
	}
	claimed, err := s.repo.CompareAndSwap(ctx, record.ID, record.Version, func(next *Record) error {
		if s.expireDuringMutation(next) {
			return nil
		}
		if next.State != StatePending {
			return passwordStateError(next.State)
		}
		next.State = StatePasswordAuthenticating
		next.UpdatedAt = s.now()
		return nil
	})
	if err != nil {
		return Completion{}, mapRepositoryError(err)
	}
	if claimed.State == StateExpired {
		return Completion{}, ErrExpired
	}

	principal, authErr := s.password.AuthenticatePassword(ctx, in.Email, in.Password)
	if err := ctx.Err(); err != nil {
		_ = s.releasePasswordClaim(ctx, claimed, false)
		return Completion{}, err
	}
	if authErr != nil {
		rejected := errors.Is(authErr, ErrAuthenticationFailed)
		if err := s.releasePasswordClaim(ctx, claimed, rejected); err != nil {
			return Completion{}, err
		}
		if rejected {
			return Completion{}, ErrAuthenticationFailed
		}
		return Completion{}, fmt.Errorf("desktop login transaction: password authenticator: %w", authErr)
	}
	if principal.UserID == 0 {
		if err := s.releasePasswordClaim(ctx, claimed, true); err != nil {
			return Completion{}, err
		}
		return Completion{}, ErrAuthenticationFailed
	}

	completion, err := s.completeAuthentication(ctx, claimed, principal, AuthMethodPassword)
	if err != nil {
		// A cancelled request or transient repository failure must not strand a
		// retryable password transaction in its in-flight claim. If the
		// authentication transition did commit, the version check makes this a
		// harmless no-op.
		_ = s.releasePasswordClaim(ctx, claimed, false)
		return Completion{}, err
	}
	return completion, nil
}

// StartGoogle claims a pending transaction and creates independent provider
// state plus a provider-facing PKCE pair. Only the state hash is persisted;
// the verifier stays in the transaction record until CompleteGoogle.
func (s *Service) StartGoogle(ctx context.Context, in GoogleStartInput) (GoogleStart, error) {
	if s.google == nil {
		return GoogleStart{}, ErrAuthenticatorUnavailable
	}
	if err := validateGoogleStartInput(in); err != nil {
		return GoogleStart{}, err
	}
	record, err := s.loadWithTransactionSecret(ctx, in.TransactionID, in.TransactionSecret)
	if err != nil {
		return GoogleStart{}, err
	}

	stateNonce, err := s.randomToken(googleStateBytes)
	if err != nil {
		return GoogleStart{}, err
	}
	providerState := record.ID + "." + stateNonce
	verifier, err := s.randomToken(googleVerifierBytes)
	if err != nil {
		return GoogleStart{}, err
	}
	challenge := pkceChallenge(verifier)

	updated, err := s.repo.CompareAndSwap(ctx, record.ID, record.Version, func(next *Record) error {
		if s.expireDuringMutation(next) {
			return nil
		}
		if next.State != StatePending {
			return googleStartStateError(next.State)
		}
		next.State = StateGooglePending
		next.GoogleStateHash = hashSecret(providerState)
		next.GoogleCodeVerifier = verifier
		next.UpdatedAt = s.now()
		return nil
	})
	if err != nil {
		return GoogleStart{}, mapRepositoryError(err)
	}
	if updated.State == StateExpired {
		return GoogleStart{}, ErrExpired
	}
	return GoogleStart{
		TransactionID:       updated.ID,
		ProviderState:       providerState,
		CodeChallenge:       challenge,
		CodeChallengeMethod: GooglePKCEMethod,
		ExpiresAt:           updated.ExpiresAt,
	}, nil
}

// CompleteGoogle consumes the provider state before performing the provider
// code exchange, so concurrent or replayed callbacks cannot run the identity
// adapter twice. Provider failures are terminal for that transaction because
// the authorization code may already have been consumed.
func (s *Service) CompleteGoogle(
	ctx context.Context,
	in GoogleCompletionInput,
) (Completion, error) {
	if s.google == nil {
		return Completion{}, ErrAuthenticatorUnavailable
	}
	transactionID, err := validateGoogleCompletionInput(in)
	if err != nil {
		return Completion{}, err
	}
	record, err := s.loadRecord(ctx, transactionID)
	if err != nil {
		return Completion{}, err
	}
	record, err = s.ensureFresh(ctx, record)
	if err != nil {
		return Completion{}, err
	}
	if record.State != StateGooglePending {
		return Completion{}, googleCompletionStateError(record.State)
	}
	if !secretMatches(record.GoogleStateHash, in.ProviderState) {
		return Completion{}, ErrInvalidTransaction
	}

	claimed, err := s.repo.CompareAndSwap(ctx, record.ID, record.Version, func(next *Record) error {
		if s.expireDuringMutation(next) {
			return nil
		}
		if next.State != StateGooglePending {
			return googleCompletionStateError(next.State)
		}
		if !secretMatches(next.GoogleStateHash, in.ProviderState) {
			return ErrInvalidTransaction
		}
		next.State = StateGoogleExchanging
		next.GoogleStateHash = [32]byte{}
		next.UpdatedAt = s.now()
		return nil
	})
	if err != nil {
		return Completion{}, mapRepositoryError(err)
	}
	if claimed.State == StateExpired {
		return Completion{}, ErrExpired
	}

	principal, authErr := s.google.AuthenticateGoogle(ctx, GoogleExchange{
		TransactionID:     claimed.ID,
		AuthorizationCode: in.AuthorizationCode,
		CodeVerifier:      claimed.GoogleCodeVerifier,
	})
	if authErr != nil || principal.UserID == 0 {
		s.failGoogleClaim(ctx, claimed)
		if err := ctx.Err(); err != nil {
			return Completion{}, err
		}
		return Completion{}, ErrAuthenticationFailed
	}

	completion, err := s.completeAuthentication(ctx, claimed, principal, AuthMethodGoogle)
	if err != nil {
		// Provider codes are single-use. Once the identity adapter has run, any
		// unsuccessful local completion is terminal. A committed authentication
		// transition wins through the repository version check.
		s.failGoogleClaim(ctx, claimed)
		return Completion{}, err
	}
	return completion, nil
}

// Exchange atomically consumes a post-authentication exchange token and
// returns the immutable authorization grant exactly once.
func (s *Service) Exchange(ctx context.Context, in ExchangeInput) (Grant, error) {
	if err := validateExchangeInput(in); err != nil {
		return Grant{}, err
	}
	record, err := s.loadRecord(ctx, in.TransactionID)
	if err != nil {
		return Grant{}, err
	}
	record, err = s.ensureFresh(ctx, record)
	if err != nil {
		return Grant{}, err
	}
	if record.State != StateAuthenticated {
		return Grant{}, exchangeStateError(record.State)
	}
	if !secretMatches(record.ExchangeTokenHash, in.ExchangeToken) {
		return Grant{}, ErrInvalidTransaction
	}

	updated, err := s.repo.CompareAndSwap(ctx, record.ID, record.Version, func(next *Record) error {
		if s.expireDuringMutation(next) {
			return nil
		}
		if next.State != StateAuthenticated {
			return exchangeStateError(next.State)
		}
		if !secretMatches(next.ExchangeTokenHash, in.ExchangeToken) {
			return ErrInvalidTransaction
		}
		now := s.now()
		next.State = StateExchanged
		next.ExchangeTokenHash = [32]byte{}
		next.ExchangedAt = now
		next.UpdatedAt = now
		return nil
	})
	if err != nil {
		return Grant{}, mapRepositoryError(err)
	}
	if updated.State == StateExpired {
		return Grant{}, ErrExpired
	}
	return grantFromRecord(updated), nil
}

// Inspect returns a non-secret status snapshot and lazily commits expiry. It
// is intended for trusted server-internal orchestration. An HTTP resume/status
// boundary must use InspectAuthenticated instead of accepting an id alone.
func (s *Service) Inspect(ctx context.Context, transactionID string) (Snapshot, error) {
	if !validRandomToken(transactionID, transactionIDBytes) {
		return Snapshot{}, ErrInvalidTransaction
	}
	record, err := s.loadRecord(ctx, transactionID)
	if err != nil {
		return Snapshot{}, err
	}
	record, freshErr := s.ensureFresh(ctx, record)
	if freshErr != nil && !errors.Is(freshErr, ErrExpired) {
		return Snapshot{}, freshErr
	}
	return snapshotFromRecord(record), nil
}

// InspectAuthenticated returns the same bounded snapshot after verifying the
// transaction bearer secret. Unknown transaction ids and incorrect secrets
// deliberately collapse to ErrInvalidTransaction. Expiry remains an
// inspectable terminal status rather than an error so an HTTP resume caller
// can stop polling deterministically.
func (s *Service) InspectAuthenticated(
	ctx context.Context,
	transactionID string,
	transactionSecret string,
) (Snapshot, error) {
	record, err := s.loadWithTransactionSecret(ctx, transactionID, transactionSecret)
	if err != nil && !errors.Is(err, ErrExpired) {
		return Snapshot{}, err
	}
	return snapshotFromRecord(record), nil
}

func (s *Service) completeAuthentication(
	ctx context.Context,
	claimed Record,
	principal Principal,
	method AuthMethod,
) (Completion, error) {
	exchangeToken, err := s.randomToken(exchangeTokenBytes)
	if err != nil {
		return Completion{}, err
	}
	expectedState := StatePasswordAuthenticating
	if method == AuthMethodGoogle {
		expectedState = StateGoogleExchanging
	}
	updated, err := s.repo.CompareAndSwap(ctx, claimed.ID, claimed.Version, func(next *Record) error {
		if s.expireDuringMutation(next) {
			return nil
		}
		if next.State != expectedState {
			return ErrConflict
		}
		now := s.now()
		next.State = StateAuthenticated
		next.UserID = principal.UserID
		next.AuthMethod = method
		next.ExchangeTokenHash = hashSecret(exchangeToken)
		next.GoogleStateHash = [32]byte{}
		next.GoogleCodeVerifier = ""
		next.AuthenticatedAt = now
		next.UpdatedAt = now
		return nil
	})
	if err != nil {
		return Completion{}, mapRepositoryError(err)
	}
	if updated.State == StateExpired {
		return Completion{}, ErrExpired
	}
	return Completion{
		TransactionID: updated.ID,
		ExchangeToken: exchangeToken,
		AuthMethod:    method,
		ExpiresAt:     updated.ExpiresAt,
	}, nil
}

func (s *Service) releasePasswordClaim(ctx context.Context, claimed Record, rejected bool) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimCleanupTimeout)
	defer cancel()
	_, err := s.repo.CompareAndSwap(cleanupCtx, claimed.ID, claimed.Version, func(next *Record) error {
		if next.State != StatePasswordAuthenticating {
			return ErrConflict
		}
		if s.expireDuringMutation(next) {
			return nil
		}
		now := s.now()
		if rejected {
			next.PasswordFailures++
			next.LastPasswordFailure = now
		}
		if next.PasswordFailures >= MaxPasswordFailures {
			next.State = StateFailed
		} else {
			next.State = StatePending
		}
		next.UpdatedAt = now
		return nil
	})
	return mapRepositoryError(err)
}

func (s *Service) failGoogleClaim(ctx context.Context, claimed Record) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimCleanupTimeout)
	defer cancel()
	_, _ = s.repo.CompareAndSwap(cleanupCtx, claimed.ID, claimed.Version, func(next *Record) error {
		if next.State != StateGoogleExchanging {
			return ErrConflict
		}
		if s.expireDuringMutation(next) {
			return nil
		}
		next.State = StateFailed
		next.GoogleStateHash = [32]byte{}
		next.GoogleCodeVerifier = ""
		next.UpdatedAt = s.now()
		return nil
	})
}

func (s *Service) loadWithTransactionSecret(
	ctx context.Context,
	id string,
	secret string,
) (Record, error) {
	if !validRandomToken(id, transactionIDBytes) || !validRandomToken(secret, transactionSecretBytes) {
		return Record{}, ErrInvalidTransaction
	}
	record, err := s.loadRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if !secretMatches(record.SecretHash, secret) {
		return Record{}, ErrInvalidTransaction
	}
	return s.ensureFresh(ctx, record)
}

func (s *Service) loadRecord(ctx context.Context, id string) (Record, error) {
	record, err := s.repo.Get(ctx, id)
	if err != nil {
		return Record{}, mapRepositoryError(err)
	}
	return record, nil
}

func (s *Service) ensureFresh(ctx context.Context, initial Record) (Record, error) {
	record := initial
	for attempt := 0; attempt < createCollisionRetries; attempt++ {
		if record.State == StateExpired {
			return record, ErrExpired
		}
		if !stateCanExpire(record.State) || s.now().Before(record.ExpiresAt) {
			return record, nil
		}
		updated, err := s.repo.CompareAndSwap(ctx, record.ID, record.Version, func(next *Record) error {
			if stateCanExpire(next.State) && !s.now().Before(next.ExpiresAt) {
				expireRecord(next, s.now())
			}
			return nil
		})
		if err == nil {
			if updated.State == StateExpired {
				return updated, ErrExpired
			}
			return updated, nil
		}
		if !errors.Is(err, ErrVersionConflict) {
			return Record{}, mapRepositoryError(err)
		}
		record, err = s.repo.Get(ctx, record.ID)
		if err != nil {
			return Record{}, mapRepositoryError(err)
		}
	}
	return Record{}, ErrConflict
}

func (s *Service) expireDuringMutation(record *Record) bool {
	if stateCanExpire(record.State) && !s.now().Before(record.ExpiresAt) {
		expireRecord(record, s.now())
		return true
	}
	return record.State == StateExpired
}

func expireRecord(record *Record, now time.Time) {
	record.State = StateExpired
	record.GoogleStateHash = [32]byte{}
	record.GoogleCodeVerifier = ""
	record.ExchangeTokenHash = [32]byte{}
	record.UpdatedAt = now.UTC()
}

func stateCanExpire(state State) bool {
	switch state {
	case StatePending,
		StatePasswordAuthenticating,
		StateGooglePending,
		StateGoogleExchanging,
		StateAuthenticated:
		return true
	default:
		return false
	}
}

func passwordStateError(state State) error {
	switch state {
	case StateAuthenticated, StateExchanged:
		return ErrReplay
	case StateExpired:
		return ErrExpired
	case StatePasswordAuthenticating, StateGooglePending, StateGoogleExchanging:
		return ErrConflict
	default:
		return ErrInvalidState
	}
}

func googleStartStateError(state State) error {
	switch state {
	case StateGooglePending, StateGoogleExchanging, StateAuthenticated, StateExchanged:
		return ErrReplay
	case StateExpired:
		return ErrExpired
	case StatePasswordAuthenticating:
		return ErrConflict
	default:
		return ErrInvalidState
	}
}

func googleCompletionStateError(state State) error {
	switch state {
	case StateGoogleExchanging, StateAuthenticated, StateExchanged, StateFailed:
		return ErrReplay
	case StateExpired:
		return ErrExpired
	default:
		return ErrInvalidState
	}
}

func exchangeStateError(state State) error {
	switch state {
	case StateExchanged:
		return ErrReplay
	case StateExpired:
		return ErrExpired
	case StatePasswordAuthenticating, StateGooglePending, StateGoogleExchanging:
		return ErrConflict
	default:
		return ErrInvalidState
	}
}

func mapRepositoryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrRecordNotFound):
		return ErrInvalidTransaction
	case errors.Is(err, ErrVersionConflict):
		return ErrConflict
	default:
		return err
	}
}

func snapshotFromRecord(record Record) Snapshot {
	return Snapshot{
		TransactionID: record.ID,
		Version:       record.Version,
		State:         record.State,
		AuthMethod:    record.AuthMethod,
		CreatedAt:     record.CreatedAt,
		UpdatedAt:     record.UpdatedAt,
		ExpiresAt:     record.ExpiresAt,
	}
}

func grantFromRecord(record Record) Grant {
	return Grant{
		TransactionID:       record.ID,
		UserID:              record.UserID,
		ClientID:            record.Request.ClientID,
		RedirectURI:         record.Request.RedirectURI,
		OAuthState:          record.Request.OAuthState,
		CodeChallenge:       record.Request.CodeChallenge,
		CodeChallengeMethod: record.Request.CodeChallengeMethod,
		Scope:               record.Request.Scope,
		DeviceID:            record.Request.DeviceID,
		AuthMethod:          record.AuthMethod,
		AuthenticatedAt:     record.AuthenticatedAt,
	}
}

func (s *Service) randomToken(byteCount int) (string, error) {
	b := make([]byte, byteCount)
	s.randomMu.Lock()
	_, err := io.ReadFull(s.random, b)
	s.randomMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("%w", ErrEntropyUnavailable)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Service) now() time.Time { return s.clock.Now().UTC() }

func hashSecret(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func secretMatches(expected [32]byte, value string) bool {
	actual := hashSecret(value)
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func providerStateTransactionID(state string) (string, bool) {
	separator := strings.LastIndexByte(state, '.')
	if separator <= 0 || separator == len(state)-1 {
		return "", false
	}
	id := state[:separator]
	nonce := state[separator+1:]
	if !validRandomToken(id, transactionIDBytes) || !validRandomToken(nonce, googleStateBytes) {
		return "", false
	}
	return id, true
}
