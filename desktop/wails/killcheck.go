//go:build desktop

// W1 kill check ①: does an SSE stream from the embedded sidecar survive a
// round trip through WKWebView's fetch + ReadableStream, byte for byte?
//
// This is the one unknown that can end the Wails migration (design doc §15),
// so it is worth automating rather than eyeballing. The harness:
//
//  1. stands up a stub OpenAI-compatible model endpoint that emits a payload
//     chosen to break naive stream handling;
//  2. points the sidecar's local route at it, so /agent/chat produces a real
//     multi-frame SSE turn with no cloud login (L3d made the local route work
//     unauthenticated);
//  3. replays the stream twice — once from Go, once from inside the webview —
//     and compares both against the exact expected text.
//
// The Go replay is the control. If both fail, the harness is wrong. If only
// the webview fails, WKWebView is the problem and the migration stops.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"server/desktop"
)

// killCheckDeltas is the model output the stub streams back, chosen so that a
// merely-plausible implementation fails:
//
//   - multi-byte UTF-8 (Chinese) and a 4-byte emoji, which JS sees as a
//     surrogate pair — a decoder that is not incremental will mangle these
//     when a chunk boundary lands mid-character;
//   - an embedded newline, which must survive JSON encoding rather than being
//     read as an SSE line break;
//   - a literal "data:" prefix inside the content, which a parser that scans
//     for field names anywhere instead of at line start will mis-frame;
//   - a long run, so the response is split across several TCP reads.
var killCheckDeltas = []string{
	"Hello, ",
	"世界",           // multi-byte
	" 🚀 ",          // 4-byte, surrogate pair in JS
	"line1\nline2", // newline inside a data payload
	" data: not-a-field ",
	strings.Repeat("x", 4096),
	" done.",
}

func killCheckExpected() string { return strings.Join(killCheckDeltas, "") }

// startHarnessServer serves the stub model endpoint the sidecar calls. It is
// deliberately a SEPARATE origin from the UI server: the kill check uses it as
// the target of a cross-origin probe, so that the CSP verdict distinguishes
// "blocked by connect-src" from "host unreachable".
func startHarnessServer(api *KillCheckAPI) (baseURL string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		for _, delta := range killCheckDeltas {
			chunk := map[string]any{
				"choices": []map[string]any{{"delta": map[string]string{"content": delta}}},
			}
			raw, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
		// An upstream keepalive comment: the sidecar must swallow it rather
		// than forward it as content.
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}

// configureLocalRoute points the sidecar at the stub and selects the local
// route, in-process rather than over HTTP — the settings store is the same
// object the PUT handler would mutate, and going direct keeps the harness
// independent of the route surface it is not testing.
func configureLocalRoute(boot *desktop.Boot, baseURL string) error {
	if boot.ModelSettings == nil {
		return fmt.Errorf("kill check: model settings unavailable")
	}
	// Model settings are per-identity, so the harness has to write under the
	// same identity the sidecar will resolve when the replay hits /agent/chat.
	_, err := boot.ModelSettings.Put(boot.IdentityUID(), desktop.LocalModelSettingsPut{
		PreferredRoute: "local",
		Local: &desktop.LocalModelProfilePut{
			Protocol: desktop.LocalProtocolOpenAICompatible,
			BaseURL:  baseURL,
			ModelID:  "killcheck-stub",
		},
	})
	return err
}

// killCheckResult is what each replay reports. The webview sends the same
// shape back through the KillCheckAPI binding.
type killCheckResult struct {
	Source      string `json:"source"`
	OK          bool   `json:"ok"`
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Frames      int    `json:"frames"`
	Text        string `json:"text"`
	Terminal    string `json:"terminal"`
	Detail      string `json:"detail"`
}

func (r killCheckResult) verdict() (bool, string) {
	switch {
	case r.Status == http.StatusForbidden:
		// Distinct from a streaming failure and with a known remedy: every
		// sidecar route rejects requests carrying a browser Origin header
		// (route_policy.go). Electron never trips this because Chromium omits
		// Origin for its file:// preload fetches.
		return false, "HTTP 403 — the webview sent a browser Origin header, which every sidecar route rejects (see route_policy.go). This is an origin-policy problem, NOT a WKWebView streaming problem"
	case !r.OK:
		return false, r.Detail
	case !strings.HasPrefix(r.ContentType, "text/event-stream"):
		return false, fmt.Sprintf("content-type = %q, want text/event-stream", r.ContentType)
	case r.Terminal != "done":
		return false, fmt.Sprintf("terminal event = %q, want done", r.Terminal)
	case r.Text != killCheckExpected():
		return false, fmt.Sprintf("text mismatch: got %d bytes, want %d (first divergence at %d)",
			len(r.Text), len(killCheckExpected()), firstDivergence(r.Text, killCheckExpected()))
	}
	return true, ""
}

func firstDivergence(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// ensureKillCheckThread creates the thread the turn will attach to. The chat
// route requires an existing thread; under the local route (L3d) this works
// with no cloud session, which is exactly the configuration the kill check
// wants — nothing about the SSE transport should depend on being logged in.
func ensureKillCheckThread(ctx context.Context, boot *desktop.Boot, threadUUID string) error {
	body, _ := json.Marshal(map[string]string{
		"name":       "kill check",
		"agent_mode": "ppt",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		fmt.Sprintf("http://127.0.0.1:%d/agent/threads/%s", boot.Port(), threadUUID),
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", boot.LocalToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT thread: HTTP %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// runKillCheckGo is the control replay: the same request the webview makes,
// consumed by Go's HTTP client. It proves the sidecar really does emit the
// stream, so a webview failure can only be the webview.
func runKillCheckGo(ctx context.Context, boot *desktop.Boot, turnUUID, threadUUID string) killCheckResult {
	res := killCheckResult{Source: "go"}

	body, _ := json.Marshal(map[string]any{
		"turn_uuid":   turnUUID,
		"thread_uuid": threadUUID,
		"user_text":   "kill check",
		"chat_mode":   "ppt",
		// The chat route decodes strictly: exactly these five fields, no
		// duplicates, and payload.stream must be true.
		"payload": map[string]any{"stream": true},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/agent/chat", boot.Port()), strings.NewReader(string(body)))
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Local-Token", boot.LocalToken)

	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.ContentType = resp.Header.Get("Content-Type")
	res.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		res.Detail = fmt.Sprintf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		return res
	}

	var text strings.Builder
	var event string
	var data strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 || event != "" {
				res.Frames++
				switch event {
				case "text_delta":
					var payload struct {
						Delta string `json:"delta"`
					}
					if err := json.Unmarshal([]byte(data.String()), &payload); err == nil {
						text.WriteString(payload.Delta)
					}
				case "done", "proxy_error", "canceled":
					res.Terminal = event
				}
			}
			event, data = "", strings.Builder{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimPrefix(rest, " ")
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimPrefix(rest, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		res.Detail = err.Error()
		return res
	}
	res.OK = true
	res.Text = text.String()
	return res
}

// KillCheckAPI receives the webview's replay result. Bound as a service so
// the page can report without needing an HTTP route of its own.
type KillCheckAPI struct {
	mu            sync.Mutex
	port          int
	token         string
	reportURL     string
	uiOrigin      string
	foreignOrigin string
	report        chan killCheckReport
}

func newKillCheckAPI() *KillCheckAPI {
	return &KillCheckAPI{report: make(chan killCheckReport, 1)}
}

// killCheckReport is what the page sends back: the webview's own origin plus
// one result per probe.
type killCheckReport struct {
	Origin      string            `json:"origin"`
	Probes      []killCheckResult `json:"probes"`
	Containment map[string]any    `json:"containment"`
}

// Report is called by the kill-check page once its probes finish.
func (k *KillCheckAPI) Report(rep killCheckReport) {
	k.mu.Lock()
	defer k.mu.Unlock()
	select {
	case k.report <- rep:
	default:
	}
}

// Params gives the page everything it needs, so the payload under test is
// defined in exactly one place (Go) rather than duplicated in JS.
func (k *KillCheckAPI) Params() map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	return map[string]any{
		"turnUUID":      killCheckTurnUUID,
		"threadUUID":    killCheckThreadUUID,
		"expected":      killCheckExpected(),
		"port":          k.port,
		"token":         k.token,
		"reportURL":     k.reportURL,
		"apiPrefix":     uiAPIPrefix,
		"foreignOrigin": k.foreignOrigin,
	}
}

// SetProbeTargets records the origins the containment probes use: the UI
// origin the page runs in, and a foreign origin it must NOT be able to reach.
func (k *KillCheckAPI) SetProbeTargets(uiOrigin, foreignOrigin string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.uiOrigin, k.foreignOrigin = uiOrigin, foreignOrigin
}

// SetLoopback records the sidecar coordinates the page must call. Set after
// Bootstrap, before the window opens.
func (k *KillCheckAPI) SetLoopback(port int, token, reportURL string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.port, k.token, k.reportURL = port, token, reportURL
}

// Fixed UUIDs: the harness runs against a throwaway data dir, and a stable
// value makes a failed run's SQLite rows easy to find.
const (
	killCheckTurnUUID   = "00000000-0000-4000-8000-00000000c001"
	killCheckThreadUUID = "00000000-0000-4000-8000-00000000c002"
)

func logKillCheck(r killCheckResult) bool {
	ok, why := r.verdict()
	if ok {
		log.Printf("kill-check[%s]: PASS (%d frames, %d bytes, terminal=%s)",
			r.Source, r.Frames, len(r.Text), r.Terminal)
		return true
	}
	log.Printf("kill-check[%s]: FAIL — %s", r.Source, why)
	return false
}
