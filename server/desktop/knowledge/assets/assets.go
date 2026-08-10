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
//
// # Where the pinning comes from
//
// Three sources, most specific first:
//
//  1. $WORKMAX_KNOWLEDGE_MANIFEST — a path to a manifest file. This is how a
//     build or an internal deployment supplies hosting locations without those
//     locations being baked into the open-source tree.
//  2. <resources dir>/manifest.json — a manifest the user dropped next to
//     their own copy of the assets.
//  3. The embedded manifest.json in this package.
//
// # Release process TODO (not a code TODO)
//
// The embedded manifest ships with an EMPTY platforms map, because the
// embedding model WorkMax was validated against is a local SentenceTransformer
// export with no public URL to pin. Empty is a deliberate, explicit state: it
// means "no assets are pinned for this platform", PlanFor returns
// ErrUnsupportedPlatform, and the caller disables RAG with a log line that says
// so. It is not a bug to be worked around by inventing a URL — an unpinned
// download is exactly the thing the hash pinning exists to prevent.
//
// Shipping RAG on by default therefore requires a release-process step, not a
// code change: host the three assets, record their sizes and SHA-256 digests,
// and fill in manifest.json's platforms map (or point builds at a manifest that
// does). Until then, RAG works for anyone who supplies their own assets or
// their own manifest, and is cleanly off for everyone else.
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

// ErrTooLarge means the manifest asks for more bytes than a first-run download
// is allowed to pull. A manifest is trusted to name files, not to be unbounded:
// the cap is what stops a mistaken or hostile manifest from filling a disk
// before a single digest gets checked.
var ErrTooLarge = errors.New("knowledge assets: manifest exceeds the download size cap")

// ManifestPathEnv names an override manifest file. Set by builds and internal
// deployments that host the assets somewhere this repository does not name.
const ManifestPathEnv = "WORKMAX_KNOWLEDGE_MANIFEST"

// ManifestFileName is the manifest a user can drop beside their own assets.
const ManifestFileName = "manifest.json"

// MaxTotalBytes caps one download run. The real payload is ~120MB (ONNX
// Runtime + MiniLM + tokenizer); 1GiB leaves room for a larger model without
// leaving room for a runaway.
const MaxTotalBytes int64 = 1 << 30

// Plan is the resolved answer to "what has to be downloaded for this machine,
// and who said so".
type Plan struct {
	// Origin is "embedded" or the path of the manifest file that won.
	// Reported in logs so a support transcript shows which pinning was used.
	Origin string

	// Platform is the "os/arch" key the assets were selected under.
	Platform string

	// Assets is the full pinned set, present or not.
	Assets []Asset

	// TotalBytes is their combined expected size.
	TotalBytes int64
}

// PlanFor resolves the manifest (env override, then dir/manifest.json, then
// embedded) and returns the pinned assets for the running platform.
//
// ErrUnsupportedPlatform means the winning manifest pins nothing here. That is
// a terminal, actionable state — not a transient failure to retry — and the
// caller is expected to say so out loud rather than degrade silently.
func PlanFor(dir string) (Plan, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	raw, origin, err := loadManifest(dir)
	if err != nil {
		return Plan{Origin: origin, Platform: platform}, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Plan{Origin: origin, Platform: platform}, fmt.Errorf("knowledge assets: parse manifest %s: %w", origin, err)
	}
	list, ok := m.Platforms[platform]
	if !ok || len(list) == 0 {
		return Plan{Origin: origin, Platform: platform}, fmt.Errorf("%w: %s (manifest: %s)", ErrUnsupportedPlatform, platform, origin)
	}
	p := Plan{Origin: origin, Platform: platform, Assets: list}
	for _, a := range list {
		if a.Path == "" || a.URL == "" || a.SHA256 == "" || a.Size <= 0 {
			return p, fmt.Errorf("knowledge assets: manifest %s pins %q without a path, url, sha256 and size", origin, a.Name)
		}
		p.TotalBytes += a.Size
	}
	if p.TotalBytes > MaxTotalBytes {
		return p, fmt.Errorf("%w: %d bytes (cap %d, manifest: %s)", ErrTooLarge, p.TotalBytes, MaxTotalBytes, origin)
	}
	return p, nil
}

// loadManifest returns the manifest bytes and a label for where they came from.
func loadManifest(dir string) ([]byte, string, error) {
	if p := os.Getenv(ManifestPathEnv); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, p, fmt.Errorf("knowledge assets: read %s=%s: %w", ManifestPathEnv, p, err)
		}
		return b, p, nil
	}
	if dir != "" {
		p := filepath.Join(dir, ManifestFileName)
		b, err := os.ReadFile(p)
		if err == nil {
			return b, p, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, p, fmt.Errorf("knowledge assets: read %s: %w", p, err)
		}
	}
	return manifestJSON, "embedded", nil
}

// For returns the embedded manifest's asset list for a platform key
// ("darwin/arm64"), ignoring any override. Only the embedded pinning is
// consulted, which is what makes it useful for asserting what ships.
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

// Current returns the embedded manifest's asset list for the running platform.
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
	p, err := PlanFor(dir)
	if err != nil {
		return nil, err
	}
	return MissingIn(dir, p.Assets)
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

// DownloadTimeout bounds one Ensure run end to end. Generous, because ~120MB
// over a bad hotel connection is legitimately slow; bounded, because a stalled
// transfer must eventually free the retry path rather than hang forever.
const DownloadTimeout = 30 * time.Minute

// EnsurePlan downloads whatever of a resolved Plan is missing into dir.
func EnsurePlan(ctx context.Context, dir string, p Plan, onProgress func(Progress)) error {
	return EnsureIn(ctx, dir, p.Assets, onProgress)
}

// Ensure downloads whatever is missing into dir, using the manifest that dir
// (and the environment) resolve to. It is safe to call when everything is
// already present (it becomes a hash check and returns nil).
//
// Downloads land in a .part file next to the destination and are renamed only
// after the digest matches, so an interrupted run never leaves a plausible-
// looking but corrupt asset behind — and a later run resumes the .part via a
// Range request instead of starting over.
func Ensure(ctx context.Context, dir string, onProgress func(Progress)) error {
	p, err := PlanFor(dir)
	if err != nil {
		return err
	}
	return EnsurePlan(ctx, dir, p, onProgress)
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

// copyWithProgress streams the body to dst, reporting progress and refusing to
// write past the pinned size. The clamp is not redundant with the digest check
// that follows it: a digest can only reject bytes already on disk, so without a
// ceiling a server answering with an endless stream would fill the disk before
// anything got verified.
func copyWithProgress(dst io.Writer, src io.Reader, already int64, a Asset, index, total int, onProgress func(Progress)) (int64, error) {
	buf := make([]byte, 512*1024)
	written := already
	lastReport := time.Time{}
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if written+int64(n) > a.Size {
				return written, fmt.Errorf("%s: response exceeds the pinned size of %d bytes", a.Name, a.Size)
			}
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
