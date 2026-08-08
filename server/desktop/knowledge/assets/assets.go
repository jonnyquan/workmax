//go:build desktop

// Package assets acquires the native resources the local RAG stack needs:
// the ONNX Runtime shared library, the sentence-embedding model, and its
// tokenizer. Together they are tens of megabytes, so they are fetched on
// first use rather than shipped inside the app bundle — a WorkMax install
// stays small for the majority of users who never turn on the knowledge base.
//
// Deliberately NOT cgo, unlike its parent package: the code that decides
// whether resources are present, and downloads them, must be reachable from
// the non-cgo desktop package that serves the HTTP surface. Only the code
// that *uses* the resources needs a C toolchain.
//
// Trust model: every asset is pinned by SHA-256 in manifest.json. A download
// that does not hash to the pinned value is discarded, so a compromised or
// merely stale mirror cannot place executable code on a user's disk. The
// hashes are the security boundary here, not the URLs.
package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "embed"
)

// manifest.json pins every downloadable asset by URL, size and SHA-256.
// It is data, not code, so refreshing a model or an ONNX Runtime version is a
// reviewable one-file diff rather than a patch to download logic.
//
//go:embed manifest.json
var manifestJSON []byte

// Asset is one downloadable file.
type Asset struct {
	// Name is the human-readable label used in logs and progress reports.
	Name string `json:"name"`

	// Path is the destination relative to the resources directory. It must
	// match the layout knowledge.ResolveResourcesIn expects.
	Path string `json:"path"`

	// URL is where to fetch it from.
	URL string `json:"url"`

	// SHA256 is the lowercase hex digest of the expected content.
	SHA256 string `json:"sha256"`

	// Size is the expected byte count, used for progress reporting and to
	// reject an obviously wrong response before streaming all of it.
	Size int64 `json:"size"`

	// Executable marks files that need the exec bit (the shared library).
	Executable bool `json:"executable,omitempty"`
}

type manifest struct {
	SchemaVersion int                `json:"schemaVersion"`
	Platforms     map[string][]Asset `json:"platforms"`
}

// Progress reports download advancement so a caller can surface it. Called
// frequently; implementations must not block.
type Progress struct {
	Asset      Asset
	Index      int // 0-based position in the asset list
	Total      int // number of assets being fetched this run
	Downloaded int64
	Expected   int64
}

// ErrUnsupportedPlatform means no asset set is pinned for this OS/arch. RAG
// is simply unavailable there; it is not a failure the caller should retry.
var ErrUnsupportedPlatform = errors.New("knowledge assets: unsupported platform")

// For returns the pinned asset list for the given platform key ("darwin/arm64").
func For(platform string) ([]Asset, error) {
	var m manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("knowledge assets: parse manifest: %w", err)
	}
	list, ok := m.Platforms[platform]
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedPlatform, platform)
	}
	return list, nil
}

// Current returns the pinned asset list for the running platform.
func Current() ([]Asset, error) {
	return For(runtime.GOOS + "/" + runtime.GOARCH)
}

// Missing reports which of the platform's assets are not yet present and
// intact under dir. A file whose digest does not match is reported missing:
// a truncated or tampered asset must be replaced, not used.
//
// Hashing is the point of this call, so it costs a full read of what is
// already on disk. Callers that only need a cheap "probably installed" signal
// should stat the paths themselves.
func Missing(dir string) ([]Asset, error) {
	all, err := Current()
	if err != nil {
		return nil, err
	}
	return MissingIn(dir, all)
}

// MissingIn is Missing against an explicit asset list.
func MissingIn(dir string, all []Asset) ([]Asset, error) {
	var missing []Asset
	for _, a := range all {
		ok, err := verify(filepath.Join(dir, a.Path), a.SHA256)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, a)
		}
	}
	return missing, nil
}

// Ensure downloads whatever is missing into dir. It is safe to call when
// everything is already present (it becomes a hash check and returns nil).
//
// Downloads land in a .part file next to the destination and are renamed only
// after the digest matches, so an interrupted run never leaves a plausible-
// looking but corrupt asset behind — and a later run resumes the .part via a
// Range request instead of starting over.
func Ensure(ctx context.Context, dir string, onProgress func(Progress)) error {
	all, err := Current()
	if err != nil {
		return err
	}
	return EnsureIn(ctx, dir, all, onProgress)
}

// EnsureIn is Ensure against an explicit asset list.
func EnsureIn(ctx context.Context, dir string, all []Asset, onProgress func(Progress)) error {
	missing, err := MissingIn(dir, all)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("knowledge assets: create %s: %w", dir, err)
	}

	client := &http.Client{
		// No overall timeout: these are large files on unknown connections.
		// Cancellation is the caller's ctx, and a stalled body still fails
		// through the transport's own read deadlines.
		Timeout: 0,
	}
	for i, a := range missing {
		if err := fetch(ctx, client, dir, a, i, len(missing), onProgress); err != nil {
			return err
		}
	}
	return nil
}

func fetch(ctx context.Context, client *http.Client, dir string, a Asset, index, total int, onProgress func(Progress)) error {
	dest := filepath.Join(dir, a.Path)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("knowledge assets: create %s: %w", filepath.Dir(dest), err)
	}
	partPath := dest + ".part"

	// Resume whatever a previous run managed to write.
	var resumeFrom int64
	if info, err := os.Stat(partPath); err == nil && info.Size() < a.Size {
		resumeFrom = info.Size()
	} else if err == nil {
		// A .part at or past the expected size is not resumable — it is
		// either finished-but-unverified or wrong. Start clean.
		_ = os.Remove(partPath)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return fmt.Errorf("knowledge assets: %s: %w", a.Name, err)
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("knowledge assets: download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Server ignored (or we did not ask for) the range; rewrite from zero.
		resumeFrom = 0
	case http.StatusPartialContent:
		// Resuming as requested.
	default:
		return fmt.Errorf("knowledge assets: download %s: unexpected status %s", a.Name, resp.Status)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resumeFrom > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("knowledge assets: open %s: %w", partPath, err)
	}

	written, err := copyWithProgress(f, resp.Body, resumeFrom, a, index, total, onProgress)
	closeErr := f.Close()
	if err != nil {
		// Keep the .part so the next attempt can resume from here.
		return fmt.Errorf("knowledge assets: download %s: %w", a.Name, err)
	}
	if closeErr != nil {
		return fmt.Errorf("knowledge assets: close %s: %w", partPath, closeErr)
	}
	if written != a.Size {
		_ = os.Remove(partPath)
		return fmt.Errorf("knowledge assets: %s: got %d bytes, want %d", a.Name, written, a.Size)
	}

	ok, err := verify(partPath, a.SHA256)
	if err != nil {
		return err
	}
	if !ok {
		// A digest mismatch is not a transient error — resuming would only
		// re-download the same wrong bytes. Discard and fail loudly.
		_ = os.Remove(partPath)
		return fmt.Errorf("knowledge assets: %s: sha256 mismatch, discarded", a.Name)
	}

	mode := os.FileMode(0o644)
	if a.Executable {
		mode = 0o755
	}
	if err := os.Chmod(partPath, mode); err != nil {
		return fmt.Errorf("knowledge assets: chmod %s: %w", partPath, err)
	}
	if err := os.Rename(partPath, dest); err != nil {
		return fmt.Errorf("knowledge assets: install %s: %w", a.Name, err)
	}
	return nil
}

func copyWithProgress(dst io.Writer, src io.Reader, already int64, a Asset, index, total int, onProgress func(Progress)) (int64, error) {
	buf := make([]byte, 512*1024)
	written := already
	lastReport := time.Time{}
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			if onProgress != nil && time.Since(lastReport) > 200*time.Millisecond {
				lastReport = time.Now()
				onProgress(Progress{
					Asset: a, Index: index, Total: total,
					Downloaded: written, Expected: a.Size,
				})
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return written, readErr
		}
	}
	if onProgress != nil {
		onProgress(Progress{
			Asset: a, Index: index, Total: total,
			Downloaded: written, Expected: a.Size,
		})
	}
	return written, nil
}

// verify reports whether path exists and hashes to want. A missing file is
// (false, nil): absence is an expected state, not an error.
func verify(path string, want string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("knowledge assets: open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, fmt.Errorf("knowledge assets: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}
