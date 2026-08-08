//go:build desktop

package desktop

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The sidecar lock guards one thing: two processes opening the same SQLite
// cache. The desktop shell has its own single-instance lock, but that does
// not cover a standalone `--serve-only` launch, a stale child that outlived
// its parent, or a developer running the binary by hand next to a live app.
//
// Note for the Windows port: processIsAlive uses syscall.Kill and so is
// unix-only, which matches the current desktop build (macOS). Windows needs a
// _windows variant using OpenProcess.

const sidecarPIDFileName = "sidecar.pid"

// AcquireSidecarLock claims <dataDir>/sidecar.pid for this process. If the
// lock points at a live process, startup is refused. If the file is stale or
// corrupt, it is removed and replaced. The returned func releases the lock,
// and is safe to call once the process is done with the data directory.
func AcquireSidecarLock(dataDir string) (func(), error) {
	lockPath := filepath.Join(dataDir, sidecarPIDFileName)
	pidText := strconv.Itoa(os.Getpid())

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
				ReleaseSidecarLock(lockPath, pidText)
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

// ReleaseSidecarLock removes the lock file only if we still own it. A lock
// whose owner changed belongs to a process that took over after we were
// declared stale; deleting it would let a third process in.
func ReleaseSidecarLock(lockPath string, pidText string) {
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
