//go:build desktop

package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNewRendererLogger_NilOnEmptyDataDir(t *testing.T) {
	if r := NewRendererLogger(""); r != nil {
		t.Errorf("expected nil for empty dataDir, got %+v", r)
	}
}

func TestRendererLogger_AppendsOneLinePerEntry(t *testing.T) {
	dir := t.TempDir()
	r := NewRendererLogger(dir)
	if r == nil {
		t.Fatal("logger should be non-nil")
	}
	t.Cleanup(func() { r.Close() })

	entries := []RendererLogEntry{
		{Level: "error", Message: "boom"},
		{Level: "warn", Message: "watch out", Context: json.RawMessage(`{"hook":"useChatStream"}`)},
		{Level: "info", Message: "all good"},
	}
	for _, e := range entries {
		if err := r.Append(e); err != nil {
			t.Fatalf("append %+v: %v", e, err)
		}
	}

	content := readLogFile(t, dir)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), content)
	}

	// Each line must be a single complete JSON object with the
	// required fields. Parsing failure on any line means the file
	// isn't grep-friendly anymore.
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d: %v\n%s", i, err, line)
			continue
		}
		if got["time"] == nil || got["time"] == "" {
			t.Errorf("line %d: missing time", i)
		}
		if got["level"] != entries[i].Level {
			t.Errorf("line %d: level %v, want %q", i, got["level"], entries[i].Level)
		}
		if got["message"] != entries[i].Message {
			t.Errorf("line %d: message %v, want %q", i, got["message"], entries[i].Message)
		}
	}
}

func TestRendererLogger_RejectsInvalid(t *testing.T) {
	r := NewRendererLogger(t.TempDir())
	t.Cleanup(func() { r.Close() })

	cases := []struct {
		name  string
		entry RendererLogEntry
	}{
		{"empty message", RendererLogEntry{Level: "error"}},
		{"empty level", RendererLogEntry{Message: "foo"}},
		{"unknown level", RendererLogEntry{Level: "debug", Message: "foo"}},
		{"typo level", RendererLogEntry{Level: "erorr", Message: "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.Append(tc.entry); err == nil {
				t.Errorf("expected error for %+v", tc.entry)
			}
		})
	}
}

func TestRendererLogger_NilLoggerIsNoop(t *testing.T) {
	var r *RendererLogger
	if err := r.Append(RendererLogEntry{Level: "error", Message: "boom"}); err != nil {
		t.Errorf("nil logger Append should no-op, got %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("nil logger Close should no-op, got %v", err)
	}
}

// TestRendererLogger_ConcurrentAppend pins that concurrent Append
// calls don't interleave a single line's bytes. Renderer in practice
// posts one at a time, but a stuck UI-loop spamming errors must not
// produce corrupted log lines that break grep.
func TestRendererLogger_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	r := NewRendererLogger(dir)
	t.Cleanup(func() { r.Close() })

	var wg sync.WaitGroup
	const N = 50
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Append(RendererLogEntry{
				Level:   "info",
				Message: "concurrent",
			})
		}(i)
	}
	wg.Wait()

	content := readLogFile(t, dir)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) != N {
		t.Fatalf("got %d lines, want %d", len(lines), N)
	}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d corrupted (likely interleaved write): %v\n%s", i, err, line)
		}
	}
}

func TestRendererLogger_RedactsSensitiveFieldsBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	r := NewRendererLogger(dir)
	t.Cleanup(func() { r.Close() })

	err := r.Append(RendererLogEntry{
		Level: "error",
		Message: "failed Authorization: Basic basic-secret Bearer bearer-secret " +
			"https://user:pass@example.com/path?client_secret=query-secret token=plain-token-secret",
		Context: json.RawMessage(`{
			"client_secret":"client-secret",
			"password":"password-secret",
			"token":"context-token-secret",
			"apikey":"api-secret",
			"token=key-token-secret":"key leak",
			"nested":{
				"refresh_token":"refresh-secret",
				"callback":"https://client:secret@example.net/callback"
			}
		}`),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	content := readLogFile(t, dir)
	for _, secret := range []string{
		"basic-secret",
		"bearer-secret",
		"user:pass",
		"query-secret",
		"plain-token-secret",
		"client-secret",
		"password-secret",
		"context-token-secret",
		"api-secret",
		"key-token-secret",
		"refresh-secret",
		"client:secret",
	} {
		if strings.Contains(content, secret) {
			t.Fatalf("renderer log leaked %q:\n%s", secret, content)
		}
	}
	if !strings.Contains(content, "[REDACTED]") {
		t.Fatalf("expected redaction marker in renderer log:\n%s", content)
	}
}

func readLogFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "logs", "renderer.log")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	return string(b)
}
