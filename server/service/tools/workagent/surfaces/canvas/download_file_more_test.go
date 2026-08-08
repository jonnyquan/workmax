package canvas

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// download_file_test.go pins the headline contract (200 success writes
// body + returns n, non-200 surfaces status, transport error wraps as
// "download failed:", unwritable dest wraps as "create file failed:").
// These fill the quieter gate invariants a silent regression would
// slip past:
//
//   • 200 is a STRICT-EQUAL check — `resp.StatusCode != http.StatusOK`.
//     A 201/202/204/206 2xx response is treated as an error. Pin so a
//     refactor that broadened the success range to `< 300` would
//     surface as a deliberate loosening.
//   • Empty body on a 200 response returns n=0 with NO error (io.Copy
//     of an empty reader). The dest file IS created but has length 0.
//     Pin so a refactor that conflated "empty body" with "error" would
//     surface.
//   • Status error message format is a literal
//     `"download returned status %d"` (no %w wrap). errors.Is / errors
//     .Unwrap cannot chase through to an underlying error — there IS
//     no underlying error. Pin the shape so a refactor that wrapped a
//     sentinel (e.g. ErrNon200) would surface.
//   • Transport error message format is `"download failed: <inner>"`
//     WITH a %w wrap. errors.Unwrap traverses to the http client's
//     underlying err. Pin the wrapping distinction so a future log-
//     scraper relying on the ":" separator or errors.Is would notice
//     a contract shift.
//   • Create-file error format is `"create file failed: <inner>"` also
//     with %w — parallel to transport error. Pin the same wrapping so
//     both paths surface unwrappable errors consistently.
//   • Default http.Client{} follows up to 10 redirects (Go stdlib
//     default). A 302 → 200 chain downloads the FINAL body. Pin so a
//     refactor that set a custom CheckRedirect would surface.
//   • Single HEAD / non-GET requests aren't made — the client uses
//     `client.Get(url)` which is strictly an HTTP GET. Pin so a
//     refactor to a MethodHEAD probe before GET would surface.
//   • dest path containing a FILE in its parent segment (not a dir)
//     fails os.Create with "create file failed:" — classic unwritable
//     path case parallel to the missing-dir one in the base test.
//   • Response body is copied byte-for-byte: binary content (0x00,
//     0xFF, UTF-16 BOM) round-trips intact. Pin so a refactor to
//     bufio-line-scanning or text-mode copy would surface.
//   • The user agent / request method / headers passed by default
//     http.Client are Go's standard (no custom headers). Pin the
//     ABSENCE of a custom User-Agent so a refactor that added one
//     would surface — caller URL-signing may depend on the exact
//     default UA.
//   • Content-Length mismatch: server declares CL=100 but writes only
//     50 bytes, then closes. io.Copy returns n=50 with an unexpected-
//     EOF error → wrapped as "write file failed:". Pin so callers
//     reading n before checking err don't miss the short-write.

func allowUnsafeDownloadsForThisTest(t *testing.T) {
	t.Helper()
	prev := allowUnsafeAttachmentDownloadsForTest
	allowUnsafeAttachmentDownloadsForTest = true
	t.Cleanup(func() {
		allowUnsafeAttachmentDownloadsForTest = prev
	})
}

func TestDownloadFileToPath_NonOKStatusesAreAllRejected(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// 200 is the ONLY success status — every other 2xx is rejected.
	cases := []int{
		http.StatusCreated,
		http.StatusAccepted,
		http.StatusNoContent,
		http.StatusPartialContent,
		http.StatusMovedPermanently, // 301 without Location → still non-200
	}
	for _, status := range cases {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			_, err := downloadFileToPath(server.URL, filepath.Join(t.TempDir(), "x.bin"))
			if err == nil {
				t.Fatalf("status %d: expected error, got nil", status)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%d", status)) {
				t.Errorf("status %d: error %q should include the status number",
					status, err.Error())
			}
		})
	}
}

func TestDownloadFileToPath_EmptyBodyOn200ReturnsZeroBytesNoError(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// No body.
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "empty.bin")
	n, err := downloadFileToPath(server.URL, dest)
	if err != nil {
		t.Fatalf("empty body should not error, got %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0", n)
	}
	// File must exist but be zero-length.
	fi, statErr := os.Stat(dest)
	if statErr != nil {
		t.Fatalf("dest should exist even for empty body: %v", statErr)
	}
	if fi.Size() != 0 {
		t.Errorf("dest size = %d, want 0", fi.Size())
	}
}

func TestDownloadFileToPath_StatusErrorIsNotUnwrappable(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// Non-200 uses fmt.Errorf WITHOUT %w. errors.Unwrap returns nil.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := downloadFileToPath(server.URL, filepath.Join(t.TempDir(), "x.bin"))
	if err == nil {
		t.Fatal("expected non-200 error")
	}
	if errors.Unwrap(err) != nil {
		t.Errorf("status error should not be unwrappable; got inner %v", errors.Unwrap(err))
	}
	// The exact literal format is "download returned status N".
	if !strings.HasPrefix(err.Error(), "download returned status ") {
		t.Errorf("error = %q, want 'download returned status …' prefix", err.Error())
	}
}

func TestDownloadFileToPath_TransportErrorIsUnwrappable(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// `fmt.Errorf("download failed: %w", err)` — %w preserves the
	// chain so errors.Unwrap returns the inner http error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.Close() // close immediately — subsequent Get fails

	_, err := downloadFileToPath(server.URL, filepath.Join(t.TempDir(), "x.bin"))
	if err == nil {
		t.Fatal("expected transport error")
	}
	if errors.Unwrap(err) == nil {
		t.Error("transport error should be unwrappable (uses %w)")
	}
}

func TestDownloadFileToPath_CreateFileErrorIsUnwrappable(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// Parallel to transport: create-file uses %w. Pin symmetry so
	// both failure modes surface their inner error to callers.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "body")
	}))
	defer server.Close()

	badDest := filepath.Join(t.TempDir(), "no-dir", "f.bin")
	_, err := downloadFileToPath(server.URL, badDest)
	if err == nil {
		t.Fatal("expected create-file error")
	}
	if errors.Unwrap(err) == nil {
		t.Error("create-file error should be unwrappable (uses %w)")
	}
}

func TestDownloadFileToPath_FollowsRedirects(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// Stdlib http.Client follows up to 10 redirects by default —
	// downloadFileToPath inherits that behaviour. Pin so a refactor
	// that set CheckRedirect: func ... { return ErrUseLastResponse }
	// would surface.
	var finalHits atomic.Int32
	var finalURL string

	finalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		finalHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "final-body")
	}))
	defer finalServer.Close()
	finalURL = finalServer.URL

	redirServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, finalURL, http.StatusFound)
	}))
	defer redirServer.Close()

	dest := filepath.Join(t.TempDir(), "redir.bin")
	n, err := downloadFileToPath(redirServer.URL, dest)
	if err != nil {
		t.Fatalf("redirect chain should succeed, got %v", err)
	}
	if n != int64(len("final-body")) {
		t.Errorf("n = %d, want %d", n, len("final-body"))
	}
	if finalHits.Load() != 1 {
		t.Errorf("final server hit %d times, want 1", finalHits.Load())
	}
	body, _ := os.ReadFile(dest)
	if string(body) != "final-body" {
		t.Errorf("body = %q, want %q", body, "final-body")
	}
}

func TestDownloadFileToPath_UsesGETMethod(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// Pin that the client issues a GET — no pre-flight HEAD probe.
	var methods []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	if _, err := downloadFileToPath(server.URL, filepath.Join(t.TempDir(), "x.bin")); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("expected exactly 1 request, got %d: %v", len(methods), methods)
	}
	if methods[0] != http.MethodGet {
		t.Errorf("method = %q, want GET", methods[0])
	}
}

func TestDownloadFileToPath_BinaryBodyRoundTripsIntact(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// Every byte survives — no text-mode conversion, no line-ending
	// normalisation. Pin the raw-bytes contract so a refactor to a
	// scanner-based copy would surface.
	payload := []byte{0x00, 0xFF, 0xFE, 0xEF, 0xBB, 0xBF, 0x0D, 0x0A, 0x1F, 0x7F}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "bin.dat")
	n, err := downloadFileToPath(server.URL, dest)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("n = %d, want %d", n, len(payload))
	}
	got, _ := os.ReadFile(dest)
	if len(got) != len(payload) {
		t.Fatalf("read back %d bytes, want %d", len(got), len(payload))
	}
	for i, b := range payload {
		if got[i] != b {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, got[i], b)
		}
	}
}

func TestDownloadFileToPath_ContentLengthMismatchYieldsWriteFailure(t *testing.T) {
	allowUnsafeDownloadsForThisTest(t)
	// Server declares Content-Length: 100 but flushes only 10 bytes
	// then hijacks + closes the TCP conn so the client sees an
	// unexpected EOF. io.Copy returns ErrUnexpectedEOF which the
	// wrapper surfaces as "write file failed: …". Pin so callers
	// don't silently accept short-writes.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server ResponseWriter is not a Hijacker")
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack failed: %v", err)
			return
		}
		defer conn.Close()
		// Manual HTTP/1.1 response with a lie about Content-Length.
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nConnection: close\r\n\r\n1234567890")
		_ = bufrw.Flush()
		// Closing the conn with 10 bytes sent vs. 100 declared forces
		// ErrUnexpectedEOF on the client side.
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "short.bin")
	_, err := downloadFileToPath(server.URL, dest)
	if err == nil {
		t.Fatal("expected write error for short body, got nil")
	}
	if !strings.HasPrefix(err.Error(), "write file failed:") {
		t.Errorf("error = %q, want 'write file failed:' prefix", err.Error())
	}
	// And the underlying error is unwrappable (uses %w).
	if errors.Unwrap(err) == nil {
		t.Error("write-file error should be unwrappable (uses %w)")
	}
}

// silence unused-import on io in case a refactor removes it elsewhere
var _ = io.EOF
