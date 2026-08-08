//go:build desktop

package cloud_proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const maxSSEFrameBytes = 1 << 20 // 1 MiB

// ErrSSEFrameTooLarge is returned before an upstream frame can accumulate
// without bound. Scanner.Buffer limits one line; this separate frame budget
// limits the sum of arbitrarily many data/comment/metadata lines between blank
// delimiters.
var ErrSSEFrameTooLarge = errors.New("cloud proxy: SSE frame exceeds size limit")

// ErrSSEStreamTruncated is returned when an otherwise clean HTTP body EOF is
// reached without a terminal done event. A 200 response ending at EOF is not a
// complete WorkAgent turn: treating it as success would mark a partially
// cached answer complete and hide the retry affordance from the Renderer.
var ErrSSEStreamTruncated = errors.New("cloud proxy: SSE stream ended before done event")

// SSEEvent is the parsed shape of one Server-Sent Events frame from
// workmax.app. We carry the raw `event:` and `data:` lines as-is so
// downstream Writer can re-frame them verbatim — there's no per-event
// transformation in P0; the sidecar is a transparent relay.
type SSEEvent struct {
	Type string // event: <type>; empty preserves a data-only upstream frame
	Data string // data: <line(s)> joined by '\n'
}

// SSEWriter is the abstraction the relay writes into. Production
// implementation wraps gin.Context.Writer (+ http.Flusher); tests
// substitute a simple sync.Mutex-guarded buffer.
//
// Write returns an error if the underlying connection is dead; the
// relay treats that as "renderer disconnected" and cancels upstream.
type SSEWriter interface {
	WriteEvent(ev SSEEvent) error
	WriteProxyError(pe ProxyError) error
	// WriteKeepalive emits an SSE comment line (`: keepalive\n\n`).
	// Per the SSE spec any line starting with ":" is a comment and
	// is ignored by EventSource clients, so this keeps the TCP
	// connection visibly alive without confusing the renderer's
	// event parser. Used by handleAgentChat's keepalive ticker
	// when upstream stalls for >30s.
	WriteKeepalive() error
}

// GinSSEWriter is the production SSEWriter, wrapping a Gin response.
// Bound by the http.Flusher; the relay flushes after every event so
// the renderer doesn't see chunks coalesced by a proxy buffer.
type GinSSEWriter struct {
	mu      sync.Mutex
	w       io.Writer
	flusher interface{ Flush() }
}

// NewGinSSEWriter wires a relay output. Caller MUST set the SSE
// response headers BEFORE constructing this — the writer doesn't
// touch headers itself (callers may want to add custom ones).
func NewGinSSEWriter(w io.Writer, flusher interface{ Flush() }) *GinSSEWriter {
	return &GinSSEWriter{w: w, flusher: flusher}
}

// WriteEvent writes a single SSE frame using the canonical
//
//	event: <type>\n
//	data: <data>\n
//	\n
//
// shape and flushes. Concurrent calls are serialized.
func (g *GinSSEWriter) WriteEvent(ev SSEEvent) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ev.Type != "" {
		if _, err := fmt.Fprintf(g.w, "event: %s\n", ev.Type); err != nil {
			return err
		}
	}
	// Per the SSE spec, multi-line data values become one `data:` line
	// per source line. The upstream already emits this shape; we just
	// preserve it.
	for _, line := range strings.Split(ev.Data, "\n") {
		if _, err := fmt.Fprintf(g.w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(g.w, "\n"); err != nil {
		return err
	}
	if g.flusher != nil {
		g.flusher.Flush()
	}
	return nil
}

// WriteKeepalive emits a `: keepalive\n\n` SSE comment line and
// flushes. Holds the writer mutex so an in-flight WriteEvent /
// WriteProxyError isn't interrupted mid-frame. Returns the
// underlying connection error (typically a "broken pipe" if the
// renderer hung up); the keepalive ticker treats any error as
// "renderer gone" and stops emitting.
func (g *GinSSEWriter) WriteKeepalive() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := io.WriteString(g.w, ": keepalive\n\n"); err != nil {
		return err
	}
	if g.flusher != nil {
		g.flusher.Flush()
	}
	return nil
}

// WriteProxyError emits a `proxy_error` SSE event with the JSON
// representation of the error. Callers should typically follow this
// with a stream close — the renderer treats proxy_error as terminal.
func (g *GinSSEWriter) WriteProxyError(pe ProxyError) error {
	raw, err := json.Marshal(pe)
	if err != nil {
		// Best-effort fallback: emit a coarse error rather than panic.
		raw = []byte(`{"kind":"unknown","message":"proxy_error marshal failed"}`)
	}
	return g.WriteEvent(SSEEvent{Type: "proxy_error", Data: string(raw)})
}

// PipeUpstream reads SSE frames from src and forwards them to dst,
// simultaneously feeding the cache writer. Returns nil only after a
// terminal `done` event; EOF before done returns ErrSSEStreamTruncated so
// the caller can preserve a partial cache row and expose retry.
//
// Cancellation honored at frame boundaries — checked before every
// scanner.Scan() iteration. Renderer-side disconnect surfaces as
// dst.WriteEvent returning an error → we return that error so Chat()
// can cancel upstream too.
//
// Maximum frame size: 1 MiB. SSE events from workmax can be large
// (whole code blocks in one frame); 1 MiB is well above what the
// production handler ever emits and bounds memory for adversarial
// upstream behavior.
func PipeUpstream(
	ctx context.Context,
	src io.Reader,
	dst SSEWriter,
	cache *CacheWriter,
) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), maxSSEFrameBytes)
	// SSE frames are delimited by a blank line (\n\n). We can't use
	// the default token-by-line scanner directly; assemble frames
	// manually inside the loop instead.

	var (
		curEvent  string
		curData   strings.Builder
		frameSize int
	)
	flushFrame := func() (bool, error) {
		// A frame with neither event nor data is a heartbeat — skip.
		if curEvent == "" && curData.Len() == 0 {
			frameSize = 0
			return false, nil
		}
		ev := SSEEvent{Type: curEvent, Data: curData.String()}
		curEvent = ""
		curData.Reset()
		frameSize = 0
		logicalType, err := handleFrame(ctx, ev, dst, cache)
		if err != nil {
			return false, err
		}
		return logicalType == "done", nil
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			// End-of-frame marker.
			terminal, err := flushFrame()
			if err != nil {
				return err
			}
			// A successfully forwarded done event ends the relay. Do not keep
			// reading or accept frames an upstream sends after its terminal event.
			if terminal {
				return nil
			}
			continue
		}
		// Include one byte for the consumed line delimiter. Counting every field,
		// including comments and ignored metadata, also bounds a malicious
		// never-terminated frame without retaining those fields in memory.
		if len(line) > maxSSEFrameBytes-frameSize-1 {
			return ErrSSEFrameTooLarge
		}
		frameSize += len(line) + 1
		if strings.HasPrefix(line, ":") {
			// Comment line (keepalive). Ignore.
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			curEvent = strings.TrimSpace(line[len("event: "):])
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			if curData.Len() > 0 {
				curData.WriteByte('\n')
			}
			curData.WriteString(line[len("data: "):])
			continue
		}
		// Other field names (id:, retry:) — pass through as a synthetic
		// data line so we don't drop information from upstream.
		// Production workmax doesn't emit these, but harmless to preserve.
	}
	if err := scanner.Err(); err != nil {
		// Final partial frame is intentionally not flushed — incomplete
		// frame on stream interruption isn't safe to forward.
		return err
	}
	if err := ctx.Err(); err != nil {
		// A body closed by caller/session cancellation is not an upstream
		// truncation and must not manufacture a retryable proxy_error.
		return err
	}
	// Trailing frame without explicit blank line (unusual but legal).
	terminal, err := flushFrame()
	if err != nil {
		return err
	}
	if terminal {
		return nil
	}
	return ErrSSEStreamTruncated
}

// handleFrame forwards one parsed event downstream + records it.
// Errors from either side bubble up so PipeUpstream can return them
// to Chat() for classification.
func handleFrame(
	ctx context.Context,
	ev SSEEvent,
	dst SSEWriter,
	cache *CacheWriter,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	logicalType := logicalSSEEventType(ev)
	// 1. Forward to renderer first. Backpressure flows from this write
	//    back through the scanner — slow renderer slows upstream read,
	//    which is what we want (otherwise workmax keeps billing tokens
	//    while we buffer downstream).
	if err := dst.WriteEvent(ev); err != nil {
		return "", fmt.Errorf("downstream write: %w", err)
	}
	// 2. Record in cache. Pull the per-event text fragment out of the
	//    JSON-shaped data when possible; otherwise treat the whole
	//    data string as the fragment.
	if cache != nil {
		fragment := extractTextFragmentForType(ev, logicalType)
		if logicalType == "done" {
			disposition := classifyDoneForCache(ev)
			if disposition.partial {
				cache.MarkPartial()
			}
			if disposition.skip {
				return logicalType, nil
			}
			if disposition.replayText != "" {
				fragment = disposition.replayText
			}
		}
		if err := cache.Enqueue(logicalType, fragment); err != nil {
			// Cache failure is non-fatal — we keep relaying so the
			// renderer still sees the turn. Future telemetry can count
			// these and alert without changing relay behavior.
			_ = err
		}
	}
	return logicalType, nil
}

type doneCacheDisposition struct {
	replayText string
	partial    bool
	skip       bool
}

type agentDoneResult struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Code    string          `json:"code"`
	IsError bool            `json:"is_error"`
	Error   json.RawMessage `json:"error"`
	Result  json.RawMessage `json:"result"`
}

// classifyDoneForCache understands only the canonical legacy terminal shapes
// needed for safe local persistence. The original frame is always forwarded
// unchanged. Busy/error terminals never create a row; an already_processed
// terminal may carry the prior answer as a nested result string and therefore
// safely refresh the existing stable-key row.
func classifyDoneForCache(ev SSEEvent) doneCacheDisposition {
	var outer agentDoneResult
	if err := json.Unmarshal([]byte(ev.Data), &outer); err != nil {
		return doneCacheDisposition{}
	}
	candidates := []agentDoneResult{outer}
	if len(outer.Result) > 0 && strings.HasPrefix(strings.TrimSpace(string(outer.Result)), "{") {
		var nested agentDoneResult
		if err := json.Unmarshal(outer.Result, &nested); err == nil {
			candidates = append(candidates, nested)
		}
	}
	for _, candidate := range candidates {
		busy := candidate.Code == "THREAD_BUSY" || candidate.Subtype == "thread_busy"
		hasError := len(candidate.Error) > 0 && string(candidate.Error) != "null" && string(candidate.Error) != `""`
		if busy || candidate.IsError || hasError || (candidate.Code != "" && candidate.Code != "OK") {
			return doneCacheDisposition{partial: true, skip: true}
		}
		if candidate.Subtype == "already_processed" && len(candidate.Result) > 0 {
			var replayText string
			if err := json.Unmarshal(candidate.Result, &replayText); err == nil {
				return doneCacheDisposition{replayText: replayText}
			}
		}
	}
	return doneCacheDisposition{}
}

// logicalSSEEventType returns the protocol event type used for terminal and
// cache semantics without changing the frame forwarded to the Renderer.
// Older cloud versions emitted explicit `event: text` / `event: done` fields;
// the current Go Server emits data-only AgentSSEEvent JSON envelopes such as
// {"type":"block",...} and {"type":"done",...}.
func logicalSSEEventType(ev SSEEvent) string {
	// An explicit non-default event field remains authoritative for backward
	// compatibility. `event: message` is the SSE default and may still wrap
	// the newer JSON envelope, so inspect its data like a data-only frame.
	if ev.Type != "" && ev.Type != "message" {
		return ev.Type
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &envelope); err == nil {
		switch envelope.Type {
		case "message", "block", "done":
			return envelope.Type
		}
	}
	return ev.Type
}

// extractTextFragment pulls the textual content from an SSE event's
// data JSON. The workmax SSE protocol emits a variety of event shapes;
// we recognize the common ones and fall back to the raw data string.
//
//	event: text         {"text":"hello"}
//	event: text_delta   {"delta":"hello"}
//	event: block_start  {"type":"text", ...}
//	event: tool_use     {"name":"…", ...}
//
// Only `text`-bearing events contribute to ai_text; metadata-only
// events still flow through for boundary-based cache flush.
func extractTextFragment(ev SSEEvent) string {
	return extractTextFragmentForType(ev, logicalSSEEventType(ev))
}

func extractTextFragmentForType(ev SSEEvent, logicalType string) string {
	switch logicalType {
	case "block":
		// Current Go Server shape:
		// data: {"type":"block","block":{"type":"text","text":"hello"}}
		// Also accept a top-level text field for tolerant explicit-event
		// compatibility, but never cache the whole JSON envelope as text.
		var probe struct {
			Text  string `json:"text"`
			Block struct {
				Text string `json:"text"`
			} `json:"block"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &probe); err != nil {
			return ""
		}
		if probe.Block.Text != "" {
			return probe.Block.Text
		}
		return probe.Text
	case "text", "text_delta", "message_delta", "content_block_delta":
		// Try common JSON shapes. We don't decode into strict structs
		// to stay tolerant of upstream evolution.
		var probe map[string]any
		if err := json.Unmarshal([]byte(ev.Data), &probe); err != nil {
			return ev.Data
		}
		if s, ok := probe["text"].(string); ok {
			return s
		}
		if s, ok := probe["delta"].(string); ok {
			return s
		}
		if delta, ok := probe["delta"].(map[string]any); ok {
			if s, ok := delta["text"].(string); ok {
				return s
			}
		}
		return ev.Data
	default:
		return ""
	}
}
