//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cloudproxy "server/desktop/cloud_proxy"
	desktopsync "server/desktop/sync"
)

func TestHandleDiagnostics_MinimalConfig(t *testing.T) {
	// Boot a server with ONLY the required deps wired. Every
	// optional subsystem must report configured=false; response
	// shape must still be stable.
	db := openHistoryTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test-0.1.0-p1-ea",
		LocalToken:     "tok",
		DB:             db,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	req, _ := http.NewRequest(http.MethodGet, base+"/system/diagnostics", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var got diagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Sidecar block: always populated from cfg.
	if got.Sidecar.Version != "test-0.1.0-p1-ea" {
		t.Errorf("Sidecar.Version: %q", got.Sidecar.Version)
	}
	if got.Sidecar.UptimeSeconds < 0 {
		t.Errorf("Sidecar.UptimeSeconds negative: %d", got.Sidecar.UptimeSeconds)
	}
	if got.Sidecar.HeapAllocBytes == 0 {
		t.Error("Sidecar.HeapAllocBytes should be populated")
	}
	if got.Sidecar.HeapSysBytes == 0 {
		t.Error("Sidecar.HeapSysBytes should be populated")
	}
	if got.Sidecar.NumGoroutine <= 0 {
		t.Errorf("Sidecar.NumGoroutine should be positive, got %d", got.Sidecar.NumGoroutine)
	}

	// Every optional subsystem reports configured=false with a
	// zero-value block; consumers can render "—" without
	// branching on missing JSON keys.
	if got.ThreadsSyncer.Configured {
		t.Error("ThreadsSyncer should report configured=false")
	}
	if got.MessagesSync.Configured {
		t.Error("MessagesSync should report configured=false")
	}
	if got.NetworkState.Configured {
		t.Error("NetworkState should report configured=false")
	}
	if got.Auth.Configured {
		t.Error("Auth should report configured=false")
	}
}

func TestHandleDiagnostics_RequiresLocalToken(t *testing.T) {
	db := openHistoryTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// No X-Local-Token header — loopback middleware must reject.
	base := "http://" + srv.listener.Addr().String()
	resp, err := http.Get(base + "/system/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("missing token: status %d, want 403", resp.StatusCode)
	}
}

// TestHandleDiagnostics_SurfacesDataDirAndMigrations pins the
// support-facing fields: a curl of /system/diagnostics must show
// where the SQLite cache lives + which schema migrations have run.
// Without these, support's "where do I look for your logs?" question
// becomes a guessing game across platforms.
func TestHandleDiagnostics_SurfacesDataDirAndMigrations(t *testing.T) {
	db := openHistoryTestDB(t)
	// Seed the _schema_migrations table so the diagnostics handler
	// has something to surface. Mirrors the shape the
	// migrations_desktop runner writes.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS _schema_migrations (
		version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO _schema_migrations (version, applied_at)
		VALUES ('0001', '2026-05-19T00:00:00Z'), ('0002', '2026-05-19T00:01:00Z')`).Error; err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		DataDir:        "/tmp/workmax-test-data",
		DBPath:         "/tmp/workmax-test-data/workagent.db",
		BackupPath:     "/tmp/workmax-test-data/backups/workagent-20260521.db",
		IntegrityCheck: "ok",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	req, _ := http.NewRequest(http.MethodGet, base+"/system/diagnostics", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got diagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got.Sidecar.DataDir != "/tmp/workmax-test-data" {
		t.Errorf("data_dir: %q", got.Sidecar.DataDir)
	}
	if got.Sidecar.DBPath != "/tmp/workmax-test-data/workagent.db" {
		t.Errorf("db_path: %q", got.Sidecar.DBPath)
	}
	if got.Sidecar.BackupPath != "/tmp/workmax-test-data/backups/workagent-20260521.db" {
		t.Errorf("backup_path: %q", got.Sidecar.BackupPath)
	}
	if got.Sidecar.IntegrityCheck != "ok" {
		t.Errorf("integrity_check: %q", got.Sidecar.IntegrityCheck)
	}
	if len(got.Sidecar.AppliedMigrations) != 2 {
		t.Fatalf("applied_migrations: got %v, want [0001 0002]", got.Sidecar.AppliedMigrations)
	}
	if got.Sidecar.AppliedMigrations[0] != "0001" || got.Sidecar.AppliedMigrations[1] != "0002" {
		t.Errorf("applied_migrations order: %v", got.Sidecar.AppliedMigrations)
	}
}

func TestHandleDiagnostics_RedactsSyncLastError(t *testing.T) {
	worker := desktopsync.NewSyncWorker(func(context.Context) error {
		return fmt.Errorf("sync failed Authorization: Bearer access-secret at https://user:pass@example.com/path?refresh_token=refresh-secret")
	}, desktopsync.Config{PeriodicInterval: 0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)
	waitForSyncLastError(t, worker)

	db := openHistoryTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		ThreadsSyncer:  worker,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	base := "http://" + srv.listener.Addr().String()
	req, _ := http.NewRequest(http.MethodGet, base+"/system/diagnostics", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got diagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	lastError := got.ThreadsSyncer.LastError
	for _, leaked := range []string{"access-secret", "user:pass", "refresh-secret"} {
		if strings.Contains(lastError, leaked) {
			t.Fatalf("diagnostics last_error leaked %q: %s", leaked, lastError)
		}
	}
	for _, want := range []string{"Bearer [REDACTED]", "https://[REDACTED]@example.com/path", "refresh_token=[REDACTED]"} {
		if !strings.Contains(lastError, want) {
			t.Fatalf("diagnostics last_error missing %q: %s", want, lastError)
		}
	}
}

func TestFormatRFC3339OrEmpty(t *testing.T) {
	if got := formatRFC3339OrEmpty(time.Time{}); got != "" {
		t.Errorf("zero time: got %q, want empty", got)
	}
	t1 := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	if got := formatRFC3339OrEmpty(t1); got != "2026-05-19T12:00:00Z" {
		t.Errorf("known time: %q", got)
	}
}

func TestProbeAuthState_UsesRefreshExpiry(t *testing.T) {
	store := cloudproxy.NewTokenStore(newMemKeychain())
	srv := &Server{cfg: ServerConfig{TokenStore: store}}
	now := time.Now().UTC()

	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "expired-access",
		AccessExpiresAt:  now.Add(-time.Minute),
		RefreshToken:     "valid-refresh",
		RefreshExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if got := srv.probeAuthState(); got != "authenticated" {
		t.Fatalf("expired access with valid refresh: got %q, want authenticated", got)
	}

	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "access",
		AccessExpiresAt:  now.Add(time.Hour),
		RefreshToken:     "expired-refresh",
		RefreshExpiresAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if got := srv.probeAuthState(); got != "expired" {
		t.Fatalf("expired refresh: got %q, want expired", got)
	}

	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := srv.probeAuthState(); got != "unauthenticated" {
		t.Fatalf("empty store: got %q, want unauthenticated", got)
	}
}

func TestProbeAuthDiagnostics_SurfacesPersistenceDegraded(t *testing.T) {
	keychain := &logoutDeleteFailureKeychain{}
	store := cloudproxy.NewTokenStore(keychain)
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	keychain.deleteErr = fmt.Errorf("private persistence detail")
	if err := store.Clear(); !errors.Is(err, cloudproxy.ErrSessionPersistence) {
		t.Fatalf("Clear error = %v, want ErrSessionPersistence", err)
	}

	srv := &Server{cfg: ServerConfig{TokenStore: store}}
	got := srv.probeAuthDiagnostics()
	if !got.Configured || got.State != "unauthenticated" || got.PersistenceState != "degraded" {
		t.Fatalf("auth diagnostics = %+v", got)
	}
}

func waitForSyncLastError(t *testing.T, worker *desktopsync.SyncWorker) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if worker.Snapshot().LastError != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for sync worker last error")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
