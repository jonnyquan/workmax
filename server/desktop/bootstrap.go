//go:build desktop

// Bootstrap is the single shared startup path for every WorkMax Desktop
// mode of the Wails binary (desktop/wails): windowed, --serve-only, and the
// verification harnesses.
//
// The wiring lived in cmd/workagent-desktop/main.go until the Wails migration
// needed a second caller. Keeping one copy is the point: two entry points that
// each assembled OpenLocalDB → keychain → proxy → syncers → server would drift
// within a release, and the drift would be invisible until a user hit the one
// subsystem the other entry point forgot to start.
//
// What Bootstrap deliberately does NOT do, so callers stay in control:
//   - it does not serve (mirrors NewServer: bind now, serve after the caller
//     has emitted whatever handshake its transport needs);
//   - it does not install signal handlers or window hooks (--serve-only wants
//     signals, the windowed mode wants OnBeforeClose — neither belongs here);
//   - it does not seed smoke fixtures (that stays in the entry point that
//     owns the smoke contract).
//
// Design doc: ProjectDocs/wails-migration-evaluation-2026-08.md §10.
package desktop

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	agentruntime "server/desktop/agentruntime"
	"sync"
	"time"

	"gorm.io/gorm"

	"server/desktop/buildinfo"
	cloudproxy "server/desktop/cloud_proxy"
	localagent "server/desktop/local_agent"
	localinference "server/desktop/local_inference"
	localrender "server/desktop/local_render"
	piagent "server/desktop/pi_agent"
	desktopsync "server/desktop/sync"
)

// LocalTokenEnv is the env var the desktop shell sets when spawning the
// sidecar out-of-process. If absent (running the binary by hand for smoke
// tests, or the in-process Wails boot), Bootstrap generates an ephemeral
// token and logs it so a developer can curl.
const LocalTokenEnv = "WORKMAX_LOCAL_TOKEN"

// CloudBaseEnv overrides the official hosted Go Server gateway used by the
// sidecar. Self-hosted and local development must set the direct Server/API
// gateway origin (for example http://127.0.0.1:8888). It must include a
// scheme and no trailing slash.
const CloudBaseEnv = "WORKMAX_CLOUD_BASE"

// DefaultCloudBase is the hosted gateway used when CloudBaseEnv is unset.
const DefaultCloudBase = "https://workmax.app"

// GracefulShutdownTimeout caps how long Shutdown waits for in-flight requests
// to drain before forcing http.Server to close. Sidecar-protocol §2.2
// recommends 5s.
const GracefulShutdownTimeout = 5 * time.Second

// BootstrapConfig lets each entry point vary the handful of decisions that
// genuinely differ between them. Every field is optional; the zero value
// boots the production configuration.
type BootstrapConfig struct {
	// SidecarVersion is stamped into diagnostics and the cloud client's
	// X-WorkMax-Client-Version header. Empty → buildinfo.Version.
	SidecarVersion string

	// LocalToken is the X-Local-Token every loopback request must carry.
	// Empty → inherited from LocalTokenEnv, or freshly generated.
	//
	// The Wails boot passes this in-process rather than through the
	// environment: there is no child process to hand an env var to, and
	// keeping it out of the environment keeps it out of `ps -E` output.
	LocalToken string

	// CloudBase overrides the cloud gateway origin. Empty → CloudBaseEnv,
	// then DefaultCloudBase.
	CloudBase string

	// Keychain backs the OAuth token store and the local model API key.
	// Nil → the platform keychain. Smoke harnesses inject an in-memory one.
	Keychain cloudproxy.Keychain

	// DisableSessionTombstone drops the SQLite tombstone marker from the
	// token store. Only hermetic smoke boots want this: a smoke Save must
	// never clear a durable tombstone that protects stale real Keychain
	// bytes. Production always keeps the tombstone.
	DisableSessionTombstone bool

	// SkipSidecarLock skips the <dataDir>/sidecar.pid lock. Intended for
	// harnesses that already guarantee exclusivity. Production entry points
	// keep the lock: it is what stops two processes opening one SQLite file.
	SkipSidecarLock bool

	// ResourcesDir is the root of the packaged native assets (the ONNX
	// Runtime shared library, the embedding model and its tokenizer). The
	// Wails boot derives it from the executable path (Contents/Resources on
	// macOS); the out-of-process sidecar inherits WORKMAX_RESOURCES_DIR.
	// Empty → env, then "resources" relative to the working directory.
	//
	// Ignored when the binary is built without cgo: RAG is off and the
	// sidecar boots normally. See bootstrap_cgo.go.
	ResourcesDir string
}

// Boot is everything a caller needs to run and then tear down a desktop
// sidecar. The exported fields are the subsystems an entry point legitimately
// reaches into (the Wails LoginAPI binding needs LoginCoordinator; smoke
// seeding needs TokenStore and DB); the unexported ones are shutdown state.
type Boot struct {
	Server     *Server
	LocalToken string
	// TokenGenerated reports whether LocalToken was minted here rather than
	// inherited from the environment. Callers log a warning when true.
	TokenGenerated bool

	DataDir  string
	DBPath   string
	DB       *gorm.DB
	DeviceID string

	CloudBase        string
	TokenStore       *cloudproxy.TokenStore
	OAuthFlow        *cloudproxy.OAuthFlow
	LoginCoordinator *cloudproxy.LoginTransactionCoordinator
	Proxy            *cloudproxy.Proxy

	MessagesSyncer *desktopsync.MessagesSyncer
	ThreadsSyncer  *desktopsync.SyncWorker
	NetworkWatcher *NetworkStateWatcher
	RendererLogger *RendererLogger
	ModelSettings  *LocalModelSettingsStore
	LocalFiles     *localrender.Store

	// RAGEnabled reports whether the local knowledge stack is wired in: the
	// vector store opened and passed its self-check. False on a non-cgo build
	// or when sqlite-vec is unusable. True does not promise a query will
	// return anything today — the embedding model is acquired and loaded on
	// first use, and may still be downloading. That distinction is why the
	// boot log reports the asset state separately.
	RAGEnabled bool

	ctx           context.Context
	cancel        context.CancelFunc
	releaseLock   func()
	closeEmbedder func() error
	startOnce     sync.Once

	// cancelOnce guards only the "shutdown initiated" log line. It is
	// deliberately NOT the same Once that guards the teardown: Cancel is
	// normally called first (by a signal handler or a window-close hook),
	// and a shared Once would let that call consume it so the real teardown
	// in Shutdown silently never runs — which leaks the ONNX Runtime
	// environment and aborts the process on exit.
	cancelOnce sync.Once

	shutdownMu   sync.Mutex
	shutdownDone bool
	shutdownErr  error
}

// Bootstrap runs the full sidecar wiring and binds the loopback HTTP server.
// It does not serve; call Start then Serve (or ServeUntilContextDone).
//
// On error every resource acquired so far is released, so a failed Bootstrap
// leaves no lock file and no open SQLite handle behind.
func Bootstrap(cfg BootstrapConfig) (_ *Boot, err error) {
	version := cfg.SidecarVersion
	if version == "" {
		version = buildinfo.Version
	}

	// Unwind whatever we already acquired if a later step fails. Without
	// this an error after acquireSidecarLock would strand sidecar.pid and
	// the next launch would refuse to start.
	var unwind []func()
	defer func() {
		if err == nil {
			return
		}
		for i := len(unwind) - 1; i >= 0; i-- {
			unwind[i]()
		}
	}()

	// 0. Acquire the per-data-dir sidecar lock before touching SQLite. The
	// desktop shell has its own single-instance lock, but this protects
	// standalone launches and stale child processes from opening the same
	// local cache concurrently.
	dataDir, err := ResolveDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	releaseLock := func() {}
	if !cfg.SkipSidecarLock {
		release, lockErr := AcquireSidecarLock(dataDir)
		if lockErr != nil {
			return nil, fmt.Errorf("acquire sidecar lock: %w", lockErr)
		}
		releaseLock = release
		unwind = append(unwind, releaseLock)
	}

	// 1. Open SQLite + migrations.
	dbRes, err := OpenLocalDB()
	if err != nil {
		return nil, fmt.Errorf("open local db: %w", err)
	}
	unwind = append(unwind, func() {
		if sqlDB, dbErr := dbRes.DB.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	logOpenResult(dbRes)

	// 2. Resolve the local token.
	token, generated := cfg.LocalToken, false
	if token == "" {
		token, generated, err = ResolveLocalToken()
		if err != nil {
			return nil, fmt.Errorf("resolve local token: %w", err)
		}
	}

	// 3. Wire OAuth: Keychain → TokenStore → OAuthFlow.
	// The default is the official hosted Go Server gateway. Override it with
	// the direct Server/API gateway origin for self-hosted development.
	cloudBase := cfg.CloudBase
	if cloudBase == "" {
		cloudBase = os.Getenv(CloudBaseEnv)
	}
	if cloudBase == "" {
		cloudBase = DefaultCloudBase
	}
	cloudBase, err = cloudproxy.NormalizeBaseURL(cloudBase)
	if err != nil {
		return nil, fmt.Errorf("invalid cloud base %q: %w", cloudBase, err)
	}
	log.Printf("cloud base: %s", cloudBase)

	keychain := cfg.Keychain
	if keychain == nil {
		keychain = cloudproxy.NewDarwinKeychain()
	}
	var tokenStore *cloudproxy.TokenStore
	if cfg.DisableSessionTombstone {
		tokenStore = cloudproxy.NewTokenStore(keychain)
	} else {
		tokenStore = cloudproxy.NewTokenStoreWithTombstone(
			keychain,
			NewSQLiteSessionTombstoneMarker(dbRes.DB),
		)
	}

	cloudClient := cloudproxy.NewClient(cloudBase)
	oauthFlow := cloudproxy.NewOAuthFlow(cloudClient, tokenStore, dbRes.DeviceID)
	loginCoordinator := cloudproxy.NewLoginTransactionCoordinator(cloudClient, tokenStore, dbRes.DeviceID)

	// Cloud Proxy (chat relay). Shares the same Client + TokenStore as the
	// OAuth flow; the relay's HTTP client has Timeout: 0 since SSE turns live
	// for minutes (configured in NewProxy).
	proxy := cloudproxy.NewProxy(cloudClient, tokenStore, dbRes.DB)

	// Early-access skill allowlist. Logged at startup so a user staring at
	// sidecar stderr can see what's available without reading the source.
	log.Printf("skill allowlist: %v (count=%d)", DesktopAllowedAgentModes, len(DesktopAllowedAgentModes))

	// Network state watcher. Probes cloudBase every 30s; the renderer
	// subscribes via /system/network-state SSE to drive the offline banner.
	// Started by Start() so its goroutine shares the shutdown ctx.
	networkWatcher := NewNetworkStateWatcher(NewHTTPNetworkProbe(cloudBase), 30*time.Second)

	cursorStore := desktopsync.NewCursorStore(dbRes.DB)

	// Shared sidecar lifetime context. All background workers receive this
	// before they are constructed so shutdown cancellation reaches in-flight
	// sync jobs before Drain waits on their goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	unwind = append(unwind, cancel)

	// MessagesSyncer — on-demand per-thread messages sync. The
	// /agent/threads/:uuid/messages handler triggers a background sync each
	// time the renderer asks; coalesces so rapid polls don't fan out. Built
	// BEFORE the threads SyncWorker because the threads job wires it as a
	// post-tick trigger.
	messagesSyncer := desktopsync.NewMessagesSyncer(desktopsync.MessagesSyncerDeps{
		DB:          dbRes.DB,
		Cloud:       cloudClient,
		TokenStore:  tokenStore,
		CursorStore: cursorStore,
		ParentCtx:   ctx,
	})

	// SyncWorker for threads. Pulls thread deltas every 5 minutes (or on
	// explicit trigger). The JobFunc reads uid from the access token's JWT
	// claims each tick — so the worker can boot before the user signs in
	// (no-op ticks until a token exists).
	threadsJob := desktopsync.NewThreadsJob(desktopsync.ThreadsJobDeps{
		DB:                  dbRes.DB,
		Cloud:               cloudClient,
		TokenStore:          tokenStore,
		CursorStore:         cursorStore,
		MessagesSyncer:      messagesSyncer,
		RecentThreadsToSync: 20, // top 20 by recency; bounded fan-out
	})
	threadsSyncer := desktopsync.NewSyncWorker(threadsJob, desktopsync.Config{
		PeriodicInterval: 5 * time.Minute, // cloud-sync.md §4
		Name:             "threads",       // prefix for [sync-stuck:threads] WARN lines
	})

	// Renderer log writer. Persists structured entries the renderer POSTs to
	// /system/log under <dataDir>/logs/renderer.log. Without this,
	// renderer-side errors in a packaged build vanish with no devtools.
	rendererLogger := NewRendererLogger(dbRes.DataDir)
	if rendererLogger != nil {
		unwind = append(unwind, func() { _ = rendererLogger.Close() })
	}

	// Local / Official model route settings: non-secret fields in SQLite,
	// API key in the same Keychain backend as OAuth tokens (separate account).
	modelSettings := NewLocalModelSettingsStore(dbRes.DB, keychain)

	// Local file attachment store (L3b): persists uploads under
	// <DataDir>/thread_files and writes w_workagent_thread_file metadata. It
	// also implements AttachmentLoader, so the Engine can resolve file_ids
	// into model context.
	localFiles := localrender.NewStore(dbRes.DB, filepath.Join(dbRes.DataDir, "thread_files"))

	// L3c RAG. Best-effort: a build without cgo, or a build whose native
	// resources are unresolvable, runs with RAG off rather than refusing to
	// boot. See bootstrap_cgo.go for the cgo-only construction.
	//
	// The default lives under the data dir rather than the working directory:
	// these assets are downloaded per-user on first use, and a packaged app's
	// working directory is wherever the OS launched it from — usually "/".
	resourcesDir := cfg.ResourcesDir
	if resourcesDir == "" {
		resourcesDir = filepath.Join(dbRes.DataDir, "resources")
	}
	knowledge := resolveKnowledge(KnowledgeDeps{
		DB:           dbRes.DB,
		Files:        localFiles,
		ResourcesDir: resourcesDir,
		DataDir:      dbRes.DataDir,
	})
	if knowledge.Close != nil {
		unwind = append(unwind, func() { _ = knowledge.Close() })
	}

	// Local inference engine (L1): runs agent turns against the
	// user-configured local model when preferred_route=local. Shares the
	// sidecar DB so CacheWriter persists local turns into the same
	// w_workagent_message rows (idempotency key = desktop-turn:<uuid>).
	localInference := localinference.NewEngine(
		&LocalModelProfileReader{Store: modelSettings},
		dbRes.DB,
		localFiles,
		knowledge.Hooks,
	)

	// Local agent engine (L2): the tool loop, for anthropic_compatible local
	// models. Gated on a claude CLI being named explicitly — L2d grows the
	// packaged download; until then the operator points at a binary. No CLI
	// simply means anthropic_compatible turns run as L1 pure chat.
	var localAgent TurnRunner
	// Interactive approvals stay behind a flag until the renderer's approval
	// card ships: without the card, every write would sit on the 120s timeout
	// and then be denied. One broker serves both engines (claude and pi), so
	// session grants and the approve endpoint are shared.
	var approvalBroker *agentruntime.ApprovalBroker
	if os.Getenv("WORKMAX_L2_APPROVALS") == "1" {
		approvalBroker = agentruntime.NewApprovalBroker()
		log.Printf("local agent: interactive tool approvals ON")
	}
	if cliPath := os.Getenv("WORKMAX_CLAUDE_CLI_PATH"); cliPath != "" {
		if info, statErr := os.Stat(cliPath); statErr != nil || info.IsDir() {
			log.Printf("local agent: WORKMAX_CLAUDE_CLI_PATH=%q is not a usable binary (%v); tool loop disabled", cliPath, statErr)
		} else {
			engine := localagent.NewEngine(
				&LocalModelProfileReader{Store: modelSettings},
				dbRes.DB,
				localFiles,
				knowledge.Hooks,
				cliPath,
				filepath.Join(dbRes.DataDir, "agent_workspace"),
			)
			if approvalBroker != nil {
				engine.EnableApprovals(approvalBroker)
			}
			localAgent = engine
			log.Printf("local agent: tool loop available (cli=%s)", cliPath)
		}
	}

	// Pi agent engine (L2, second runtime): the tool loop for
	// openai_compatible local models, driven over `pi --mode rpc`. Same
	// gating pattern as the claude block: an explicitly named binary until
	// packaging lands; no binary simply means openai_compatible turns run
	// as L1 pure chat.
	var piAgent TurnRunner
	if piPath := os.Getenv("WORKMAX_PI_PATH"); piPath != "" {
		if info, statErr := os.Stat(piPath); statErr != nil || info.IsDir() {
			log.Printf("pi agent: WORKMAX_PI_PATH=%q is not a usable binary (%v); pi tool loop disabled", piPath, statErr)
		} else {
			piEngine := localagent.NewEngineWithRuntime(
				&LocalModelProfileReader{Store: modelSettings},
				dbRes.DB,
				localFiles,
				knowledge.Hooks,
				piagent.NewRuntime(piagent.Config{
					BinPath:       piPath,
					DataDir:       dbRes.DataDir,
					WorkspaceRoot: filepath.Join(dbRes.DataDir, "agent_workspace"),
				}),
			)
			if approvalBroker != nil {
				// Same broker as the claude engine: one approval UI, one set
				// of session/always grants, whichever runtime asks.
				piEngine.EnableApprovals(approvalBroker)
			}
			piAgent = piEngine
			log.Printf("pi agent: tool loop available (pi=%s)", piPath)
		}
	}

	// 4. Build & bind the HTTP server.
	srv, err := NewServer(ServerConfig{
		SidecarVersion:   version,
		LocalToken:       token,
		DB:               dbRes.DB,
		DeviceID:         dbRes.DeviceID,
		OAuthFlow:        oauthFlow,
		TokenStore:       tokenStore,
		LoginCoordinator: loginCoordinator,
		Proxy:            proxy,
		Approvals:        approvalBroker,
		NetworkState:     networkWatcher,
		MessagesSyncer:   messagesSyncer,
		ThreadsSyncer:    threadsSyncer,
		RendererLogger:   rendererLogger,
		DataDir:          dbRes.DataDir,
		DBPath:           dbRes.DBPath,
		BackupPath:       dbRes.BackupPath,
		IntegrityCheck:   dbRes.IntegrityCheck,
		ModelSettings:    modelSettings,
		LocalInference:   localInference,
		LocalAgent:       localAgent,
		PiAgent:          piAgent,
		LocalFiles:       localFiles,
		KnowledgeIndex:   knowledge.Index,
	})
	if err != nil {
		return nil, fmt.Errorf("new server: %w", err)
	}
	log.Printf("HTTP server bound to 127.0.0.1:%d", srv.Port())

	return &Boot{
		Server:           srv,
		LocalToken:       token,
		TokenGenerated:   generated,
		DataDir:          dbRes.DataDir,
		DBPath:           dbRes.DBPath,
		DB:               dbRes.DB,
		DeviceID:         dbRes.DeviceID,
		CloudBase:        cloudBase,
		TokenStore:       tokenStore,
		OAuthFlow:        oauthFlow,
		LoginCoordinator: loginCoordinator,
		Proxy:            proxy,
		MessagesSyncer:   messagesSyncer,
		ThreadsSyncer:    threadsSyncer,
		NetworkWatcher:   networkWatcher,
		RendererLogger:   rendererLogger,
		ModelSettings:    modelSettings,
		LocalFiles:       localFiles,
		RAGEnabled:       knowledge.Hooks != nil,
		ctx:              ctx,
		cancel:           cancel,
		releaseLock:      releaseLock,
		closeEmbedder:    knowledge.Close,
	}, nil
}

// Port is the OS-assigned loopback port the server bound to.
func (b *Boot) Port() int { return b.Server.Port() }

// Context is the sidecar lifetime context. It is cancelled by Cancel or
// Shutdown; background workers started outside Boot should honor it.
func (b *Boot) Context() context.Context { return b.ctx }

// Done closes when shutdown has been initiated. Entry points select on this
// alongside their own transport-specific shutdown triggers.
func (b *Boot) Done() <-chan struct{} { return b.ctx.Done() }

// Cancel initiates shutdown without waiting for it. Safe to call repeatedly
// and from any goroutine; only the first call logs.
func (b *Boot) Cancel(reason string) {
	b.cancelOnce.Do(func() {
		log.Printf("shutdown initiated: %s", reason)
	})
	b.cancel()
}

// Start launches the background workers under the sidecar lifetime context.
// Idempotent. Call it before Serve.
func (b *Boot) Start() {
	b.startOnce.Do(func() {
		if b.NetworkWatcher != nil {
			b.NetworkWatcher.Start(b.ctx)
			log.Printf("network state watcher: probe every 30s against %s", b.CloudBase)
		}

		// The worker fires an immediate "startup" trigger so a
		// freshly-launched (or relaunched-while-authenticated) sidecar starts
		// pulling deltas right away. Without an authenticated session the
		// JobFunc returns nil silently.
		if b.ThreadsSyncer != nil {
			b.ThreadsSyncer.Start(b.ctx)
			log.Printf("threads sync worker: periodic every 5m")
		}
	})
}

// Serve blocks until the HTTP server exits. Mirrors http.Server.Serve:
// returns http.ErrServerClosed on graceful shutdown.
func (b *Boot) Serve() error { return b.Server.Serve() }

// ServeUntilContextDone starts the workers, serves, and returns once either
// the lifetime context is cancelled or the server stops on its own. It does
// NOT shut down — the caller owns Shutdown so it can bound the timeout and
// order it against its own teardown.
//
// This is the whole body of the --serve-only mode and of the legacy sidecar's
// serve loop; both differ only in what cancels the context.
func (b *Boot) ServeUntilContextDone() error {
	b.Start()
	serveErr := make(chan error, 1)
	go func() { serveErr <- b.Serve() }()

	select {
	case <-b.ctx.Done():
		return nil
	case err := <-serveErr:
		return err
	}
}

// Shutdown performs the ordered teardown: cancel → HTTP drain → syncer drain
// → embedder release → renderer log flush → SQLite close → lock release.
//
// The ordering is load-bearing, not stylistic:
//   - syncers drain before SQLite closes, or a late sync write races a closed
//     handle against the WAL;
//   - the ONNX embedder is released before the process exits, or onnxruntime's
//     C++ static destructors abort with "mutex lock failed";
//   - SQLite is closed only if every owner actually wound down — an exiting
//     process lets the OS reclaim the descriptor, which is strictly safer
//     than racing a wedged goroutine.
//
// Idempotent: a concurrent caller blocks until the teardown finishes and then
// receives the same result, rather than racing ahead as if shutdown were done.
func (b *Boot) Shutdown(ctx context.Context) error {
	b.shutdownMu.Lock()
	defer b.shutdownMu.Unlock()
	if b.shutdownDone {
		return b.shutdownErr
	}
	b.shutdownDone = true
	b.shutdownErr = b.shutdown(ctx)
	return b.shutdownErr
}

func (b *Boot) shutdown(ctx context.Context) error {
	b.cancel()

	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	safeToCloseDB := true

	shutCtx, shutCancel := context.WithTimeout(context.Background(), GracefulShutdownTimeout)
	defer shutCancel()
	if deadline, ok := ctx.Deadline(); ok {
		var override context.CancelFunc
		shutCtx, override = context.WithDeadline(context.Background(), deadline)
		defer override()
	}
	if b.Server != nil {
		if err := b.Server.Shutdown(shutCtx); err != nil {
			log.Printf("http shutdown: %v", err)
			safeToCloseDB = false
			note(fmt.Errorf("http shutdown: %w", err))
		}
	}

	// Wait for in-flight sync goroutines so SQLite writes complete before
	// DB.Close. The lifetime ctx is already cancelled — in-flight jobs see
	// ctx.Done and exit early; the drain just waits for the wind-down.
	resourceCtx, resourceCancel := context.WithTimeout(context.Background(), GracefulShutdownTimeout)
	defer resourceCancel()
	if b.MessagesSyncer != nil {
		if err := b.MessagesSyncer.DrainContext(resourceCtx); err != nil {
			log.Printf("messages syncer did not exit within %s; leaving SQLite open for process-exit cleanup", GracefulShutdownTimeout)
			safeToCloseDB = false
			note(fmt.Errorf("drain messages syncer: %w", err))
		}
	}

	// Same wind-down rule for the periodic ThreadsSyncer. Bounded so a wedged
	// worker can't block process exit indefinitely.
	if b.ThreadsSyncer != nil {
		select {
		case <-b.ThreadsSyncer.Done():
		case <-resourceCtx.Done():
			log.Printf("threads syncer did not exit within %s; abandoning wait", GracefulShutdownTimeout)
			safeToCloseDB = false
			note(fmt.Errorf("drain threads syncer: %w", resourceCtx.Err()))
		}
	}

	// Release the ONNX Runtime environment before the Go runtime exits.
	// Boot owns this rather than the caller's defer: under Wails the control
	// flow after app.Run() belongs to the framework, so a caller-side defer
	// is not guaranteed to run.
	if b.closeEmbedder != nil {
		if err := b.closeEmbedder(); err != nil {
			log.Printf("knowledge embedder close: %v", err)
			note(fmt.Errorf("close embedder: %w", err))
		}
	}

	if b.RendererLogger != nil {
		if err := b.RendererLogger.Close(); err != nil {
			log.Printf("renderer logger close: %v", err)
			note(fmt.Errorf("close renderer logger: %w", err))
		}
	}

	if safeToCloseDB && b.DB != nil {
		if sqlDB, err := b.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	} else if !safeToCloseDB {
		log.Printf("SQLite close skipped because a shutdown owner may still be active")
	}

	if b.releaseLock != nil {
		b.releaseLock()
	}

	return firstErr
}

// ResolveLocalToken returns either the value of $WORKMAX_LOCAL_TOKEN or a
// freshly generated 32-byte URL-safe random string. The second return value
// reports whether the token was generated (true) or inherited (false).
func ResolveLocalToken() (token string, generated bool, err error) {
	if env := os.Getenv(LocalTokenEnv); env != "" {
		return env, false, nil
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, fmt.Errorf("rand.Read: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), true, nil
}

func logOpenResult(dbRes *OpenResult) {
	log.Printf("data dir: %s", dbRes.DataDir)
	log.Printf("sqlite db: %s", dbRes.DBPath)
	log.Printf("sqlite integrity_check: %s", dbRes.IntegrityCheck)
	log.Printf("sqlite backup: %s", dbRes.BackupPath)
	log.Printf("device_id: %s", dbRes.DeviceID)
	if dbRes.FirstLaunch {
		log.Printf("first launch — device_id generated")
	}
	if len(dbRes.NewMigrations) > 0 {
		log.Printf("applied %d new migration(s): %v", len(dbRes.NewMigrations), dbRes.NewMigrations)
	} else {
		log.Printf("migrations up-to-date")
	}
	if dbRes.AbandonedStreamingMessages > 0 {
		log.Printf("marked %d abandoned streaming message(s) partial", dbRes.AbandonedStreamingMessages)
	}
	if dbRes.InterruptedAgentTurnIntents > 0 {
		log.Printf("marked %d legacy agent turn intent(s) interrupted", dbRes.InterruptedAgentTurnIntents)
	}
}
