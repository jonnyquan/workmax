//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bootRendererLogServer stands up a sidecar with a RendererLogger
// pointed at the test's TempDir. Returns the base URL, the local
// token, and the data dir so the test can read the log file back.
func bootRendererLogServer(t *testing.T) (baseURL, tok, dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	logger := NewRendererLogger(dataDir)
	if logger == nil {
		t.Fatal("NewRendererLogger returned nil")
	}
	t.Cleanup(func() { logger.Close() })

	// Renderer log endpoint doesn't need DB, but ServerConfig demands
	// one. openHistoryTestDB is the cheapest option already wired here.
	db := openHistoryTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		RendererLogger: logger,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + srv.listener.Addr().String(), "tok", dataDir
}

func postLog(t *testing.T, base, tok string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/system/log", bytes.NewReader(buf))
	req.Header.Set("X-Local-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postRawLog(t *testing.T, base, tok string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/system/log", bytes.NewReader(body))
	req.Header.Set("X-Local-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHandleRendererLog_WritesToFile(t *testing.T) {
	base, tok, dataDir := bootRendererLogServer(t)

	resp := postLog(t, base, tok, RendererLogEntry{
		Level:   "error",
		Message: "useChatStream: aborted unexpectedly",
		Context: json.RawMessage(`{"thread":"u-1"}`),
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	content := readFile(t, filepath.Join(dataDir, "logs", "renderer.log"))
	if !strings.Contains(content, "useChatStream: aborted unexpectedly") {
		t.Errorf("log file missing message:\n%s", content)
	}
	if !strings.Contains(content, `"level":"error"`) {
		t.Errorf("log file missing level field:\n%s", content)
	}
	if !strings.Contains(content, `"thread":"u-1"`) {
		t.Errorf("log file missing context:\n%s", content)
	}
}

func TestHandleRendererLog_RedactsSecretsBeforeWriting(t *testing.T) {
	base, tok, dataDir := bootRendererLogServer(t)

	resp := postLog(t, base, tok, RendererLogEntry{
		Level:   "error",
		Message: "request failed Authorization: Bearer access-secret at https://user:pass@example.com/path",
		Context: json.RawMessage(`{
			"Authorization":"Bearer header-secret",
			"X-Local-Token":"local-secret",
			"nested":{
				"https://client:secret@example.net/callback":"key leak",
				"access_token=key-secret":"key leak",
				"refresh_token":"refresh-secret",
				"callback":"https://client:secret@example.org/callback",
				"api_key":"api-secret"
			}
		}`),
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	content := readFile(t, filepath.Join(dataDir, "logs", "renderer.log"))
	for _, leaked := range []string{
		"access-secret",
		"user:pass",
		"header-secret",
		"local-secret",
		"refresh-secret",
		"client:secret",
		"key-secret",
		"api-secret",
	} {
		if strings.Contains(content, leaked) {
			t.Fatalf("renderer.log leaked %q:\n%s", leaked, content)
		}
	}
	for _, want := range []string{
		"Bearer [REDACTED]",
		"https://[REDACTED]@example.com/path",
		`"Authorization":"[REDACTED]"`,
		`"X-Local-Token":"[REDACTED]"`,
		"https://[REDACTED]@example.net/callback",
		`"access_token=[REDACTED]"`,
		`"refresh_token":"[REDACTED]"`,
		"https://[REDACTED]@example.org/callback",
		`"api_key":"[REDACTED]"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("renderer.log missing redacted value %q:\n%s", want, content)
		}
	}
}

func TestHandleRendererLog_RejectsInvalid(t *testing.T) {
	base, tok, _ := bootRendererLogServer(t)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"unknown level", RendererLogEntry{Level: "debug", Message: "x"}, http.StatusBadRequest},
		{"empty message", RendererLogEntry{Level: "error"}, http.StatusBadRequest},
		{"bad json", "not an object", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postLog(t, base, tok, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status: %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestHandleRendererLog_EmptyBodyIsAccepted(t *testing.T) {
	base, tok, _ := bootRendererLogServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/system/log", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Empty body must not 400 — that would risk a log-call-failed
	// recursion if the renderer hits a race during teardown.
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("empty body should 204, got %d", resp.StatusCode)
	}
}

func TestHandleRendererLog_RequiresLocalToken(t *testing.T) {
	base, _, dataDir := bootRendererLogServer(t)

	body := []byte(`{"level":"error","message":"must not write"}`)
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "missing token"},
		{name: "wrong token", token: "wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, base+"/system/log", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("X-Local-Token", tc.token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status: %d, want %d", resp.StatusCode, http.StatusForbidden)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dataDir, "logs", "renderer.log")); !os.IsNotExist(err) {
		t.Fatalf("unauthorized log request should not write renderer.log; stat err=%v", err)
	}
}

func TestHandleRendererLog_RejectsOversizeBody(t *testing.T) {
	base, tok, dataDir := bootRendererLogServer(t)

	body := append(
		[]byte(`{"level":"error","message":"oversize"}`),
		bytes.Repeat([]byte(" "), (64<<10)+1)...,
	)
	resp := postRawLog(t, base, tok, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "logs", "renderer.log")); !os.IsNotExist(err) {
		t.Fatalf("oversize body should not write renderer.log; stat err=%v", err)
	}
}

func TestHandleRendererLog_503WhenNotConfigured(t *testing.T) {
	db := openHistoryTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		// No RendererLogger — handler must 503.
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	base := "http://" + srv.listener.Addr().String()
	resp := postLog(t, base, "tok", RendererLogEntry{Level: "error", Message: "x"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d, want 503", resp.StatusCode)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
