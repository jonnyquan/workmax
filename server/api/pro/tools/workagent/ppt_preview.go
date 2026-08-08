package workagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pptPreviewConvertTimeout = 45 * time.Second
	macSofficePath           = "/Applications/LibreOffice.app/Contents/MacOS/soffice"
	// pptPreviewMaxConcurrent caps how many soffice subprocesses can
	// run at the same time across the whole process. Each soffice
	// invocation allocates a Java VM + GUI subsystem and easily uses
	// 200–400 MB resident; a thread that just produced a pile of
	// fresh .pptx outputs would otherwise fan out one process per
	// file and saturate host memory before any of them complete.
	// 4 is a conservative ceiling — preview is a soft path (callers
	// degrade to the raw pptx download on timeout or queue wait).
	pptPreviewMaxConcurrent = 4
)

var (
	errSofficeUnavailable = errors.New("soffice/libreoffice is not available")

	sofficePathOnce sync.Once
	sofficePath     string
	sofficePathErr  error

	// pptPreviewLocks serializes preview conversions for the same source path.
	// Entries are refcounted and deleted when the last waiter releases the lock,
	// so a long-running process can't accumulate one mutex per presentation
	// ever previewed.
	pptPreviewLocks   = map[string]*pptPreviewLockEntry{}
	pptPreviewLocksMu sync.Mutex

	// pptPreviewSlots bounds concurrent soffice subprocesses across all
	// paths. Per-path locks above only stop two converters racing on
	// the *same* file; without this semaphore a fresh batch of
	// distinct outputs would still launch N parallel sofficeprocesses
	// and starve the host.
	pptPreviewSlots = make(chan struct{}, pptPreviewMaxConcurrent)
)

type pptPreviewLockEntry struct {
	mu       sync.Mutex
	refCount int
}

func isPresentationPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".ppt" || ext == ".pptx"
}

// toPresentationPreviewPDFPath derives the cached preview path next
// to the source. Naming is intentional: we keep the original .pptx
// extension in the filename and append `.preview.pdf` so an unrelated
// user-uploaded `<basename>.pdf` in the same directory can never be
// mistaken for our cached preview by the mtime check below. soffice's
// own output (always `<stripped>.pdf`) is staged via a temp subdir
// and renamed into this path — see ensurePresentationPreviewPDF.
func toPresentationPreviewPDFPath(path string) string {
	return path + ".preview.pdf"
}

func resolveSofficeBinary() (string, error) {
	sofficePathOnce.Do(func() {
		if path, err := exec.LookPath("soffice"); err == nil {
			sofficePath = path
			return
		}
		if path, err := exec.LookPath("libreoffice"); err == nil {
			sofficePath = path
			return
		}
		if _, err := os.Stat(macSofficePath); err == nil {
			sofficePath = macSofficePath
			return
		}
		sofficePathErr = errSofficeUnavailable
	})

	if sofficePath == "" {
		if sofficePathErr != nil {
			return "", sofficePathErr
		}
		return "", errSofficeUnavailable
	}
	return sofficePath, nil
}

func shouldLogPreviewConversionError(err error) bool {
	return err != nil && !errors.Is(err, errSofficeUnavailable)
}

func withPresentationPreviewLock(path string, fn func() (string, error)) (string, error) {
	lockKey := filepath.Clean(path)

	pptPreviewLocksMu.Lock()
	entry, ok := pptPreviewLocks[lockKey]
	if !ok {
		entry = &pptPreviewLockEntry{}
		pptPreviewLocks[lockKey] = entry
	}
	entry.refCount++
	pptPreviewLocksMu.Unlock()

	entry.mu.Lock()
	defer func() {
		entry.mu.Unlock()
		pptPreviewLocksMu.Lock()
		entry.refCount--
		if entry.refCount == 0 {
			delete(pptPreviewLocks, lockKey)
		}
		pptPreviewLocksMu.Unlock()
	}()

	return fn()
}

func ensurePresentationPreviewPDF(presentationPath string) (string, error) {
	if !isPresentationPath(presentationPath) {
		return "", nil
	}

	cleanPath := filepath.Clean(presentationPath)
	return withPresentationPreviewLock(cleanPath, func() (string, error) {
		// Lstat (not Stat) so a symlinked source — which an agent run
		// could plausibly drop into the workspace — gets refused
		// outright instead of silently following into the OS file
		// system. Otherwise soffice would convert the resolved target
		// and `--outdir filepath.Dir(cleanPath)` would still write the
		// resulting PDF next to the symlink, leaking arbitrary file
		// content under workspace-relative URLs. Same posture as the
		// recent copyFile / RegisterOutputFile / SyncOutputFiles
		// confinement fixes in this package.
		srcInfo, err := os.Lstat(cleanPath)
		if err != nil {
			return "", fmt.Errorf("presentation file stat failed: %w", err)
		}
		if srcInfo.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to convert symlinked presentation: %s", cleanPath)
		}
		if !srcInfo.Mode().IsRegular() {
			return "", fmt.Errorf("refusing to convert non-regular presentation: %s", cleanPath)
		}

		pdfPath := toPresentationPreviewPDFPath(cleanPath)
		if pdfInfo, err := os.Lstat(pdfPath); err == nil &&
			pdfInfo.Mode().IsRegular() &&
			!pdfInfo.ModTime().Before(srcInfo.ModTime()) {
			return pdfPath, nil
		}

		sofficeBin, err := resolveSofficeBinary()
		if err != nil {
			return "", err
		}

		// Stage soffice's output in a per-conversion temp dir so its
		// hard-coded `<stripped>.pdf` filename can't clobber any
		// unrelated user PDF that happens to live next to the source.
		// MkdirTemp under filepath.Dir(cleanPath) keeps everything on
		// the same filesystem so the final rename is atomic. Cleanup
		// happens unconditionally — the produced PDF moves out before
		// RemoveAll runs.
		tempOutDir, err := os.MkdirTemp(filepath.Dir(cleanPath), ".pptpreview-")
		if err != nil {
			return "", fmt.Errorf("preview temp dir create failed: %w", err)
		}
		defer os.RemoveAll(tempOutDir)

		ctx, cancel := context.WithTimeout(context.Background(), pptPreviewConvertTimeout)
		defer cancel()

		// Wait for a global slot before launching soffice. Bound by
		// the same convert-timeout so a queue stall doesn't leak
		// blocked goroutines indefinitely; callers already treat a
		// timeout as "preview unavailable, fall back to raw download".
		select {
		case pptPreviewSlots <- struct{}{}:
			defer func() { <-pptPreviewSlots }()
		case <-ctx.Done():
			return "", fmt.Errorf("presentation to pdf conversion queue wait timed out after %s", pptPreviewConvertTimeout)
		}

		cmd := exec.CommandContext(
			ctx,
			sofficeBin,
			"--headless",
			"--invisible",
			"--nologo",
			"--nolockcheck",
			"--nodefault",
			"--nofirststartwizard",
			"--convert-to",
			"pdf",
			"--outdir",
			tempOutDir,
			cleanPath,
		)

		output, err := cmd.CombinedOutput()
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("presentation to pdf conversion timed out after %s", pptPreviewConvertTimeout)
			}
			trimmed := strings.TrimSpace(string(output))
			if len(trimmed) > 300 {
				trimmed = trimmed[:300] + "..."
			}
			if trimmed == "" {
				return "", fmt.Errorf("presentation to pdf conversion failed: %w", err)
			}
			return "", fmt.Errorf("presentation to pdf conversion failed: %w (%s)", err, trimmed)
		}

		// soffice writes `<basename without ext>.pdf` regardless of the
		// source extension. Find it inside our temp dir and atomic-move
		// it to the final cache path.
		producedName := strings.TrimSuffix(filepath.Base(cleanPath), filepath.Ext(cleanPath)) + ".pdf"
		producedPath := filepath.Join(tempOutDir, producedName)
		if _, err := os.Stat(producedPath); err != nil {
			return "", fmt.Errorf("converted pdf not found in temp dir: %w", err)
		}
		if err := os.Rename(producedPath, pdfPath); err != nil {
			return "", fmt.Errorf("preview rename failed: %w", err)
		}

		return pdfPath, nil
	})
}
