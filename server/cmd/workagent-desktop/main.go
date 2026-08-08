//go:build desktop

// workagent-desktop is the sidecar binary that runs on a user's machine
// as a child process of the Electron desktop app. It hosts the local HTTP
// server the renderer talks to, manages the local SQLite cache, and proxies
// model calls through to the configured WorkMax Go Server API origin (via
// server/desktop/cloud_proxy).
//
// Build:
//
//	cd server && GOOS=darwin GOARCH=arm64 go build -tags desktop ./cmd/workagent-desktop
//
// Dev:
//
//	cd server && GOOS=darwin GOARCH=arm64 go run -tags desktop ./cmd/workagent-desktop
//
// (Repo go env defaults to linux/amd64 for production cross-compile,
// so local macOS builds must override both vars. desktop/scripts/dev.sh
// will wrap this for normal desktop development.)
//
// Platform design: ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
//
// Boot sequence
//  1. Resolve data dir and acquire sidecar.pid lock
//  2. Open SQLite, apply migrations, seed device_id
//  3. Resolve X-Local-Token from env (or generate)
//  4. Bind HTTP server to 127.0.0.1:0
//  5. Emit single-line JSON handshake on stdout
//  6. Serve until stdin "shutdown" or SIGINT/SIGTERM
//  7. Graceful HTTP/sync shutdown + DB close only after all owners exit
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"server/desktop"
	"server/desktop/buildinfo"
	cloudproxy "server/desktop/cloud_proxy"
	localinference "server/desktop/local_inference"
	localrender "server/desktop/local_render"
	knowledge "server/desktop/knowledge"
	desktopsync "server/desktop/sync"

	"gorm.io/gorm"
)

// sidecarVersion is a local alias so the existing call sites
// (handshake, log line, ServerConfig wiring) read cleanly. The
// authoritative copy lives in server/desktop/buildinfo and is what
// the cloud HTTP layer stamps as X-WorkMax-Client-Version, so the
// two paths cannot diverge.
var sidecarVersion = buildinfo.Version

// LocalTokenEnv is the env var Electron sets when spawning the sidecar.
// If absent (e.g. running the binary by hand for smoke tests), we
// generate an ephemeral token and log it so the developer can curl.
const LocalTokenEnv = "WORKMAX_LOCAL_TOKEN"

// CloudBaseEnv overrides the official hosted Go Server gateway used by the
// sidecar. Self-hosted and local development must set the direct Server/API
// gateway origin (for example http://127.0.0.1:8888). It must include a scheme
// and no trailing slash.
const CloudBaseEnv = "WORKMAX_CLOUD_BASE"

// SkipStdinWatcherEnv disables the stdin-EOF / "shutdown" line watcher.
// Set to any non-empty value when running the binary from a test
// harness that uses /dev/null as stdin (otherwise immediate EOF would
// trigger an instant shutdown). Signals still work in all cases.
const SkipStdinWatcherEnv = "WORKMAX_DESKTOP_SKIP_STDIN_WATCHER"

// SmokeSeedCacheEnv enables a hermetic packaged-app smoke mode. It switches
// the sidecar to an in-process token store and seeds one cached thread/message
// into the configured temp data directory. Production Electron never sets this.
const SmokeSeedCacheEnv = "WORKMAX_DESKTOP_SMOKE_SEED_CACHE"

// gracefulShutdownTimeout caps how long we wait for in-flight requests
// to drain before forcing http.Server to close. Sidecar-protocol §2.2
// recommends 5s.
const gracefulShutdownTimeout = 5 * time.Second

func main() {
	if handleMetaCommand(os.Args[1:]) {
		return
	}

	// All log output goes to stderr; stdout is reserved for the
	// single-line handshake JSON.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	log.SetPrefix("workagent-desktop ")

	log.Printf("starting sidecar v%s", sidecarVersion)

	// 0. Acquire the per-data-dir sidecar lock before touching SQLite.
	// Electron has its own single-instance lock, but this protects
	// standalone sidecar launches and stale child processes from
	// opening the same local cache concurrently.
	dataDir, err := desktop.ResolveDataDir()
	if err != nil {
		log.Fatalf("resolve data dir: %v", err)
	}
	releaseLock, err := acquireSidecarLock(dataDir)
	if err != nil {
		log.Fatalf("acquire sidecar lock: %v", err)
	}
	defer releaseLock()

	// 1. Open SQLite + migrations.
	dbRes, err := desktop.OpenLocalDB()
	if err != nil {
		log.Fatalf("open local db: %v", err)
	}
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

	// 2. Resolve local token
	token, generated, err := resolveLocalToken()
	if err != nil {
		log.Fatalf("resolve local token: %v", err)
	}
	if generated {
		// Visible warning + the actual token so developer can curl.
		// Production Electron always sets the env, so this branch is
		// only hit during standalone smoke tests.
		log.Printf("WARN: %s not set; generated ephemeral token: %s", LocalTokenEnv, token)
		log.Printf("WARN: in production Electron always sets this env var; see desktop/README.md")
	} else {
		log.Printf("local token: inherited from %s (%d chars)", LocalTokenEnv, len(token))
	}

	// 3. Wire OAuth: Keychain → TokenStore → OAuthFlow.
	// The default is the official hosted Go Server gateway. Override it with the
	// direct Server/API gateway origin for self-hosted and local development.
	cloudBase := os.Getenv(CloudBaseEnv)
	if cloudBase == "" {
		cloudBase = "https://workmax.app"
	}
	cloudBase, err = cloudproxy.NormalizeBaseURL(cloudBase)
	if err != nil {
		log.Fatalf("invalid %s: %v", CloudBaseEnv, err)
	}
	log.Printf("cloud base: %s", cloudBase)
	var keychain cloudproxy.Keychain = cloudproxy.NewDarwinKeychain()
	smokeSeedCache := os.Getenv(SmokeSeedCacheEnv) == "1"
	if smokeSeedCache {
		keychain = newSmokeMemoryKeychain()
		log.Printf("smoke cache seed enabled: using in-memory token store")
	}
	tokenStore := newDesktopTokenStore(keychain, dbRes.DB, smokeSeedCache)
	if smokeSeedCache {
		if err := seedSmokeDesktopCache(tokenStore, dbRes.DB); err != nil {
			log.Fatalf("seed smoke desktop cache: %v", err)
		}
	}
	cloudClient := cloudproxy.NewClient(cloudBase)
	oauthFlow := cloudproxy.NewOAuthFlow(cloudClient, tokenStore, dbRes.DeviceID)
	loginCoordinator := cloudproxy.NewLoginTransactionCoordinator(cloudClient, tokenStore, dbRes.DeviceID)

	// Cloud Proxy (chat relay). Shares the same Client + TokenStore as
	// the OAuth flow; the relay's HTTP client has Timeout: 0 since SSE
	// turns live for minutes (configured in NewProxy).
	proxy := cloudproxy.NewProxy(cloudClient, tokenStore, dbRes.DB)

	// Early-access skill allowlist. Pinned to a single skill ("ppt")
	// until each additional mode is validated in the slim Desktop
	// renderer. Log it at startup so a user staring at sidecar stderr
	// can see what's available without reading the source.
	log.Printf("skill allowlist: %v (count=%d)",
		desktop.DesktopAllowedAgentModes, len(desktop.DesktopAllowedAgentModes))

	// Network state watcher. Probes cloudBase every 30s; renderer
	// subscribes via /system/network-state SSE to drive the offline banner
	// + input-disabling UX. Started below (after server bind) so its
	// goroutine inherits the same shutdown ctx as the HTTP server.
	networkProbe := desktop.NewHTTPNetworkProbe(cloudBase)
	networkWatcher := desktop.NewNetworkStateWatcher(networkProbe, 30*time.Second)

	cursorStore := desktopsync.NewCursorStore(dbRes.DB)

	// Shared sidecar lifetime context. All background workers receive
	// this before they are constructed so shutdown cancellation reaches
	// in-flight sync jobs before Drain() waits on their goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// P1.B.3.x.2: MessagesSyncer — on-demand per-thread messages
	// sync. The /agent/threads/:uuid/messages handler triggers a
	// background sync each time the renderer asks; coalesces so
	// rapid polls don't fan out. Built BEFORE the threads SyncWorker
	// because P1.B.3.x.3 wires it as a post-tick trigger from the
	// threads job (every 5min, after threads drain, the N most-
	// recent local threads get periodic message-sync triggers — for
	// users who want everything cached without manually clicking).
	messagesSyncer := desktopsync.NewMessagesSyncer(desktopsync.MessagesSyncerDeps{
		DB:          dbRes.DB,
		Cloud:       cloudClient,
		TokenStore:  tokenStore,
		CursorStore: cursorStore,
		ParentCtx:   ctx,
	})

	// P1.B.3: SyncWorker for threads. Pulls thread deltas from
	// cloud /api/desktop/sync/threads every 5 minutes (or on
	// explicit trigger). The JobFunc reads uid from the access
	// token's JWT claims each tick — so the worker can boot
	// before the user signs in (no-op ticks until token exists).
	//
	// P1.B.3.x.3: MessagesSyncer + RecentThreadsToSync wire the
	// "after threads drain, sync messages for top N threads"
	// periodic path. Per-thread coalesce in the syncer means
	// repeat ticks for the same thread within one sync window
	// collapse — safe.
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

	// Renderer log writer. Persists structured log entries the
	// renderer POSTs to /system/log under <dataDir>/logs/renderer.log
	// (rotated by lumberjack). Without this, renderer-side errors
	// in a packaged Electron build vanish into console.error with
	// no devtools to inspect.
	rendererLogger := desktop.NewRendererLogger(dbRes.DataDir)
	if rendererLogger != nil {
		defer func() {
			if err := rendererLogger.Close(); err != nil {
				log.Printf("renderer logger close: %v", err)
			}
		}()
	}

	// Local / Official model route settings: non-secret fields in SQLite,
	// API key in the same Keychain backend as OAuth tokens (separate account).
	modelSettings := desktop.NewLocalModelSettingsStore(dbRes.DB, keychain)

	// Local inference engine (L1): runs agent turns against the user-configured
	// local model when preferred_route=local. Reads profile + key from the same
	// ModelSettings store the renderer configures via PUT /settings/model-route.
	// Shares the sidecar DB so CacheWriter persists local turns into the same
	// w_workagent_message rows (idempotency key = desktop-turn:<uuid>).
	// Local file attachment store (L3b): persists uploads under
	// <DataDir>/thread_files and writes w_workagent_thread_file metadata.
	// It also implements AttachmentLoader, so the Engine can resolve file_ids
	// into model context.
	localFiles := localrender.NewStore(dbRes.DB, filepath.Join(dbRes.DataDir, "thread_files"))

	// L3c RAG (optional): the local knowledge indexer powers file indexing
	// (L3c-3), conversation memory (L3c-4), and retrieval injection (L3c-5).
	// Best-effort construction: if onnxruntime resources aren't resolvable
	// (dev without resources, or not yet packaged), RAG is disabled and the
	// sidecar runs normally. Enable by pointing WORKMAX_RESOURCES_DIR (or the
	// individual *_PATH env vars in knowledge.ResolveResources) at the assets.
	var (
		knowledgeHooks localinference.KnowledgeHooks
		fileIndexer    desktop.FileIndexer
		emb            *knowledge.Embedder
	)
	if res, rerr := knowledge.ResolveResources(); rerr == nil {
		if e, eerr := knowledge.NewEmbedder(res); eerr == nil {
			if kstore, serr := knowledge.NewStore(dbRes.DB); serr == nil {
				emb = e
				idx := knowledge.NewIndexer(localFiles, emb, kstore)
				knowledgeHooks = idx
				fileIndexer = idx
				log.Printf("knowledge: local RAG enabled (%d-dim embeddings)", emb.Dim())
			} else {
				log.Printf("knowledge: RAG disabled (vector store init: %v)", serr)
			}
		} else {
			log.Printf("knowledge: RAG disabled (embedder init: %v)", eerr)
		}
	} else {
		log.Printf("knowledge: RAG disabled (native resources unresolved: %v)", rerr)
	}
	// Release the ONNX Runtime environment during shutdown, before the Go
	// runtime exits. Otherwise onnxruntime's C++ static destructors abort the
	// process on exit ("mutex lock failed"). In-flight turns have drained by
	// the time main returns (graceful shutdown waits on the HTTP server).
	defer func() {
		if emb != nil {
			_ = emb.Close()
		}
	}()

	localInference := localinference.NewEngine(
		&desktop.LocalModelProfileReader{Store: modelSettings},
		dbRes.DB,
		localFiles,
		knowledgeHooks,
	)

	// 4. Build & bind HTTP server
	srv, err := desktop.NewServer(desktop.ServerConfig{
		SidecarVersion:   sidecarVersion,
		LocalToken:       token,
		DB:               dbRes.DB,
		DeviceID:         dbRes.DeviceID,
		OAuthFlow:        oauthFlow,
		TokenStore:       tokenStore,
		LoginCoordinator: loginCoordinator,
		Proxy:            proxy,
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
		LocalFiles:       localFiles,
		FileIndexer:      fileIndexer,
	})
	if err != nil {
		log.Fatalf("new server: %v", err)
	}
	port := srv.Port()
	log.Printf("HTTP server bound to 127.0.0.1:%d", port)

	// 4. Emit handshake — MUST happen before any other stdout write
	if err := desktop.WriteHandshake(port, sidecarVersion); err != nil {
		log.Fatalf("write handshake: %v", err)
	}

	// 5. Serve & wait for shutdown signal

	// P0.9: start network watcher under the same shutdown ctx so its
	// goroutine winds down with the rest of the process.
	networkWatcher.Start(ctx)
	log.Printf("network state watcher: probe every 30s against %s", cloudBase)

	// P1.B.3: start the threads SyncWorker under the same ctx. The
	// worker fires an immediate "startup" trigger so a fresh-launched
	// (or re-launched-while-already-authenticated) sidecar starts
	// pulling deltas right away. Without an authenticated session
	// the JobFunc returns nil silently; once the user signs in,
	// the next tick (or a Trigger from auth) picks up the work.
	threadsSyncer.Start(ctx)
	log.Printf("threads sync worker: periodic every 5m")

	var shutdownOnce sync.Once
	triggerShutdown := func(reason string) {
		shutdownOnce.Do(func() {
			log.Printf("shutdown initiated: %s", reason)
			cancel()
		})
	}

	if os.Getenv(SkipStdinWatcherEnv) == "" {
		go watchStdinShutdown(triggerShutdown)
	} else {
		log.Printf("stdin watcher disabled via %s; rely on signals for shutdown", SkipStdinWatcherEnv)
	}
	go watchSignals(triggerShutdown)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	select {
	case <-ctx.Done():
		// graceful path
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
	}

	// 6. Graceful shutdown
	shutCtx, shutCancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer shutCancel()
	safeToCloseDB := true
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("http shutdown: %v", err)
		safeToCloseDB = false
	}

	// Wait for in-flight messages-sync goroutines to finish so
	// SQLite writes complete before DB.Close. ParentCtx is the
	// already-cancelled `ctx` from above — in-flight jobs see
	// ctx.Done and exit early; Drain just waits for the wind-down.
	resourceCtx, resourceCancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer resourceCancel()
	if err := messagesSyncer.DrainContext(resourceCtx); err != nil {
		log.Printf("messages syncer did not exit within %s; leaving SQLite open for process-exit cleanup", gracefulShutdownTimeout)
		safeToCloseDB = false
	}

	// Same wind-down rule for the periodic ThreadsSyncer. The
	// worker exposes Done() (close()-on-goroutine-exit); waiting
	// here closes the matching SQLite-WAL race the messages
	// Drain just closed.
	//
	// Bound the wait so a wedged worker can't block process exit
	// indefinitely — the goroutine itself catches panics + honors
	// ctx cancellation, so a multi-second wait should be ample;
	// 5s lines up with the gracefulShutdownTimeout used above.
	select {
	case <-threadsSyncer.Done():
	case <-resourceCtx.Done():
		log.Printf("threads syncer did not exit within %s; abandoning wait", gracefulShutdownTimeout)
		safeToCloseDB = false
	}

	// Do not race DB.Close against an HTTP auth cleanup or sync job that missed
	// its deadline. The process is exiting, so the OS can safely reclaim the
	// descriptor after those goroutines are terminated with the process.
	if safeToCloseDB {
		if sqlDB, err := dbRes.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	} else {
		log.Printf("SQLite close skipped because a shutdown owner may still be active")
	}

	log.Printf("bye")
}

// newDesktopTokenStore keeps hermetic smoke credentials completely detached
// from production session authority. In particular, a smoke Save must never
// clear a durable SQLite tombstone that protects stale real Keychain bytes.
func newDesktopTokenStore(keychain cloudproxy.Keychain, db *gorm.DB, smoke bool) *cloudproxy.TokenStore {
	if smoke {
		return cloudproxy.NewTokenStore(keychain)
	}
	return cloudproxy.NewTokenStoreWithTombstone(
		keychain,
		desktop.NewSQLiteSessionTombstoneMarker(db),
	)
}

type smokeMemoryKeychain struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newSmokeMemoryKeychain() *smokeMemoryKeychain {
	return &smokeMemoryKeychain{data: map[string][]byte{}}
}

func (k *smokeMemoryKeychain) key(service, account string) string {
	return service + "\x00" + account
}

func (k *smokeMemoryKeychain) Write(service, account string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.data[k.key(service, account)] = append([]byte(nil), value...)
	return nil
}

func (k *smokeMemoryKeychain) Read(service, account string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	value, ok := k.data[k.key(service, account)]
	if !ok {
		return nil, cloudproxy.ErrKeychainNoEntry
	}
	return append([]byte(nil), value...), nil
}

func (k *smokeMemoryKeychain) Delete(service, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.data, k.key(service, account))
	return nil
}

func seedSmokeDesktopCache(tokenStore *cloudproxy.TokenStore, db *gorm.DB) error {
	const uid = uint64(42)
	now := time.Now().UTC()
	if err := tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintSmokeAccessToken(uid),
		AccessExpiresAt:  now.Add(time.Hour),
		RefreshToken:     "smoke-refresh-token",
		RefreshExpiresAt: now.Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		return err
	}
	updatedAt := now.Format(time.RFC3339Nano)
	if err := db.Exec(`
		INSERT INTO w_workagent_thread
			(uid, uuid, name, agent_mode, agent_type, message_count, msg_preview, cloud_sync_state, updated_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET
			uid = excluded.uid,
			name = excluded.name,
			agent_mode = excluded.agent_mode,
			agent_type = excluded.agent_type,
			message_count = excluded.message_count,
			msg_preview = excluded.msg_preview,
			cloud_sync_state = excluded.cloud_sync_state,
			updated_at = excluded.updated_at`,
		uid,
		"smoke-cached-thread",
		"Smoke Cached Thread",
		"ppt",
		"general_agent",
		1,
		"Smoke cached assistant answer",
		"synced",
		updatedAt,
		updatedAt,
	).Error; err != nil {
		return err
	}
	var threadID uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, "smoke-cached-thread").Row().Scan(&threadID); err != nil {
		return err
	}
	if err := db.Exec(`
		INSERT INTO w_workagent_message
			(uid, uuid, thread_id, user_text, ai_text, chat_mode, streaming_state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET
			uid = excluded.uid,
			thread_id = excluded.thread_id,
			user_text = excluded.user_text,
			ai_text = excluded.ai_text,
			chat_mode = excluded.chat_mode,
			streaming_state = excluded.streaming_state,
			updated_at = excluded.updated_at`,
		uid,
		"smoke-cached-message",
		threadID,
		"Smoke cached user question",
		"Smoke cached assistant answer",
		"ppt",
		"complete",
		updatedAt,
		updatedAt,
	).Error; err != nil {
		return err
	}
	log.Printf("smoke cache seed: thread=smoke-cached-thread uid=%d", uid)
	return nil
}

func mintSmokeAccessToken(uid uint64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"Id":` + strconv.FormatUint(uid, 10) + `,"exp":9999999999}`))
	signature := base64.RawURLEncoding.EncodeToString([]byte("smoke-signature"))
	return header + "." + payload + "." + signature
}

func handleMetaCommand(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--version", "version":
		fmt.Println(sidecarVersion)
		return true
	case "-h", "--help", "help":
		fmt.Fprintf(os.Stdout, "workagent-desktop %s\n\n", sidecarVersion)
		fmt.Fprintln(os.Stdout, "Usage:")
		fmt.Fprintln(os.Stdout, "  workagent-desktop          start the loopback sidecar")
		fmt.Fprintln(os.Stdout, "  workagent-desktop --version")
		return true
	default:
		return false
	}
}

const sidecarPIDFileName = "sidecar.pid"

// acquireSidecarLock claims <dataDir>/sidecar.pid for this process.
// If the lock points at a live process, startup is refused. If the
// file is stale or corrupt, it is removed and replaced.
func acquireSidecarLock(dataDir string) (func(), error) {
	lockPath := filepath.Join(dataDir, sidecarPIDFileName)
	pid := os.Getpid()
	pidText := strconv.Itoa(pid)

	for {
		f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if _, writeErr := fmt.Fprintln(f, pidText); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("write %s: %w", lockPath, writeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("close %s: %w", lockPath, closeErr)
			}
			return func() {
				releaseSidecarLock(lockPath, pidText)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create %s: %w", lockPath, err)
		}

		existingPID, readErr := readSidecarLockPID(lockPath)
		if readErr == nil && processIsAlive(existingPID) {
			return nil, fmt.Errorf("another sidecar instance is already running (pid %d)", existingPID)
		}
		if readErr != nil {
			log.Printf("removing invalid sidecar lock %s: %v", lockPath, readErr)
		} else {
			log.Printf("removing stale sidecar lock %s for pid %d", lockPath, existingPID)
		}
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale %s: %w", lockPath, removeErr)
		}
	}
}

func readSidecarLockPID(lockPath string) (int, error) {
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, err
	}
	pidText := strings.TrimSpace(string(raw))
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid < 1 {
		return 0, fmt.Errorf("invalid pid %q", pidText)
	}
	return pid, nil
}

func processIsAlive(pid int) bool {
	if pid < 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func releaseSidecarLock(lockPath string, pidText string) {
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("read sidecar lock before release: %v", err)
		}
		return
	}
	if strings.TrimSpace(string(raw)) != pidText {
		log.Printf("not removing sidecar lock %s; owner changed", lockPath)
		return
	}
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove sidecar lock: %v", err)
	}
}

// resolveLocalToken returns either the value of $WORKMAX_LOCAL_TOKEN or a
// freshly generated 32-byte URL-safe random string. The second return
// value indicates whether the token was generated (true) or inherited
// (false) — used only for log clarity.
func resolveLocalToken() (token string, generated bool, err error) {
	if env := os.Getenv(LocalTokenEnv); env != "" {
		return env, false, nil
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, fmt.Errorf("rand.Read: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), true, nil
}

// watchStdinShutdown reads stdin line-by-line. When it sees "shutdown",
// it invokes the shutdown trigger and stops scanning. Closing stdin
// (Electron parent exits abruptly) also ends the scan and triggers
// shutdown — the sidecar should not outlive its parent.
func watchStdinShutdown(trigger func(reason string)) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "shutdown" {
			trigger("stdin command")
			return
		}
		// Any other input is ignored. We don't expect anything else on
		// stdin in P0.3; future commands will go through HTTP.
	}
	// stdin closed (EOF). Treat as parent gone.
	trigger("stdin EOF")
}

// watchSignals listens for SIGINT (Ctrl-C) and SIGTERM. Either triggers
// graceful shutdown.
func watchSignals(trigger func(reason string)) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	sig := <-ch
	trigger(fmt.Sprintf("signal %s", sig))
}
