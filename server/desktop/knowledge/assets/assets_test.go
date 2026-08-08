//go:build desktop

package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestCurrentReportsUnsupportedPlatformWhileManifestIsEmpty(t *testing.T) {
	// The manifest ships empty until the embedding model has a pinned public
	// URL. Until then RAG must degrade cleanly rather than half-install.
	if _, err := Current(); err == nil {
		t.Skip("manifest now pins this platform; this guard test is obsolete")
	}
}
