package logintransaction

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	// DefaultTTL is the maximum lifetime of a Desktop login transaction.
	DefaultTTL = 10 * time.Minute
	// MaxPasswordFailures is the durable per-transaction credential-attempt
	// budget. Account/device/IP-wide controls remain a separate deployment
	// concern, but a restart or second Server instance cannot reset this budget.
	MaxPasswordFailures uint16 = 5

	// GooglePKCEMethod is the only provider PKCE method emitted by this
	// package. Plain challenges are never supported.
	GooglePKCEMethod = "S256"
)

var (
	ErrInvalidInput             = errors.New("desktop login transaction: invalid input")
	ErrInvalidTransaction       = errors.New("desktop login transaction: invalid transaction")
	ErrExpired                  = errors.New("desktop login transaction: expired")
	ErrConflict                 = errors.New("desktop login transaction: concurrent update conflict")
	ErrInvalidState             = errors.New("desktop login transaction: invalid state")
	ErrReplay                   = errors.New("desktop login transaction: replay rejected")
	ErrAuthenticationFailed     = errors.New("desktop login transaction: authentication failed")
	ErrAuthenticatorUnavailable = errors.New("desktop login transaction: authenticator unavailable")
	ErrEntropyUnavailable       = errors.New("desktop login transaction: secure randomness unavailable")
)

// State is the persisted state machine for one login transaction.
type State string

const (
	StatePending                State = "pending"
	StatePasswordAuthenticating State = "password_authenticating"
	StateGooglePending          State = "google_pending"
	StateGoogleExchanging       State = "google_exchanging"
	StateAuthenticated          State = "authenticated"
	StateExchanged              State = "exchanged"
	StateFailed                 State = "failed"
	StateExpired                State = "expired"
)

// AuthMethod identifies the identity proof that completed the transaction.
type AuthMethod string

const (
	AuthMethodPassword AuthMethod = "password"
	AuthMethodGoogle   AuthMethod = "google"
)

// CreateInput is the already validated OAuth authorization request frozen into
// a Desktop login transaction. The service repeats the safety-critical local
// shape checks, but the API boundary must still validate client registration,
// its scope allowlist, and any deployment-specific policy before calling
// Create.
type CreateInput struct {
	ClientID            string
	RedirectURI         string
	OAuthState          string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	DeviceID            string
}

// Principal is the stable server-side identity returned by an authenticator.
// No caller-supplied user id is ever accepted by the transaction service.
type Principal struct {
	UserID uint
}

// PasswordAuthenticator is the seam to the account identity service. A
// production adapter should return the same bounded error for an unknown user
// and an invalid password, and should migrate legacy password hashes on a
// successful login. The transaction package never imports the legacy MD5/JWT
// handlers directly.
type PasswordAuthenticator interface {
	AuthenticatePassword(ctx context.Context, email, password string) (Principal, error)
}

// GoogleExchange is the provider authorization code plus the PKCE verifier
// generated and retained by the login transaction service.
type GoogleExchange struct {
	TransactionID     string
	AuthorizationCode string
	CodeVerifier      string
}

// GoogleAuthenticator exchanges a Google authorization code and resolves it
// to a stable WorkMax principal. A production adapter must validate the
// provider subject and verified-email/linking policy.
type GoogleAuthenticator interface {
	AuthenticateGoogle(ctx context.Context, exchange GoogleExchange) (Principal, error)
}

// Clock is injectable so TTL and boundary behavior are deterministic in tests.
type Clock interface {
	Now() time.Time
}

// Options configures the domain service without global configuration.
type Options struct {
	TTL    time.Duration
	Clock  Clock
	Random io.Reader
}

// TransactionHandle is the capability returned by Create. Secret must remain
// inside the Sidecar/Main login coordinator and must never be returned to the
// general-purpose Desktop Renderer.
type TransactionHandle struct {
	TransactionID     string
	TransactionSecret string
	ExpiresAt         time.Time
}

type PasswordCompletionInput struct {
	TransactionID     string
	TransactionSecret string
	Email             string
	Password          string
}

type GoogleStartInput struct {
	TransactionID     string
	TransactionSecret string
}

// GoogleStart contains the opaque provider state and a provider-facing S256
// challenge. The verifier remains inside the repository record and is only
// handed to GoogleAuthenticator during CompleteGoogle.
type GoogleStart struct {
	TransactionID       string
	ProviderState       string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

type GoogleCompletionInput struct {
	ProviderState     string
	AuthorizationCode string
}

// Completion carries a fresh one-time exchange capability. It is intended for
// trusted server/Sidecar orchestration only and must not cross into the general
// Renderer API.
type Completion struct {
	TransactionID string
	ExchangeToken string
	AuthMethod    AuthMethod
	ExpiresAt     time.Time
}

type ExchangeInput struct {
	TransactionID string
	ExchangeToken string
}

// Grant is the immutable, non-secret result consumed by the authorization-code
// issuer. Exchange returns it exactly once.
type Grant struct {
	TransactionID       string
	UserID              uint
	ClientID            string
	RedirectURI         string
	OAuthState          string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	DeviceID            string
	AuthMethod          AuthMethod
	AuthenticatedAt     time.Time
}

// Snapshot is the bounded status model safe for orchestration and polling. It
// intentionally excludes every secret, password, provider code, PKCE verifier,
// and authorization grant payload.
type Snapshot struct {
	TransactionID string
	Version       uint64
	State         State
	AuthMethod    AuthMethod
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ExpiresAt     time.Time
}
