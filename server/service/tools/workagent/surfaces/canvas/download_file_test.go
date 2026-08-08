package canvas

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// downloadFileToPath is the URL-based fetch path inside
// PrepareAttachments. It's a thin wrapper around net/http, but the
// error wrapping is load-bearing: the caller logs the message, and a
// future change that rewrites the error to a generic "download failed"
// without the status code would erase signal from the prod logs.

func TestDownloadFileToPath_RejectsLoopbackURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "should-not-exist.bin")
	_, err := downloadFileToPath(server.URL, dest)
	if err == nil {
		t.Fatal("expected error on loopback URL, got nil")
	}
	if !strings.Contains(err.Error(), "private or reserved") {
		t.Errorf("error message should reject private/reserved host; got %q", err.Error())
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("destination file should not exist for rejected URL; stat err = %v", statErr)
	}
}

func TestDownloadFileToPath_RejectsNonHTTPURL(t *testing.T) {
	_, err := downloadFileToPath("file:///etc/passwd", filepath.Join(t.TempDir(), "x.bin"))
	if err == nil {
		t.Fatal("expected URL scheme error, got nil")
	}
	if !strings.Contains(err.Error(), "http or https") {
		t.Errorf("error should mention allowed schemes; got %q", err.Error())
	}
}

// validateSafeAttachmentURL was inlined into server/utils/safehttp.
// Equivalent coverage lives in safehttp/safehttp_test.go's
// TestValidateURL_RejectsUnsafe under the "creds in url" case.
