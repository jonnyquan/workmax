//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// CallbackResult is what the loopback server captures from the
// OAuth callback (path /oauth/callback?code=...&state=...). On
// vendor errors (user denied, workmax backend issue), `code` is empty
// and `errParam` carries the value (RFC 6749 §4.1.2.1).
type CallbackResult struct {
	Code     string
	State    string
	ErrParam string // "" on success
	ErrDesc  string
}

// LoopbackCallbackServer is the ephemeral HTTP server that listens
// for the OAuth callback redirect. One server per OAuth flow:
// constructed → Start (binds an OS-assigned port) → caller reads
// .Port() for the redirect_uri → caller awaits result via Result()
// or context timeout → Stop.
//
// The server only handles GET /oauth/callback. Anything else 404s
// — the browser shouldn't be sending other requests, and we want
// noise to be visible if it does.
type LoopbackCallbackServer struct {
	listener net.Listener
	server   *http.Server

	resultMu sync.Mutex
	result   *CallbackResult
	resultCh chan struct{} // closed once result is set

	// HTML body sent to the browser after capturing the callback.
	// Test harness injects custom content; production gets the
	// "You can close this window" auto-close page.
	successHTML string

	// True if the loopback server should call window.close() in
	// the success HTML. Disabled in tests where browser behavior
	// is irrelevant.
	autoClose bool
}

// NewLoopbackCallbackServer binds to 127.0.0.1:0 (OS-assigned port)
// and prepares the handler. Does NOT start serving until Start() is
// called.
//
// Loopback-only binding is critical: 0.0.0.0 would let a remote
// attacker on the LAN race the legitimate callback (RFC 8252 §7.3).
func NewLoopbackCallbackServer() (*LoopbackCallbackServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("loopback callback: listen: %w", err)
	}
	s := &LoopbackCallbackServer{
		listener:    ln,
		resultCh:    make(chan struct{}),
		successHTML: defaultSuccessHTML,
		autoClose:   true,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", s.handleCallback)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

// Port returns the OS-assigned port. Safe to call any time after
// construction (before Start counts too).
func (s *LoopbackCallbackServer) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// RedirectURI returns the exact `redirect_uri` that should be sent
// in the /authorize request. Convenience over hand-building from Port.
func (s *LoopbackCallbackServer) RedirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", s.Port())
}

// Start begins serving in a goroutine. Returns immediately; the
// server runs until Stop() is called or the underlying http.Server
// errors out.
func (s *LoopbackCallbackServer) Start() {
	go func() {
		_ = s.server.Serve(s.listener)
	}()
}

// Wait blocks until a callback is received OR ctx is canceled.
// Returns the captured CallbackResult on success, or ctx.Err()
// on cancellation / timeout (CallbackResult will be nil).
//
// Idempotent — subsequent calls after a result is captured return
// immediately with the same result.
func (s *LoopbackCallbackServer) Wait(ctx context.Context) (*CallbackResult, error) {
	select {
	case <-s.resultCh:
		s.resultMu.Lock()
		defer s.resultMu.Unlock()
		return s.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stop gracefully shuts the HTTP server down. Idempotent. Caller
// usually defers this immediately after construction.
func (s *LoopbackCallbackServer) Stop() error {
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.server.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *LoopbackCallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cr := callbackResultFromQuery(q)

	// Store and signal exactly once. Late duplicate callbacks (which
	// the browser shouldn't generate but might) are ignored.
	s.resultMu.Lock()
	if s.result == nil {
		s.result = cr
		close(s.resultCh)
	}
	s.resultMu.Unlock()

	// Render the success page so the user sees that the handoff
	// worked. Auto-close JS might be blocked by the browser, so the page also tells the
	// user they can close it manually.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if cr.ErrParam != "" {
		_, _ = w.Write([]byte(defaultErrorHTML))
		return
	}
	_, _ = w.Write([]byte(s.successHTML))
}

func callbackResultFromQuery(q url.Values) *CallbackResult {
	if hasDuplicateOAuthCallbackParam(q, "code") ||
		hasDuplicateOAuthCallbackParam(q, "state") ||
		hasDuplicateOAuthCallbackParam(q, "error") ||
		hasDuplicateOAuthCallbackParam(q, "error_description") {
		return &CallbackResult{
			ErrParam: "invalid_request",
			ErrDesc:  "duplicate OAuth callback parameter",
			State:    uniqueOAuthCallbackParam(q, "state"),
		}
	}
	cr := &CallbackResult{
		Code:     q.Get("code"),
		State:    q.Get("state"),
		ErrParam: q.Get("error"),
		ErrDesc:  q.Get("error_description"),
	}
	if cr.Code != "" && cr.ErrParam != "" {
		return &CallbackResult{
			ErrParam: "invalid_request",
			ErrDesc:  "OAuth callback cannot include both code and error",
			State:    cr.State,
		}
	}
	return cr
}

func hasDuplicateOAuthCallbackParam(q url.Values, key string) bool {
	return len(q[key]) > 1
}

func uniqueOAuthCallbackParam(q url.Values, key string) string {
	if len(q[key]) != 1 {
		return ""
	}
	return q[key][0]
}

// defaultSuccessHTML is the page shown to the user after the callback is
// captured. It includes a best-effort window.close() and falls back to
// user-facing copy when the browser suppresses it.
const defaultSuccessHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Signed in</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
           background: #0b0b0e; color: #f5f5f7;
           display: flex; align-items: center; justify-content: center;
           min-height: 100vh; margin: 0; }
    .card { max-width: 340px; text-align: center; padding: 24px; }
    h1 { font-size: 22px; font-weight: 600; margin: 0 0 8px; }
    p { color: #86868b; font-size: 14px; margin: 0; line-height: 1.5; }
  </style>
</head>
<body>
  <div class="card">
    <h1>Signed in to workmax</h1>
    <p>You can close this window and return to the desktop app.</p>
  </div>
  <script>
    // Best-effort auto-close. window.close() works in browsers when the window was
    // opened programmatically. If it doesn't, the message above
    // tells the user what to do.
    setTimeout(() => { try { window.close(); } catch (e) {} }, 600);
  </script>
</body>
</html>`

const defaultErrorHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Sign-in not completed</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
           background: #0b0b0e; color: #f5f5f7;
           display: flex; align-items: center; justify-content: center;
           min-height: 100vh; margin: 0; }
    .card { max-width: 360px; text-align: center; padding: 24px; }
    h1 { font-size: 22px; font-weight: 600; margin: 0 0 8px; }
    p { color: #86868b; font-size: 14px; margin: 0; line-height: 1.5; }
  </style>
</head>
<body>
  <div class="card">
    <h1>Sign-in was not completed</h1>
    <p>Close this window and try signing in again from the desktop app.</p>
  </div>
</body>
</html>`
