//go:build desktop

package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func fmtSscan(s string, out *int64) (int, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	*out = v
	return 1, nil
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// rangeServer serves body, honoring a single open-ended Range request so the
// resume path is exercised the way a real CDN would answer it.
func rangeServer(t *testing.T, body []byte, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			var from int64
			if _, err := fmtSscan(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"), &from); err == nil && from < int64(len(body)) {
				w.Header().Set("Content-Range", "bytes "+rng[6:]+"/"+itoa(int64(len(body))))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[from:])
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

func TestEnsureInDownloadsVerifiesAndInstalls(t *testing.T) {
	body := []byte(strings.Repeat("workmax-onnx-payload", 1000))
	srv := rangeServer(t, body, nil)
	defer srv.Close()

	dir := t.TempDir()
	list := []Asset{{
		Name:       "libonnxruntime",
		Path:       "libonnxruntime.dylib",
		URL:        srv.URL,
		SHA256:     digest(body),
		Size:       int64(len(body)),
		Executable: true,
	}}

	var lastProgress Progress
	if err := EnsureIn(context.Background(), dir, list, func(p Progress) { lastProgress = p }); err != nil {
		t.Fatalf("EnsureIn: %v", err)
	}

	dest := filepath.Join(dir, "libonnxruntime.dylib")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read installed asset: %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("installed asset content differs from served body")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable asset should carry the exec bit, mode=%v", info.Mode())
	}
	if lastProgress.Downloaded != int64(len(body)) {
		t.Fatalf("final progress = %d bytes, want %d", lastProgress.Downloaded, len(body))
	}
	// No .part should survive a successful install.
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part file should be gone after install, stat err=%v", err)
	}
}

func TestEnsureInIsNoOpWhenAlreadyInstalled(t *testing.T) {
	body := []byte("already here")
	hits := 0
	srv := rangeServer(t, body, &hits)
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.bin"), body, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	list := []Asset{{Name: "model", Path: "model.bin", URL: srv.URL, SHA256: digest(body), Size: int64(len(body))}}

	if err := EnsureIn(context.Background(), dir, list, nil); err != nil {
		t.Fatalf("EnsureIn: %v", err)
	}
	if hits != 0 {
		t.Fatalf("server was contacted %d times; an intact asset must not be re-downloaded", hits)
	}
}

func TestEnsureInRejectsDigestMismatch(t *testing.T) {
	body := []byte("tampered payload")
	srv := rangeServer(t, body, nil)
	defer srv.Close()

	dir := t.TempDir()
	list := []Asset{{
		Name:   "model",
		Path:   "knowledge/model.onnx",
		URL:    srv.URL,
		SHA256: digest([]byte("what we actually expected")),
		Size:   int64(len(body)),
	}}

	err := EnsureIn(context.Background(), dir, list, nil)
	if err == nil {
		t.Fatal("EnsureIn should fail when the digest does not match")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("error = %q, want a sha256 mismatch", err)
	}
	// Nothing plausible-looking may be left behind for a later run to trust.
	for _, p := range []string{"knowledge/model.onnx", "knowledge/model.onnx.part"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist after a mismatch, stat err=%v", p, err)
		}
	}
}

func TestEnsureInResumesPartialDownload(t *testing.T) {
	body := []byte(strings.Repeat("resume-me", 500))
	srv := rangeServer(t, body, nil)
	defer srv.Close()

	dir := t.TempDir()
	// Simulate an interrupted run that got the first half.
	half := len(body) / 2
	if err := os.WriteFile(filepath.Join(dir, "model.bin.part"), body[:half], 0o644); err != nil {
		t.Fatalf("seed part: %v", err)
	}
	list := []Asset{{Name: "model", Path: "model.bin", URL: srv.URL, SHA256: digest(body), Size: int64(len(body))}}

	if err := EnsureIn(context.Background(), dir, list, nil); err != nil {
		t.Fatalf("EnsureIn: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("resumed download did not reconstruct the original content")
	}
}

func TestMissingInReportsCorruptAsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.bin"), []byte("truncated"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	list := []Asset{{Name: "model", Path: "model.bin", SHA256: digest([]byte("the whole thing")), Size: 15}}

	missing, err := MissingIn(dir, list)
	if err != nil {
		t.Fatalf("MissingIn: %v", err)
	}
	if len(missing) != 1 {
		t.Fatalf("missing = %d assets, want 1 (a corrupt file must be replaced, not used)", len(missing))
	}
}

// The embedded manifest ships with no platforms, and that has to stay a
// deliberate, legible state rather than a silent failure: the sentinel is what
// lets the sidecar say "nothing is pinned here" instead of "something broke".
func TestPlanForReportsUnpinnedPlatformExplicitly(t *testing.T) {
	t.Setenv(ManifestPathEnv, "")
	p, err := PlanFor(t.TempDir())
	if err == nil {
		t.Skip("the embedded manifest now pins this platform; this guard is obsolete")
	}
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("err = %v, want ErrUnsupportedPlatform", err)
	}
	if p.Origin != "embedded" {
		t.Errorf("origin = %q, want embedded", p.Origin)
	}
	if p.Platform == "" {
		t.Error("the platform key must be reported even when nothing is pinned")
	}
}

func writeManifest(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func manifestBody(t *testing.T, size int64, url string) string {
	t.Helper()
	return `{"schemaVersion":1,"platforms":{"` + runtime.GOOS + `/` + runtime.GOARCH + `":[
		{"name":"model","path":"knowledge/model.onnx","url":"` + url + `","sha256":"` + digest([]byte("x")) + `","size":` + itoa(size) + `}
	]}}`
}

// A manifest the repository does not ship is how an internal build supplies
// hosting without those URLs living in the open-source tree.
func TestPlanForPrefersEnvManifestThenDirThenEmbedded(t *testing.T) {
	dir := t.TempDir()

	// 1. Nothing supplied → embedded (which pins nothing).
	t.Setenv(ManifestPathEnv, "")
	if _, err := PlanFor(dir); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("bare PlanFor = %v, want ErrUnsupportedPlatform", err)
	}

	// 2. A manifest beside the user's own assets wins over the embedded one.
	dirManifest := filepath.Join(dir, ManifestFileName)
	writeManifest(t, dirManifest, manifestBody(t, 10, "https://example.invalid/dir"))
	p, err := PlanFor(dir)
	if err != nil {
		t.Fatalf("PlanFor with a dir manifest: %v", err)
	}
	if p.Origin != dirManifest {
		t.Errorf("origin = %q, want %q", p.Origin, dirManifest)
	}
	if len(p.Assets) != 1 || p.Assets[0].URL != "https://example.invalid/dir" {
		t.Fatalf("assets = %+v, want the dir manifest's pin", p.Assets)
	}

	// 3. The env override beats both.
	envManifest := filepath.Join(t.TempDir(), "override.json")
	writeManifest(t, envManifest, manifestBody(t, 20, "https://example.invalid/env"))
	t.Setenv(ManifestPathEnv, envManifest)
	p, err = PlanFor(dir)
	if err != nil {
		t.Fatalf("PlanFor with an env manifest: %v", err)
	}
	if p.Origin != envManifest {
		t.Errorf("origin = %q, want the env override %q", p.Origin, envManifest)
	}
	if p.TotalBytes != 20 {
		t.Errorf("total = %d, want 20", p.TotalBytes)
	}

	// A named-but-missing override is an error, not a quiet fall-through: the
	// operator asked for a specific pinning and did not get it.
	t.Setenv(ManifestPathEnv, filepath.Join(t.TempDir(), "absent.json"))
	if _, err := PlanFor(dir); err == nil {
		t.Error("a missing env manifest must fail loudly, not fall back")
	}
}

// A manifest is trusted to name files, not to be unbounded.
func TestPlanForRejectsOversizedManifest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ManifestPathEnv, "")
	writeManifest(t, filepath.Join(dir, ManifestFileName), manifestBody(t, MaxTotalBytes+1, "https://example.invalid/big"))
	if _, err := PlanFor(dir); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("PlanFor = %v, want ErrTooLarge", err)
	}
}

func TestPlanForRejectsUnpinnedEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ManifestPathEnv, "")
	writeManifest(t, filepath.Join(dir, ManifestFileName),
		`{"schemaVersion":1,"platforms":{"`+runtime.GOOS+`/`+runtime.GOARCH+`":[{"name":"model","path":"m.onnx","url":"https://example.invalid/m"}]}}`)
	if _, err := PlanFor(dir); err == nil || errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("PlanFor = %v, want a complaint about the missing sha256/size", err)
	}
}

// The digest check can only reject bytes already written. A server that streams
// forever has to be stopped before it fills the disk.
func TestEnsureInStopsAnOversizedResponse(t *testing.T) {
	body := []byte(strings.Repeat("way-too-much", 5000))
	srv := rangeServer(t, body, nil)
	defer srv.Close()

	dir := t.TempDir()
	list := []Asset{{Name: "model", Path: "model.bin", URL: srv.URL, SHA256: digest(body), Size: 64}}

	err := EnsureIn(context.Background(), dir, list, nil)
	if err == nil {
		t.Fatal("EnsureIn accepted a response larger than the pinned size")
	}
	if !strings.Contains(err.Error(), "exceeds the pinned size") {
		t.Fatalf("error = %q, want a size-cap refusal", err)
	}
	info, statErr := os.Stat(filepath.Join(dir, "model.bin.part"))
	if statErr == nil && info.Size() > 64 {
		t.Fatalf(".part grew to %d bytes past the %d-byte cap", info.Size(), 64)
	}
}
