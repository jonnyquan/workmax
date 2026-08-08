package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// PendingAuthorizationTTL caps how long an in-flight consent request
// lives. Browser navigations finish in seconds; 10 min gives plenty
// of headroom for distracted users without keeping abandoned requests
// in memory forever.
const PendingAuthorizationTTL = 10 * time.Minute

// PendingAuthorization holds the validated /authorize request payload
// between the GET (renders consent) and the POST (processes approve/
// deny). One row per outstanding consent screen.
//
// Stored in-process for P0 (single backend instance, brief lifetime).
// P3 multi-instance backend would swap to Redis or DB persistence.
type PendingAuthorization struct {
	ID                  string
	ClientID            string
	UID                 int
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	State               string // OAuth `state` parameter, echoed back on redirect
	CreatedAt           time.Time
	ExpiresAt           time.Time
}

// ErrPendingNotFound is returned by Consume when the ID doesn't match
// any active pending request (unknown ID, expired, or already
// consumed).
var ErrPendingNotFound = errors.New("pending_authorization: not found or expired")

// PendingAuthorizationService is an in-memory cache of pending consent
// requests. Safe for concurrent use.
type PendingAuthorizationService struct {
	mu       sync.Mutex
	now      func() time.Time
	random   io.Reader
	requests map[string]*PendingAuthorization

	stopSweepOnce sync.Once
	stopSweep     chan struct{}
}

// NewPendingAuthorizationService returns a service with a background
// goroutine sweeping expired entries every 30s. Call Stop() in tests
// or graceful shutdown.
func NewPendingAuthorizationService() *PendingAuthorizationService {
	s := &PendingAuthorizationService{
		now:       func() time.Time { return time.Now().UTC() },
		random:    rand.Reader,
		requests:  make(map[string]*PendingAuthorization),
		stopSweep: make(chan struct{}),
	}
	go s.sweepLoop(30 * time.Second)
	return s
}

// StoreInput captures everything Authorize() validated and needs to
// hand off to Consume() after the user clicks Approve/Deny.
type StoreInput struct {
	ClientID            string
	UID                 int
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	State               string
}

// Store creates a new PendingAuthorization with a random ID and
// returns the ID. The ID is what the consent form sends back via
// the hidden form input.
func (s *PendingAuthorizationService) Store(ctx context.Context, in StoreInput) (string, error) {
	id, err := generateOpaqueCode(s.random, 16) // 16 bytes = 22-char base64url
	if err != nil {
		return "", fmt.Errorf("pending_authorization: generate id: %w", err)
	}
	now := s.now()
	pa := &PendingAuthorization{
		ID:                  id,
		ClientID:            in.ClientID,
		UID:                 in.UID,
		RedirectURI:         in.RedirectURI,
		CodeChallenge:       in.CodeChallenge,
		CodeChallengeMethod: in.CodeChallengeMethod,
		Scope:               in.Scope,
		State:               in.State,
		CreatedAt:           now,
		ExpiresAt:           now.Add(PendingAuthorizationTTL),
	}
	s.mu.Lock()
	s.requests[id] = pa
	s.mu.Unlock()
	return id, nil
}

// Consume atomically looks up and removes a pending request by ID.
// Returns ErrPendingNotFound for unknown, expired, or already-consumed
// IDs.
func (s *PendingAuthorizationService) Consume(ctx context.Context, id string) (*PendingAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pa, ok := s.requests[id]
	if !ok {
		return nil, ErrPendingNotFound
	}
	delete(s.requests, id)
	if s.now().After(pa.ExpiresAt) {
		return nil, ErrPendingNotFound
	}
	return pa, nil
}

// Stop terminates the background sweeper. Idempotent. Use in tests
// and during graceful shutdown.
func (s *PendingAuthorizationService) Stop() {
	s.stopSweepOnce.Do(func() {
		close(s.stopSweep)
	})
}

// Size returns the current number of pending requests. Mostly for
// tests and metrics.
func (s *PendingAuthorizationService) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *PendingAuthorizationService) sweepLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stopSweep:
			return
		case <-t.C:
			s.sweep()
		}
	}
}

func (s *PendingAuthorizationService) sweep() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, pa := range s.requests {
		if now.After(pa.ExpiresAt) {
			delete(s.requests, id)
		}
	}
}

// idBytesForTest is a test-only helper that lets us pass a
// deterministic random reader.
func (s *PendingAuthorizationService) setRandomForTest(r io.Reader) {
	s.random = r
}

// nowForTest swaps the clock for deterministic expiry tests.
func (s *PendingAuthorizationService) setNowForTest(now func() time.Time) {
	s.now = now
}

// base64 import is implied via generateOpaqueCode which lives in
// authorization_code.go. Keep this var to make the import explicit
// for readers (it's used transitively).
var _ = base64.RawURLEncoding
