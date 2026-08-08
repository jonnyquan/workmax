//go:build desktop

package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

// server_system.go houses handlers under /system/* — the
// ops-facing surface area. Split out of server.go to keep each
// file scoped to one concern; the same package = same struct
// access, no behavior change.
//
// Endpoints:
//   - GET  /system/network-state   — SSE stream of network state
//   - POST /system/log             — renderer-side log persistence
//   - GET  /system/diagnostics     — read-only aggregate snapshot

// handleNetworkStateSSE streams network_state events to the renderer.
// The first event is the current snapshot (so a late-subscribing
// renderer sees state immediately, not just future deltas); subsequent
// events fire only when state actually changes (LastProbeAt updates
// without state changes ARE sent so the renderer can show "last
// probed at X" diagnostics if it wants — but the renderer is free to
// ignore those).
//
// The connection lives for the duration of the renderer's interest.
// Cleanly cancellable: renderer closes the EventSource → ctx done →
// we Unsubscribe + return. A keepalive comment is sent periodically
// so idle streams stay observable and future proxies do not time out
// the connection.
var networkStateSSEKeepaliveInterval = 30 * time.Second

func (s *Server) handleNetworkStateSSE(c *gin.Context) {
	if s.cfg.NetworkState == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "network state watcher not configured"})
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		// Should be impossible — gin's writer always implements Flusher.
		return
	}
	flusher.Flush() // commit headers immediately

	sub := s.cfg.NetworkState.Subscribe()
	defer s.cfg.NetworkState.Unsubscribe(sub)

	keepalive := time.NewTicker(networkStateSSEKeepaliveInterval)
	defer keepalive.Stop()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := io.WriteString(c.Writer, ": keepalive\n\n"); err != nil {
				return // renderer disconnected mid-write
			}
			flusher.Flush()
		case ev, ok := <-sub:
			if !ok {
				return // watcher stopped (process shutdown)
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				// Should be impossible for our struct; if it does
				// happen, give up the stream rather than panic.
				return
			}
			if _, err := fmt.Fprintf(c.Writer, "event: network_state\ndata: %s\n\n", payload); err != nil {
				return // renderer disconnected mid-write
			}
			flusher.Flush()
		}
	}
}

// diagnosticsResponse is the wire shape of GET /system/diagnostics.
// Read-only ops snapshot: aggregates the per-subsystem state that
// would otherwise require N endpoint hops to collect. Useful for
// support (ask user to curl this), dev (build a Diagnostics panel
// in the renderer), and CI smoke (verify the sidecar wires up).
//
// Stable field shape: this is consumed by ops scripts + (future)
// renderer dev panels, so renames are wire-shape changes. Add new
// fields rather than reshape existing ones.
type diagnosticsResponse struct {
	Sidecar       diagnosticsSidecar       `json:"sidecar"`
	ThreadsSyncer diagnosticsSyncerBlock   `json:"threads_syncer"`
	MessagesSync  diagnosticsMessagesBlock `json:"messages_sync"`
	NetworkState  diagnosticsNetworkBlock  `json:"network_state"`
	Auth          diagnosticsAuthBlock     `json:"auth"`
}

type diagnosticsSidecar struct {
	Version           string   `json:"version"`
	UptimeSeconds     int64    `json:"uptime_seconds"`
	HeapAllocBytes    uint64   `json:"heap_alloc_bytes"`
	HeapSysBytes      uint64   `json:"heap_sys_bytes"`
	NumGoroutine      int      `json:"num_goroutine"`
	DataDir           string   `json:"data_dir,omitempty"`
	DBPath            string   `json:"db_path,omitempty"`
	BackupPath        string   `json:"backup_path,omitempty"`
	IntegrityCheck    string   `json:"integrity_check,omitempty"`
	AppliedMigrations []string `json:"applied_migrations,omitempty"`
}

type diagnosticsSyncerBlock struct {
	Configured      bool   `json:"configured"`
	Running         bool   `json:"running,omitempty"`
	ConsecutiveFail int    `json:"consecutive_fail,omitempty"`
	LastTickAt      string `json:"last_tick_at,omitempty"` // RFC3339, empty = never
	LastError       string `json:"last_error,omitempty"`
	TotalTicks      int64  `json:"total_ticks,omitempty"`
	TotalFails      int64  `json:"total_fails,omitempty"`
	LastDurationMs  int64  `json:"last_duration_ms,omitempty"`
}

type diagnosticsMessagesBlock struct {
	Configured     bool  `json:"configured"`
	ActiveThreads  int   `json:"active_threads"`
	TotalTriggered int64 `json:"total_triggered"`
}

type diagnosticsNetworkBlock struct {
	Configured  bool   `json:"configured"`
	State       string `json:"state,omitempty"`         // "probing" | "online" | "offline"
	LastProbeAt string `json:"last_probe_at,omitempty"` // RFC3339
}

type diagnosticsAuthBlock struct {
	Configured       bool   `json:"configured"`
	State            string `json:"state,omitempty"`             // "authenticated" | "unauthenticated" | "expired"
	PersistenceState string `json:"persistence_state,omitempty"` // "ok" | "degraded" | "unavailable"
}

// handleDiagnostics aggregates per-subsystem state into one
// snapshot. Every block has a `configured` flag so the response
// shape is stable whether or not a subsystem is wired; consumers
// can render "—" for unconfigured blocks rather than branching
// on missing JSON keys.
//
// No auth beyond the X-Local-Token check the loopback middleware
// already enforces — same trust model as /health.
func (s *Server) handleDiagnostics(c *gin.Context) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	resp := diagnosticsResponse{
		Sidecar: diagnosticsSidecar{
			Version:           s.cfg.SidecarVersion,
			UptimeSeconds:     int64(time.Since(s.startedAt).Seconds()),
			HeapAllocBytes:    mem.HeapAlloc,
			HeapSysBytes:      mem.HeapSys,
			NumGoroutine:      runtime.NumGoroutine(),
			DataDir:           s.cfg.DataDir,
			DBPath:            s.cfg.DBPath,
			BackupPath:        s.cfg.BackupPath,
			IntegrityCheck:    s.cfg.IntegrityCheck,
			AppliedMigrations: s.appliedMigrations(),
		},
	}

	if s.cfg.ThreadsSyncer != nil {
		snap := s.cfg.ThreadsSyncer.Snapshot()
		resp.ThreadsSyncer = diagnosticsSyncerBlock{
			Configured:      true,
			Running:         snap.Running,
			ConsecutiveFail: snap.ConsecutiveFail,
			LastTickAt:      formatRFC3339OrEmpty(snap.LastTickAt),
			LastError:       redactDiagnosticsString(snap.LastError),
			TotalTicks:      snap.TotalTicks,
			TotalFails:      snap.TotalFails,
			LastDurationMs:  snap.LastDuration.Milliseconds(),
		}
	}

	if s.cfg.MessagesSyncer != nil {
		resp.MessagesSync = diagnosticsMessagesBlock{
			Configured:     true,
			ActiveThreads:  s.cfg.MessagesSyncer.ActiveCount(),
			TotalTriggered: s.cfg.MessagesSyncer.TotalTriggered(),
		}
	}

	if s.cfg.NetworkState != nil {
		snap := s.cfg.NetworkState.Snapshot()
		resp.NetworkState = diagnosticsNetworkBlock{
			Configured:  true,
			State:       string(snap.State),
			LastProbeAt: formatRFC3339OrEmpty(snap.LastProbeAt),
		}
	}

	if s.cfg.TokenStore != nil {
		resp.Auth = s.probeAuthDiagnostics()
	}

	c.JSON(http.StatusOK, resp)
}

// appliedMigrations reads the _schema_migrations table (managed by
// the migrations_desktop runner) and returns the versions in
// applied-order. Errors and a missing table both return nil — the
// diagnostics endpoint surfaces an empty list rather than 500ing
// the whole snapshot for a non-critical field.
//
// Surfaced via /system/diagnostics so support can answer
// "what schema version is this user on?" without a SQLite shell.
func (s *Server) appliedMigrations() []string {
	if s.cfg.DB == nil {
		return nil
	}
	var versions []string
	if err := s.cfg.DB.Raw(
		`SELECT version FROM _schema_migrations ORDER BY applied_at ASC, version ASC`,
	).Scan(&versions).Error; err != nil {
		return nil
	}
	return versions
}

// probeAuthState mirrors the logic in handleAuthStatus minus the
// extra timestamp field the auth UI needs — we just want
// "authenticated", "expired", or "unauthenticated" for the
// diagnostics snapshot.
// Errors fall through to "unauthenticated" rather than 500-ing the
// whole diagnostics surface.
func (s *Server) probeAuthState() string {
	return s.probeAuthDiagnostics().State
}

func (s *Server) probeAuthDiagnostics() diagnosticsAuthBlock {
	block := diagnosticsAuthBlock{
		Configured:       s.cfg.TokenStore != nil,
		State:            "unauthenticated",
		PersistenceState: "ok",
	}
	if s.cfg.TokenStore == nil {
		return block
	}
	snapshot, err := s.cfg.TokenStore.GetSnapshot()
	if snapshot.PersistenceDegraded {
		block.PersistenceState = "degraded"
	} else if err != nil && !errors.Is(err, cloudproxy.ErrNoSession) {
		block.PersistenceState = "unavailable"
	}
	if err != nil || snapshot.Pair.AccessToken == "" {
		return block
	}
	if snapshot.Pair.IsRefreshExpired(time.Now().UTC()) {
		block.State = "expired"
		return block
	}
	block.State = "authenticated"
	return block
}

func formatRFC3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func redactDiagnosticsString(value string) string {
	if value == "" {
		return ""
	}
	return redactRendererLogString(value)
}

// serverVersionResponse echoes the cloud's /api/desktop/version
// shape verbatim plus a `sidecar_version` field so the renderer
// can compute the upgrade comparison without a second buildinfo
// fetch.
type serverVersionResponse struct {
	MinSupported      string `json:"min_supported"`
	LatestRecommended string `json:"latest_recommended"`
	SidecarVersion    string `json:"sidecar_version"`
	ReleaseNotesURL   string `json:"release_notes_url,omitempty"`
}

// handleServerVersion proxies GET /api/desktop/version through the
// sidecar to the renderer so it can render an "outdated" banner
// when the sidecar is below the cloud-published floor.
//
// Returns 503 when the cloud client isn't configured (test boots,
// diagnostic mode) — same posture as the chat proxy: the endpoint
// exists, but it can't do useful work without the cloud client.
// On a cloud-side failure (network, 5xx) we surface the error so
// the renderer can show "couldn't reach cloud" rather than a stale
// cached value.
//
// The cloud sets Cache-Control on its side (60s); we don't add an
// additional sidecar cache because the cloud's TTL is already short
// enough to keep this light and the renderer hook polls infrequently.
func (s *Server) handleServerVersion(c *gin.Context) {
	if s.cfg.Proxy == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "cloud client not configured"})
		return
	}
	info, err := s.cfg.Proxy.CloudClient().GetVersion(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, serverVersionResponse{
		MinSupported:      info.MinSupported,
		LatestRecommended: info.LatestRecommended,
		SidecarVersion:    s.cfg.SidecarVersion,
		ReleaseNotesURL:   info.ReleaseNotesURL,
	})
}

// handleTriggerSync fires an immediate ThreadsSyncer tick — and,
// optionally, a per-thread MessagesSyncer trigger for the thread
// the user is currently viewing.
//
// Support tooling: "click Force Sync, tell me what happens" gives
// the support engineer a concrete reproducer without waiting up to
// 5min for the next periodic tick.
//
// Optional query param ?thread=<uuid> triggers the per-thread
// messages syncer too — covers the "I deleted a message on web
// and it still shows here" support-bait scenario, not just the
// thread-list-stale case. Silently skipped (200 NoContent still)
// when:
//   - MessagesSyncer isn't wired
//   - thread UUID is absent / unknown
//   - thread doesn't have a cloud_thread_id yet (cache-writer
//     created the row before the first thread sync landed)
//   - malformed thread query values are rejected before any sync
//     side effect is triggered
//
// Returns 204 on success — the trigger is fire-and-forget; the
// renderer learns about the result via /system/diagnostics on the
// next poll. 503 when ThreadsSyncer isn't wired (test boots,
// pre-sync configs) so the renderer button surfaces 'not available'
// rather than silently doing nothing.
func (s *Server) handleTriggerSync(c *gin.Context) {
	if s.cfg.ThreadsSyncer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "threads syncer not configured"})
		return
	}
	threadUUID, err := manualSyncThreadQuery(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateManualSyncThreadQuery(threadUUID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	activeUID := s.activeLocalHistoryUID()
	if activeUID == noLocalHistoryUID {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no active desktop session"})
		return
	}
	s.cfg.ThreadsSyncer.Trigger("manual")

	// Optional per-thread messages trigger. Reuses the existing
	// triggerMessagesSync helper from server_agent.go so the
	// cloud_thread_id lookup + parse rules stay in one place.
	if threadUUID != "" {
		s.triggerMessagesSync(threadUUID, activeUID)
	}

	c.Status(http.StatusNoContent)
}

func manualSyncThreadQuery(r *http.Request) (string, error) {
	values, ok := r.URL.Query()["thread"]
	if !ok {
		return "", nil
	}
	if len(values) > 1 {
		return "", fmt.Errorf("thread query must be provided at most once")
	}
	if values[0] == "" {
		return "", fmt.Errorf("thread query must not be empty")
	}
	return values[0], nil
}

func validateManualSyncThreadQuery(threadUUID string) error {
	if threadUUID == "" {
		return nil
	}
	if _, err := validateLocalHistoryThreadUUID(threadUUID); err != nil {
		return fmt.Errorf("thread query is malformed")
	}
	return nil
}

// handleRendererLog appends a single log entry from the renderer to
// the rotated renderer.log file under the data directory. Returns
// 503 if no logger is wired (sidecar misconfiguration / tests) so
// the renderer can fall back to console.* and a developer sees
// that capture isn't working.
//
// Body limit: 64 KiB. Renderer-side messages stay small; if someone
// tries to dump a stack trace bigger than that, we reject rather
// than risk an OOM from a runaway error loop.
//
// We intentionally don't 400 on a benign empty body — a renderer
// that races a log call against a teardown may POST nothing; better
// to swallow than to log "log call failed" recursion.
func (s *Server) handleRendererLog(c *gin.Context) {
	if s.cfg.RendererLogger == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "renderer logger not configured"})
		return
	}
	const maxBody = maxRendererLogBodyBytes
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body: " + err.Error()})
		return
	}
	if len(body) > maxBody {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "renderer log body too large"})
		return
	}
	if len(body) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	var entry RendererLogEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json: " + err.Error()})
		return
	}
	if err := s.cfg.RendererLogger.Append(entry); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
