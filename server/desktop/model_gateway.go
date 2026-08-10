//go:build desktop

package desktop

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
)

// model_gateway.go lets a local tool loop run on an OFFICIAL model without any
// cloud credential ever landing on this machine.
//
// The problem it solves: the L2 engines drive somebody else's binary (the
// claude CLI, the pi subprocess), and those binaries authenticate the only way
// they know how — an API key in an environment variable or a config file. The
// obvious implementation is therefore to hand them a real credential, which is
// exactly what must not happen: an OAuth access token written into a
// subprocess environment (or, for pi, into models.json on disk) is a cloud
// credential sitting in the least protected place on the machine, outliving
// the turn that needed it and readable by anything running as the user.
//
// So the sidecar becomes the gateway. It publishes an Anthropic-shaped and an
// OpenAI-shaped endpoint on its own loopback port, authenticated by a token
// that is minted with crypto/rand at process start, kept in memory, and never
// written anywhere. A subprocess holding it can reach one thing — this
// process, on this machine — and the sidecar turns that into a cloud request
// with the OAuth session it was already managing. What leaks if the "API key"
// leaks is the ability to talk to a sidecar you already had code execution
// next to.
//
// Revocation is therefore the session's own: logging out rotates the token and
// cancels every forward already in flight, and entitlement is re-checked by
// the cloud on every single request rather than cached into a local grant.

// Model gateway protocol names. These are the sidecar's own path segments, and
// they mirror the cloud's: /model-gateway/<protocol>/... here forwards to
// /api/desktop/model-gateway/<protocol>/... there.
const (
	modelGatewayProtocolAnthropic = "anthropic"
	modelGatewayProtocolOpenAI    = "openai"
)

// modelGatewayTokenBytes is the entropy behind the loopback credential. 32
// bytes matches the local token and the UI capability: guessing is not a
// threat model anybody has to think about again.
const modelGatewayTokenBytes = 32

// ModelGateway owns the loopback credential and the port it is valid on.
//
// It deliberately holds no cloud material: the access token for an upstream
// forward is acquired per request from the TokenStore the rest of the sidecar
// uses, so there is no second place a session can go stale or survive a
// logout.
type ModelGateway struct {
	mu     sync.RWMutex
	token  string
	digest [sha256.Size]byte
	port   int

	// session is canceled on every rotation. Forwards bind to it, so a logout
	// does not merely stop the NEXT request — it kills the ones already
	// streaming, which is the difference between revoking a credential and
	// announcing that it will expire eventually.
	session context.Context
	cancel  context.CancelFunc

	// client has no overall timeout: a forwarded turn is an SSE stream that
	// lives for minutes. A dead upstream is bounded by the caller's context,
	// exactly as in cloud_proxy.Proxy.
	client *http.Client
}

// NewModelGateway mints the first loopback token. It fails only if the system
// entropy source does, which is not a state worth booting through.
func NewModelGateway() (*ModelGateway, error) {
	g := &ModelGateway{client: &http.Client{Timeout: 0}}
	if err := g.Rotate(); err != nil {
		return nil, err
	}
	return g, nil
}

// SetPort publishes the loopback port the gateway answers on. Called once,
// after the sidecar's listener is bound — the base URLs handed to subprocesses
// are meaningless before that, and the profile reader asks for them lazily at
// turn time.
func (g *ModelGateway) SetPort(port int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.port = port
}

// Ready reports whether the gateway can be addressed yet.
func (g *ModelGateway) Ready() bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.port != 0 && g.token != ""
}

// Token is the current loopback credential. Sidecar-internal: callers may put
// it in a subprocess environment, and must never log it, return it to the
// renderer, or write it into an SSE frame.
func (g *ModelGateway) Token() string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.token
}

// Matches compares a presented credential against the current one in constant
// time over fixed-length digests, the same shape as the local token check.
// A rotated-away token fails here, which is what makes logout immediate for
// requests that have not started yet.
func (g *ModelGateway) Matches(candidate string) bool {
	if g == nil || candidate == "" {
		return false
	}
	g.mu.RLock()
	expected := g.digest
	empty := g.token == ""
	g.mu.RUnlock()
	if empty {
		return false
	}
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(got[:], expected[:]) == 1
}

// Rotate mints a new token and cancels every forward bound to the old one.
//
// Both halves matter. Without the new token, a subprocess started before a
// logout keeps a working credential; without the cancellation, a turn that was
// already streaming keeps consuming the account it just signed out of.
func (g *ModelGateway) Rotate() error {
	if g == nil {
		return nil
	}
	var raw [modelGatewayTokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Errorf("model gateway token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])

	g.mu.Lock()
	previous := g.cancel
	session, cancel := context.WithCancel(context.Background())
	g.token = token
	g.digest = sha256.Sum256([]byte(token))
	g.session = session
	g.cancel = cancel
	g.mu.Unlock()

	if previous != nil {
		previous()
	}
	return nil
}

// Shutdown cancels in-flight forwards without minting a replacement. The
// gateway is unusable afterwards; call it when the process is going away.
func (g *ModelGateway) Shutdown() {
	if g == nil {
		return
	}
	g.mu.Lock()
	cancel := g.cancel
	g.token = ""
	g.digest = [sha256.Size]byte{}
	g.cancel = nil
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// BindContext ties one forward to the current token generation. The returned
// context is canceled when the caller's context ends OR when the token
// rotates, whichever happens first.
func (g *ModelGateway) BindContext(parent context.Context) (context.Context, func()) {
	if g == nil {
		return parent, func() {}
	}
	g.mu.RLock()
	session := g.session
	g.mu.RUnlock()
	if session == nil {
		bound, cancel := context.WithCancel(parent)
		cancel()
		return bound, func() {}
	}
	bound, cancel := context.WithCancel(parent)
	stopSessionWatch := context.AfterFunc(session, cancel)
	return bound, func() {
		stopSessionWatch()
		cancel()
	}
}

// BaseURLFor is the endpoint a local engine points at for one local protocol.
//
// The version segment is deliberately NOT part of the base: the claude CLI
// appends /v1/messages to ANTHROPIC_BASE_URL and cannot be told otherwise, so
// the base it is handed must stop before the version. This repo's L1 adapter
// now appends the same full path (local_inference.AnthropicBaseURL), so one
// base string serves both engines and neither needs special handling. The
// unversioned spellings stay registered as tolerance for a client that
// disagrees — see route_policy.go.
func (g *ModelGateway) BaseURLFor(localProtocol string) string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	port := g.port
	live := g.token != ""
	g.mu.RUnlock()
	// A shut-down gateway has no credential, so an endpoint pointing at it
	// would only produce a 401 somewhere further from the cause.
	if port == 0 || !live {
		return ""
	}
	protocol, ok := modelGatewayProtocolFor(localProtocol)
	if !ok {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/model-gateway/" + protocol
}

// modelGatewayProtocolFor maps a stored local-model protocol onto the gateway
// wire shape it speaks. The mapping is the identity the names suggest; it
// exists so the two vocabularies cannot drift silently.
func modelGatewayProtocolFor(localProtocol string) (string, bool) {
	switch localProtocol {
	case LocalProtocolAnthropicCompatible:
		return modelGatewayProtocolAnthropic, true
	case LocalProtocolOpenAICompatible:
		return modelGatewayProtocolOpenAI, true
	default:
		return "", false
	}
}

// rotateModelGatewayToken is the sidecar-side hook for a session change. It is
// called on logout and on a completed password login: in both cases the
// account a local subprocess would be billing to has changed, and a credential
// that survives that is a credential that bills the wrong person.
func (s *Server) rotateModelGatewayToken(reason string) {
	if s == nil || s.cfg.ModelGateway == nil {
		return
	}
	if err := s.cfg.ModelGateway.Rotate(); err != nil {
		// Rotation only fails when crypto/rand does. Shut the gateway instead
		// of leaving the retired token valid: refusing official-model turns is
		// recoverable by restarting, spending somebody else's membership is
		// not.
		s.cfg.ModelGateway.Shutdown()
		log.Printf("model gateway: rotation failed on %s; gateway disabled until restart: %v", reason, err)
		return
	}
	// The reason is logged so a support transcript can explain why a running
	// tool loop suddenly lost its endpoint. The token never is.
	log.Printf("model gateway: loopback token rotated (%s)", reason)
}
