//go:build desktop

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"server/desktop"
	cloudproxy "server/desktop/cloud_proxy"
)

func TestHandleMetaCommand(t *testing.T) {
	for _, args := range [][]string{
		{"--version"},
		{"version"},
		{"--help"},
		{"help"},
	} {
		if !handleMetaCommand(args) {
			t.Fatalf("handleMetaCommand(%v) = false, want true", args)
		}
	}

	for _, args := range [][]string{
		nil,
		{},
		{"--unknown"},
		{"--version", "extra"},
	} {
		if handleMetaCommand(args) {
			t.Fatalf("handleMetaCommand(%v) = true, want false", args)
		}
	}
}

func TestAcquireSidecarLockCreatesAndReleasesPIDFile(t *testing.T) {
	dataDir := t.TempDir()
	release, err := acquireSidecarLock(dataDir)
	if err != nil {
		t.Fatalf("acquireSidecarLock: %v", err)
	}

	lockPath := filepath.Join(dataDir, sidecarPIDFileName)
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := strings.TrimSpace(string(raw)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock pid = %q, want %q", got, want)
	}

	release()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed on release, stat err=%v", err)
	}
}

func TestAcquireSidecarLockRejectsLiveOwner(t *testing.T) {
	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, sidecarPIDFileName)
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}

	_, err := acquireSidecarLock(dataDir)
	if err == nil {
		t.Fatal("acquireSidecarLock should reject a live owner")
	}
	if !strings.Contains(err.Error(), "another sidecar instance is already running") {
		t.Fatalf("error = %q, want live-owner message", err)
	}
}

func TestAcquireSidecarLockReplacesStaleOwner(t *testing.T) {
	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, sidecarPIDFileName)
	if err := os.WriteFile(lockPath, []byte("999999999\n"), 0o644); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}

	release, err := acquireSidecarLock(dataDir)
	if err != nil {
		t.Fatalf("acquireSidecarLock: %v", err)
	}
	defer release()

	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := strings.TrimSpace(string(raw)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock pid = %q, want %q", got, want)
	}
}

func TestAcquireSidecarLockReplacesCorruptLock(t *testing.T) {
	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, sidecarPIDFileName)
	if err := os.WriteFile(lockPath, []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}

	release, err := acquireSidecarLock(dataDir)
	if err != nil {
		t.Fatalf("acquireSidecarLock: %v", err)
	}
	defer release()

	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := strings.TrimSpace(string(raw)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock pid = %q, want %q", got, want)
	}
}

func TestReleaseSidecarLockDoesNotRemoveChangedOwner(t *testing.T) {
	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, sidecarPIDFileName)
	if err := os.WriteFile(lockPath, []byte("12345\n"), 0o644); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}

	releaseSidecarLock(lockPath, strconv.Itoa(os.Getpid()))

	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "12345" {
		t.Fatalf("lock pid = %q, want unchanged 12345", got)
	}
}

func TestSeedSmokeDesktopCacheScopesSeededAuthAndHistory(t *testing.T) {
	t.Setenv(desktop.DataDirEnv, t.TempDir())

	dbRes, err := desktop.OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB: %v", err)
	}
	sqlDB, err := dbRes.DB.DB()
	if err != nil {
		t.Fatalf("DB.DB: %v", err)
	}
	defer sqlDB.Close()

	tokenStore := cloudproxy.NewTokenStore(newSmokeMemoryKeychain())
	if err := seedSmokeDesktopCache(tokenStore, dbRes.DB); err != nil {
		t.Fatalf("seedSmokeDesktopCache: %v", err)
	}

	pair, err := tokenStore.Get()
	if err != nil {
		t.Fatalf("TokenStore.Get: %v", err)
	}
	if pair.RefreshToken != "smoke-refresh-token" {
		t.Fatalf("refresh token = %q, want smoke fixture token", pair.RefreshToken)
	}
	uid, err := cloudproxy.ExtractUIDFromAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("ExtractUIDFromAccessToken: %v", err)
	}
	if uid != 42 {
		t.Fatalf("seeded access token uid = %d, want 42", uid)
	}

	threads, err := desktop.ListLocalThreads(dbRes.DB, 42, 10, false)
	if err != nil {
		t.Fatalf("ListLocalThreads uid=42: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("uid=42 threads count = %d, want 1: %+v", len(threads), threads)
	}
	if threads[0].UUID != "smoke-cached-thread" || threads[0].Name != "Smoke Cached Thread" {
		t.Fatalf("seeded thread = %+v, want smoke cached thread", threads[0])
	}

	otherThreads, err := desktop.ListLocalThreads(dbRes.DB, 99, 10, false)
	if err != nil {
		t.Fatalf("ListLocalThreads uid=99: %v", err)
	}
	if len(otherThreads) != 0 {
		t.Fatalf("uid=99 should not see smoke thread: %+v", otherThreads)
	}

	messages, err := desktop.ListLocalMessages(dbRes.DB, 42, "smoke-cached-thread", 10)
	if err != nil {
		t.Fatalf("ListLocalMessages uid=42: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("uid=42 messages count = %d, want 1: %+v", len(messages), messages)
	}
	if messages[0].UserText != "Smoke cached user question" ||
		messages[0].AIText != "Smoke cached assistant answer" ||
		messages[0].StreamingState != "complete" {
		t.Fatalf("seeded message = %+v, want smoke cached exchange", messages[0])
	}

	otherMessages, err := desktop.ListLocalMessages(dbRes.DB, 99, "smoke-cached-thread", 10)
	if err != nil {
		t.Fatalf("ListLocalMessages uid=99: %v", err)
	}
	if len(otherMessages) != 0 {
		t.Fatalf("uid=99 should not see smoke messages: %+v", otherMessages)
	}
}

func TestSmokeTokenStoreCannotClearProductionSessionTombstone(t *testing.T) {
	t.Setenv(desktop.DataDirEnv, t.TempDir())
	dbRes, err := desktop.OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB: %v", err)
	}
	sqlDB, err := dbRes.DB.DB()
	if err != nil {
		t.Fatalf("DB.DB: %v", err)
	}
	defer sqlDB.Close()

	marker := desktop.NewSQLiteSessionTombstoneMarker(dbRes.DB)
	if err := marker.Mark(); err != nil {
		t.Fatalf("seed production tombstone: %v", err)
	}
	smokeStore := newDesktopTokenStore(newSmokeMemoryKeychain(), dbRes.DB, true)
	if err := smokeStore.Save(cloudproxy.TokenPair{
		AccessToken:  "smoke-access",
		RefreshToken: "smoke-refresh",
	}); err != nil {
		t.Fatalf("smoke Save: %v", err)
	}
	marked, err := marker.IsMarked()
	if err != nil {
		t.Fatalf("production marker IsMarked: %v", err)
	}
	if !marked {
		t.Fatal("smoke token persistence cleared the production session tombstone")
	}

	// A real-store Save is the only path that may clear the production marker.
	productionStore := newDesktopTokenStore(newSmokeMemoryKeychain(), dbRes.DB, false)
	if err := productionStore.Save(cloudproxy.TokenPair{
		AccessToken:  "production-access",
		RefreshToken: "production-refresh",
	}); err != nil {
		t.Fatalf("production Save: %v", err)
	}
	marked, err = marker.IsMarked()
	if err != nil {
		t.Fatalf("production marker after Save: %v", err)
	}
	if marked {
		t.Fatal("complete production token persistence left tombstone marked")
	}
}
