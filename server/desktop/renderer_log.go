//go:build desktop

package desktop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/natefinch/lumberjack"
)

// RendererLogger appends structured log entries from the renderer
// process to a rotated file under <dataDir>/logs/renderer.log.
//
// Why this exists: in a packaged build, console.error
// from the renderer evaporates — there's no devtools, no terminal,
// no Sentry. A user reporting "the app crashed" gives us nothing
// to debug from. Persisting renderer events to disk gives us at
// least a tail to ask the user for.
//
// Lumberjack handles rotation so the file can't grow unbounded;
// defaults below cap total on-disk space at ~15MB (5MB current
// file + 2 backups × 5MB).
//
// Concurrent access: lumberjack's Logger is safe for concurrent
// Write, and we hold a mutex around the marshal-and-write so a
// single record can't be interleaved with another. The renderer
// posts logs one at a time anyway, but a future batched
// endpoint would still be safe.
type RendererLogger struct {
	mu  sync.Mutex
	out *lumberjack.Logger
}

// NewRendererLogger opens the renderer log file under
// <dataDir>/logs/renderer.log, creating parent directories as
// needed. Returns nil if dataDir is empty (sidecar misconfigured;
// callers should no-op).
func NewRendererLogger(dataDir string) *RendererLogger {
	if dataDir == "" {
		return nil
	}
	return &RendererLogger{
		out: &lumberjack.Logger{
			Filename:   filepath.Join(dataDir, "logs", "renderer.log"),
			MaxSize:    5,    // megabytes per file before rotation
			MaxBackups: 2,    // keep 2 rotated files
			MaxAge:     14,   // days
			Compress:   true, // gzip backups
		},
	}
}

// RendererLogEntry is the wire shape POSTed by the renderer to
// /system/log. Level + Message are required; Context is an opaque
// JSON blob the renderer can use to attach hook state, request id,
// etc. without us needing a typed schema per event class.
type RendererLogEntry struct {
	Level   string          `json:"level"`             // "error" | "warn" | "info"
	Message string          `json:"message"`           // human-readable summary
	Context json.RawMessage `json:"context,omitempty"` // optional, opaque
}

var (
	rendererLogURLCredentialsRE    = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+(?::[^/\s@]*)?@`)
	rendererLogEphemeralTokenRE    = regexp.MustCompile(`(?i)(generated ephemeral token:\s*)\S+`)
	rendererLogLocalTokenEnvRE     = regexp.MustCompile(`(?i)(WORKMAX_LOCAL_TOKEN=)\S+`)
	rendererLogLocalTokenHeaderRE  = regexp.MustCompile(`(?i)(X-Local-Token[:=]\s*)\S+`)
	rendererLogAuthorizationRE     = regexp.MustCompile(`(?i)(Authorization:\s*(?:Bearer|Basic)\s+)\S+`)
	rendererLogBearerRE            = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	rendererLogBasicRE             = regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9._~+/=-]+`)
	rendererLogOAuthTokenRE        = regexp.MustCompile(`(?i)((?:access|refresh|id)_token["']?\s*[:=]\s*["']?)[^"',&\s]+`)
	rendererLogTokenRE             = regexp.MustCompile(`(?i)\b(token["']?\s*[:=]\s*["']?)[^"',&\s]+`)
	rendererLogAPIKeyRE            = regexp.MustCompile(`(?i)(api[_-]?key["']?\s*[:=]\s*["']?)[^"',&\s]+`)
	rendererLogAPIKeyCompactRE     = regexp.MustCompile(`(?i)(apikey["']?\s*[:=]\s*["']?)[^"',&\s]+`)
	rendererLogClientSecretRE      = regexp.MustCompile(`(?i)(client_secret["']?\s*[:=]\s*["']?)[^"',&\s]+`)
	rendererLogPasswordRE          = regexp.MustCompile(`(?i)(password["']?\s*[:=]\s*["']?)[^"',&\s]+`)
	rendererLogSecretRE            = regexp.MustCompile(`(?i)(secret["']?\s*[:=]\s*["']?)[^"',&\s]+`)
	rendererLogSensitiveContextKey = regexp.MustCompile(`(?i)^(authorization|x-local-token|workmax_local_token|access_token|refresh_token|id_token|token|api[_-]?key|apikey|client_secret|password|secret)$`)
)

// Append writes one entry as a single newline-terminated JSON line
// so log readers (jq, grep) can parse without state. Adds a
// server-side timestamp so renderer clock skew can't muddy the
// ordering for ops.
//
// Silent on validation failure (bad level / empty message) — the
// HTTP handler is the right place to 400 on garbage input; this
// is for callers that already validated.
func (r *RendererLogger) Append(entry RendererLogEntry) error {
	if r == nil || r.out == nil {
		return nil // no-op for unconfigured loggers
	}
	if entry.Message == "" {
		return fmt.Errorf("renderer log: message required")
	}
	if !validRendererLogLevel(entry.Level) {
		return fmt.Errorf("renderer log: invalid level %q (must be error|warn|info)", entry.Level)
	}

	wire := struct {
		Time    string          `json:"time"`
		Level   string          `json:"level"`
		Message string          `json:"message"`
		Context json.RawMessage `json:"context,omitempty"`
	}{
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
		Level:   entry.Level,
		Message: redactRendererLogString(entry.Message),
		Context: redactRendererLogContext(entry.Context),
	}
	line, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("renderer log: marshal: %w", err)
	}
	line = append(line, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	_, err = r.out.Write(line)
	return err
}

func redactRendererLogContext(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	encoded, err := json.Marshal(redactRendererLogValue(value, ""))
	if err != nil {
		return raw
	}
	return encoded
}

func redactRendererLogValue(value any, key string) any {
	if rendererLogSensitiveContextKey.MatchString(key) {
		return "[REDACTED]"
	}
	switch v := value.(type) {
	case string:
		return redactRendererLogString(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactRendererLogValue(item, "")
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for childKey, childValue := range v {
			out[redactRendererLogString(childKey)] = redactRendererLogValue(childValue, childKey)
		}
		return out
	default:
		return value
	}
}

func redactRendererLogString(value string) string {
	value = rendererLogURLCredentialsRE.ReplaceAllString(value, "${1}[REDACTED]@")
	value = rendererLogEphemeralTokenRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogLocalTokenEnvRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogLocalTokenHeaderRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogAuthorizationRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogBearerRE.ReplaceAllString(value, "Bearer [REDACTED]")
	value = rendererLogBasicRE.ReplaceAllString(value, "Basic [REDACTED]")
	value = rendererLogOAuthTokenRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogTokenRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogAPIKeyRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogAPIKeyCompactRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogClientSecretRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogPasswordRE.ReplaceAllString(value, "${1}[REDACTED]")
	value = rendererLogSecretRE.ReplaceAllString(value, "${1}[REDACTED]")
	return value
}

// Close flushes + closes the underlying file. Call on sidecar
// shutdown so the final entries reach disk.
func (r *RendererLogger) Close() error {
	if r == nil || r.out == nil {
		return nil
	}
	return r.out.Close()
}

func validRendererLogLevel(level string) bool {
	switch level {
	case "error", "warn", "info":
		return true
	}
	return false
}
