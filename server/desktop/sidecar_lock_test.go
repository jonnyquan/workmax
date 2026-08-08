//go:build desktop

package desktop

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireSidecarLockCreatesAndReleasesPIDFile(t *testing.T) {
	dataDir := t.TempDir()
	release, err := AcquireSidecarLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireSidecarLock: %v", err)
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

	_, err := AcquireSidecarLock(dataDir)
	if err == nil {
		t.Fatal("AcquireSidecarLock should reject a live owner")
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

	release, err := AcquireSidecarLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireSidecarLock: %v", err)
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

	release, err := AcquireSidecarLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireSidecarLock: %v", err)
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

	ReleaseSidecarLock(lockPath, strconv.Itoa(os.Getpid()))

	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "12345" {
		t.Fatalf("lock pid = %q, want unchanged 12345", got)
	}
}
