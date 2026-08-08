//go:build desktop

package desktop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/desktop/auth"
	cloudproxy "server/desktop/cloud_proxy"
	localrender "server/desktop/local_render"
	desktopsync "server/desktop/sync"
)

// ServerConfig is the runtime configuration the sidecar HTTP server
// needs at startup. LocalToken and DB are required by NewServer.
// KnowledgeIndex is the sidecar's write surface onto the local knowledge
// store: index an uploaded file, and remove what was indexed when its source
// goes away. Satisfied structurally by the lazy wiring around
// *knowledge.Indexer. Nil on the config means the sidecar runs without RAG —
// uploads still succeed, deletes still delete, nothing is indexed or removed.
// Defined here as an interface so the desktop package does not depend on the
// cgo knowledge package.
//
// (Named FileIndexer until thread deletion needed turn removal too; the
// interface covers both source types now, so the file-only name was a lie.)
type KnowledgeIndex interface {
	IndexFile(ctx context.Context, uid uint64, fileID int64) error
	RemoveFile(ctx context.Context, fileID int64) (int, error)
	RemoveTurn(ctx context.Context, turnUUID string) (int, error)
}

// Other subsystems are optional at boot and become required only when
// their endpoint is hit, so diagnostics and read-only local history
// can still work in trimmed test/support configurations.
type ServerConfig struct {
	SidecarVersion string
	LocalToken     string // X-Local-Token expected on every request
	DB             *gorm.DB
	DeviceID       string

	// OAuth wiring. Nil OAuthFlow causes /auth/start to return 503;
	// nil TokenStore causes /auth/logout to return 503. /auth/status
	// intentionally returns a stable unauthenticated payload when
	// TokenStore is nil so the renderer can still route to LoginPage
	// in trimmed test/support configurations.
	OAuthFlow  *cloudproxy.OAuthFlow
	TokenStore *cloudproxy.TokenStore

	// LoginCoordinator owns the Server-backed password transaction. Nil keeps
	// the four fixed local routes present but fail-closed for diagnostic/test
	// boots; production Main wires the concrete cloud_proxy coordinator.
	LoginCoordinator PasswordLoginCoordinator

	// Cloud Proxy. Nil → /agent/chat returns 503 with a clear
	// message. Required for actual chat traffic; we keep the field
	// optional so tests + diagnostics can boot without it.
	Proxy *cloudproxy.Proxy

	// Network state watcher. Nil → /system/network-state returns
	// 503. Main wires this with the production HTTPNetworkProbe; tests
	// can substitute a stub.
	NetworkState *NetworkStateWatcher

	// MessagesSyncer (P1.B.3.x.2). Nil → /agent/threads/:uuid/messages
	// still serves local rows but does NOT trigger a fresh sync.
	// Optional so test fixtures that don't care about the trigger
	// can boot without standing up the full sync chain.
	MessagesSyncer *desktopsync.MessagesSyncer

	// ThreadsSyncer (P1.B.3) periodic threads-sync worker. Nil leaves
	// /system/diagnostics with a zero ThreadsSyncer block; the rest
	// of the diagnostic surface still renders.
	ThreadsSyncer *desktopsync.SyncWorker

	// RendererLogger receives structured log entries POSTed by the
	// renderer to /system/log. Nil → endpoint returns 503 with a
	// clear message so the renderer falls back to console.* and the
	// developer notices that logs aren't being captured.
	RendererLogger *RendererLogger

	// DataDir is the per-user data root the sidecar resolved at boot
	// (~/.workmax or WORKMAX_DESKTOP_DATA_DIR override). Surfaced in
	// /system/diagnostics so support can tell users where to look
	// for their logs / SQLite cache. Empty in test fixtures.
	DataDir string

	// DBPath is the absolute path of the SQLite cache file. Same
	// rationale as DataDir — exposed for ops/support visibility.
	DBPath string

	// BackupPath is the daily SQLite backup OpenLocalDB created or
	// reused at boot. Exposed through diagnostics so support can ask
	// for the exact recovery candidate rather than guessing by date.
	BackupPath string

	// IntegrityCheck is the boot-time SQLite PRAGMA integrity_check
	// result. "ok" means the sidecar accepted the DB; empty means an
	// older/test fixture did not populate it.
	IntegrityCheck string

	// ModelSettings owns preferred_route + local model profile (non-secret
	// fields in SQLite, API key in Keychain). Nil → /settings/model-route
	// returns 503; production Main always wires a store.
	ModelSettings *LocalModelSettingsStore

	// LocalInference runs agent turns against the user-configured local model
	// when preferred_route=local (L1). Nil → local route is never selected
	// (shouldUseLocalRoute returns false); production Main wires the engine.
	LocalInference TurnRunner

	// LocalAgent runs L2 tool-loop turns (Claude Agent SDK + claude CLI) for
	// anthropic_compatible local models. Nil → those turns fall back to
	// LocalInference pure chat. Dispatch: Server.localTurnRunner.
	LocalAgent TurnRunner

	// LocalFiles persists file attachments uploaded via
	// POST /agent/threads/:uuid/files to local disk + w_workagent_thread_file,
	// for use as model context in local turns (L3b). Nil → upload route returns 503.
	LocalFiles *localrender.Store

	// KnowledgeIndex indexes uploaded files for local RAG retrieval (L3c-3)
	// and removes indexed content when its source is deleted. Nil → uploads
	// still succeed but are not indexed, and deletes skip knowledge cleanup.
	KnowledgeIndex KnowledgeIndex
}

// Server wraps the sidecar's loopback HTTP server, the listener it's
// bound to (kept so callers can read back the OS-assigned port), and
// minimal boot-time metadata.
type Server struct {
	cfg             ServerConfig
	listener        net.Listener
	httpServer      *http.Server
	startedAt       time.Time
	authLifecycleMu sync.Mutex
	agentTurnLocks  sync.Map // turn_uuid -> *sync.Mutex; process-lifetime TryLock registry
	authClosing     atomic.Bool
	authContext     context.Context
	authCancel      context.CancelFunc
}

// NewServer binds to 127.0.0.1:0 (OS-assigned port), registers the desktop
// endpoint surface with an explicit per-route credential/request policy, and
// keeps the authenticated not-found fallback that prevents route discovery.
// It does NOT start serving — call Serve() once the caller has published
// the port however its transport does that. There is no longer a parent
// process to hand it to; the Wails shell reads it in-process.
//
// The dynamic-port choice is deliberate: hardcoding a port would
// collide with another sidecar instance or another app, and
// hardcoding a port range adds discovery complexity for no benefit.
// See sidecar-protocol.md §3.1.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.LocalToken == "" {
		return nil, fmt.Errorf("ServerConfig.LocalToken is required")
	}
	if cfg.DB == nil {
		return nil, fmt.Errorf("ServerConfig.DB is required")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on 127.0.0.1:0: %w", err)
	}

	authContext, authCancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:         cfg,
		listener:    listener,
		startedAt:   time.Now().UTC(),
		authContext: authContext,
		authCancel:  authCancel,
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// Privileged loopback routes must never be reached through Gin's implicit
	// trailing-slash or fixed-path redirects. The renderer bridge
	// also rejects redirects, but the Sidecar keeps this independent second
	// line of defense for every caller.
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.Use(gin.Recovery())
	// Keep the current whole-port perimeter in addition to the explicit
	// credential on every registered route. This preserves token-first
	// behavior for Gin redirects, unknown paths, and wrong methods while the
	// per-route table becomes the auditable source for route policy.
	router.Use(auth.RequireLocalToken(cfg.LocalToken))
	if err := registerCurrentSidecarRoutes(router, s, cfg.LocalToken); err != nil {
		authCancel()
		_ = listener.Close()
		return nil, fmt.Errorf("register sidecar routes: %w", err)
	}
	registerAuthenticatedNotFound(router)

	s.httpServer = &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: SSE responses live for the duration
		// of a long-running chat turn. Idle SSE keepalive carries the
		// burden of detecting dead clients.
	}

	return s, nil
}

// Port returns the OS-assigned port the listener bound to. Safe to
// call any time after NewServer.
func (s *Server) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Serve blocks until the underlying http.Server exits (either via
// Shutdown or an error). Mirrors http.Server.Serve semantics: returns
// http.ErrServerClosed on graceful shutdown.
func (s *Server) Serve() error {
	return s.httpServer.Serve(s.listener)
}

// Shutdown attempts a graceful close. Should be invoked with a bounded
// context (we recommend 5s) so a hung in-flight request can't keep the
// process around forever.
func (s *Server) Shutdown(ctx context.Context) error {
	s.authClosing.Store(true)
	if s.authCancel != nil {
		s.authCancel()
	}
	cancelAuth := func() {
		var cancelGroup sync.WaitGroup
		if s.cfg.OAuthFlow != nil {
			cancelGroup.Add(1)
			go func() {
				defer cancelGroup.Done()
				s.cfg.OAuthFlow.Cancel()
			}()
		}
		if s.cfg.LoginCoordinator != nil {
			cancelGroup.Add(1)
			go func() {
				defer cancelGroup.Done()
				s.cfg.LoginCoordinator.Cancel()
			}()
		}
		cancelGroup.Wait()
	}
	// Keychain commits are deliberately fenced under coordinator locks and may
	// take up to their command deadline. Run both flow cancellations in parallel
	// with the HTTP drain so Shutdown still honors its caller's context.
	cleanupDone := make(chan struct{})
	go func() {
		cancelAuth()
		close(cleanupDone)
	}()

	shutdownErr := s.httpServer.Shutdown(ctx)
	var closeErr error
	if shutdownErr != nil {
		// Shutdown does not force-close active connections when its context ends.
		// The Sidecar is exiting, so prevent a timed-out auth handler from keeping
		// the process or a credential-bearing request alive.
		if err := s.httpServer.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			closeErr = err
		}
	}

	select {
	case <-cleanupDone:
		// authClosing and the synchronously canceled authContext prevent handlers
		// that were already inside admission from installing a replacement flow.
	case <-ctx.Done():
		// Preserve the caller's deadline. The first cancellation sweep continues
		// in the background if a bounded Keychain command still owns a commit
		// fence, but report that cleanup did not finish within the contract.
		return errors.Join(shutdownErr, closeErr, ctx.Err())
	}
	return errors.Join(shutdownErr, closeErr)
}

// authOperationContext ties a login-start request to both its HTTP request and
// the Sidecar lifetime. In particular, a handler that passed admission just
// before Shutdown cannot install a pending flow after Shutdown's cancellation
// sweep, even when http.Server.Shutdown reaches its own deadline.
func (s *Server) authOperationContext(parent context.Context) (context.Context, func()) {
	// Make the Sidecar lifetime the direct parent so authCancel closes the
	// operation synchronously. The request is the secondary cancellation source.
	lifetime := s.authContext
	if lifetime == nil {
		lifetime = context.Background()
	}
	operationContext, operationCancel := context.WithCancel(lifetime)
	stopRequestCancellation := context.AfterFunc(parent, operationCancel)
	if parent.Err() != nil {
		operationCancel()
	}
	return operationContext, func() {
		stopRequestCancellation()
		operationCancel()
	}
}

// healthResponse is the shape of GET /health. Matches sidecar-protocol.md §6.1.
type healthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	Build         string `json:"build"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	SQLite        string `json:"sqlite"`
	Auth          string `json:"auth"`
}

func (s *Server) handleHealth(c *gin.Context) {
	resp := healthResponse{
		Status:        "ok",
		Version:       s.cfg.SidecarVersion,
		Build:         "desktop",
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		SQLite:        s.probeSQLite(),
		// Keep /health cheap and side-effect-free; detailed auth state
		// lives in /auth/status and /system/diagnostics.
		Auth: "unauthenticated",
	}
	c.JSON(http.StatusOK, resp)
}

// handleAuthStatus, handleAuthStart, handleAuthLogout +
// authStatusResponse / authStartResponse live in server_auth.go.
// Same package; no behavior change.

// /agent/* handlers (chat / skills-catalog / threads / messages) +
// their types + helpers (runSSEKeepalive, strconvAtoi,
// triggerMessagesSync) live in server_agent.go. Same package; no
// behavior change.

// probeSQLite runs a cheap query to verify the DB connection is alive.
// Returns "ok" or a short error description suitable for /health JSON.
func (s *Server) probeSQLite() string {
	var n int
	if err := s.cfg.DB.Raw("SELECT 1").Scan(&n).Error; err != nil {
		return "error: " + err.Error()
	}
	if n != 1 {
		return "unexpected probe result"
	}
	return "ok"
}

// handleNetworkStateSSE, handleDiagnostics, handleRendererLog +
// their types + helpers (probeAuthState, formatRFC3339OrEmpty) live
// in server_system.go. Same package; no behavior change.
