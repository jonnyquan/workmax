//go:build desktop

package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"
)

// uiServer is the origin the webview lives in. It exists because the two
// obvious alternatives were both measured and both fail (see the design doc
// §0.5.9 for the kill-check evidence):
//
//   - fetching the sidecar cross-origin: every sidecar route rejects requests
//     carrying a browser Origin, and a token-bearing JSON POST needs a CORS
//     preflight the sidecar does not answer;
//   - serving the renderer from the wails:// asset scheme: WebKit's
//     WKURLSchemeHandler drops POST bodies, so the renderer could never send
//     a chat request.
//
// Serving both the renderer and the API from one real loopback origin makes
// every renderer request same-origin: no Origin header, no preflight, intact
// bodies, working ReadableStream. The sidecar's perimeter is untouched — this
// server strips Origin on the way through rather than asking the sidecar to
// relax.
//
// It also means the token stays in Go. The renderer never sees it, which is
// the property decision D4/A2 had accepted losing.
type uiServer struct {
	listener net.Listener
	server   *http.Server
	origin   string
	baseURL  string
}

// mintCapability returns the unguessable path segment everything is served
// under.
//
// Without it the UI origin is an open door: the proxy injects the local token
// on the caller's behalf, so any local process that finds the ephemeral port
// would drive the sidecar with full authority — exactly the bypass the token
// exists to prevent. ("The loopback port is reachable by other local
// processes; the token is the real protection." — appendix C.)
//
// A path segment rather than a header because the first request is a top-level
// navigation, which cannot carry one. The renderer needs no special handling:
// it addresses everything relatively, so knowing the capability is the same as
// knowing its own URL.
func mintCapability() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ui capability: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// uiAPIPrefix is the same-origin path the sidecar is reachable under.
const uiAPIPrefix = "/api/"

// contentSecurityPolicy is the primary containment control, and it is set as a
// header rather than a meta tag because we own the server — a stronger
// position than the Electron build, which served the renderer over file:// and
// could only use a meta tag.
//
// No inline exemption is granted, for scripts or for styles. The shipped
// renderer has no inline scripts, no style attributes, and no runtime .style
// mutations, so the concession normally made "for the existing code" is not
// needed here — and a concession made once is never taken back. script-src is
// the one that matters most: the realistic injection vector is model output
// rendered into the DOM, and restricting it to 'self' stops that executing.
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"media-src 'self' blob:; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'; " +
	"frame-src 'none'; " +
	"object-src 'none'; " +
	"base-uri 'none'"

// UIHandler builds the routing the UI origin exposes: bundled assets at /,
// the sidecar proxy under uiAPIPrefix, and nothing else. Returned separately
// from newUIServer so the kill-check harness can exercise the real routing
// with its own page mounted alongside, rather than testing a lookalike.
func UIHandler(renderer fs.FS, capability string, sidecarPort int, sidecarToken string) http.Handler {
	return UIHandlerWithOpener(renderer, capability, sidecarPort, sidecarToken, nil)
}

// UIHandlerWithOpener is UIHandler plus the system-browser opener. A nil
// opener refuses every external link rather than silently doing nothing: a
// harness that has no browser to open should fail the request, not appear to
// succeed.
func UIHandlerWithOpener(renderer fs.FS, capability string, sidecarPort int, sidecarToken string, open externalURLOpener) http.Handler {
	base := "/" + capability
	apiPrefix := base + uiAPIPrefix

	if open == nil {
		open = func(string) error { return fmt.Errorf("no system browser opener wired") }
	}

	inner := http.NewServeMux()
	inner.Handle(apiPrefix, newSidecarProxyAt(apiPrefix, sidecarPort, sidecarToken))
	inner.Handle(base+uiLoginPrefix, newLoginGate(base+uiLoginPrefix, sidecarPort, sidecarToken))
	inner.Handle(base+uiOpenExternalPath, newOpenExternalHandler(open))
	inner.Handle(base+"/", securityHeaders(http.StripPrefix(base, http.FileServer(http.FS(renderer)))))

	outer := http.NewServeMux()
	// Anything not under the capability is answered as if nothing is here —
	// no hint that a different path would have worked.
	outer.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	outer.Handle(base+"/", inner)

	return rejectNonCanonicalPaths(outer)
}

// rejectNonCanonicalPaths refuses any request whose path is not already in
// cleaned form, instead of letting ServeMux 301 it to the cleaned one.
//
// The sidecar takes the same position for the same reason (server.go sets
// RedirectTrailingSlash and RedirectFixedPath to false): a privileged-route
// check that runs after a redirect is a check whose result depends on whether
// the client follows redirects. Refusing outright keeps "is this path
// blocked?" a question with one answer.
func rejectNonCanonicalPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path.Clean(r.URL.Path) && r.URL.Path != path.Clean(r.URL.Path)+"/" {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newUIServer binds a loopback listener that serves assets from renderer and
// proxies uiAPIPrefix to the sidecar. Nothing else is reachable: an unmatched
// path 404s rather than falling through to anything.
func newUIServer(renderer fs.FS, sidecarPort int, sidecarToken string, open externalURLOpener) (*uiServer, error) {
	capability, err := mintCapability()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("ui server listen: %w", err)
	}

	srv := &http.Server{Handler: UIHandlerWithOpener(renderer, capability, sidecarPort, sidecarToken, open)}
	go func() { _ = srv.Serve(listener) }()

	origin := "http://" + listener.Addr().String()
	return &uiServer{
		listener: listener,
		server:   srv,
		origin:   origin,
		baseURL:  origin + "/" + capability + "/",
	}, nil
}

// BaseURL is what the window loads: origin plus the capability segment. Every
// renderer request is relative to it.
func (u *uiServer) BaseURL() string { return u.baseURL }

// Origin is the scheme://host:port the webview loads and treats as its own.
func (u *uiServer) Origin() string { return u.origin }

// Close stops serving. The sidecar is shut down separately by Boot.Shutdown.
func (u *uiServer) Close() error { return u.server.Close() }

// securityHeaders applies the containment headers to every asset response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// The renderer is a single-window app shell; there is no legitimate
		// reason for it to be framed or to expose device APIs.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// uiLoginPrefix is the renderer's only way to drive the password login. It is
// the direct analogue of Electron's Main-process IPC gate: the renderer states
// an intent, a process it does not control performs the privileged call, and
// the flow ID never leaves Go.
//
// Credentials still travel loopback HTTP to the sidecar, exactly as they do in
// the Electron build today (login-transaction.ts posts to the same two paths
// from Main). What the gate buys is that the renderer cannot name those paths
// itself — the proxy refuses them — so it can only submit through this one
// shape, and every server-side protection on those routes (fresh-copy
// credential hygiene, duplicate-key rejection, the 4 KiB cap) still applies.
//
// The stronger form of D3 — unregister the routes entirely and call
// loginCoordinator in-process — remains available and is a strict improvement;
// it is deliberately not bundled into the shell migration.
const uiLoginPrefix = "/login/"

// loginGateRoutes maps the renderer-facing verbs onto the sidecar's login
// transaction routes. Enumerated rather than pattern-matched so that adding a
// reachable credential path is a visible edit.
var loginGateRoutes = map[string]struct {
	method string
	path   string
}{
	"begin":    {http.MethodPost, "/auth/login-transaction"},
	"status":   {http.MethodGet, "/auth/login-transaction"},
	"password": {http.MethodPost, "/auth/login-transaction/password"},
	"cancel":   {http.MethodDelete, "/auth/login-transaction"},
}

// newLoginGate forwards exactly the four known verbs and nothing else.
func newLoginGate(prefix string, sidecarPort int, sidecarToken string) http.Handler {
	client := &http.Client{Timeout: 30 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", sidecarPort)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verb := strings.TrimPrefix(r.URL.Path, prefix)
		route, ok := loginGateRoutes[verb]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// The renderer states an intent; the gate decides the method. A
		// renderer that could pick the verb could turn a status poll into a
		// credential submission.
		if r.Method != http.MethodPost {
			http.Error(w, "login gate expects POST", http.StatusMethodNotAllowed)
			return
		}

		var body io.Reader
		if verb == "password" {
			body = io.LimitReader(r.Body, maxLoginBodyBytes)
		}
		req, err := http.NewRequestWithContext(r.Context(), route.method, base+route.path, body)
		if err != nil {
			http.Error(w, "login gate error", http.StatusInternalServerError)
			return
		}
		if verb == "password" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Local-Token", sidecarToken)

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "login gate upstream error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, maxLoginBodyBytes))
	})
}

// maxLoginBodyBytes matches the sidecar's own cap on the password body.
const maxLoginBodyBytes = 4 << 10

// privilegedSidecarPaths are the routes the renderer must not be able to
// reach, no matter what it asks for. They are the password login transaction:
// credentials go in, and a renderer that could drive them could phish or
// exfiltrate them.
//
// The Electron build enforced this in preload (isPrivilegedLoginTransactionURL)
// — a guard living in the same process as the code it restrains. Here it moves
// to the proxy, which the renderer cannot reach around at all. Login instead
// goes through the Wails LoginAPI binding (decision D3), so the credential
// path never becomes an HTTP surface the renderer can name.
var privilegedSidecarPaths = map[string]struct{}{
	"/auth/login-transaction":          {},
	"/auth/login-transaction/password": {},
}

// privilegedSidecarPrefixes are whole subtrees the renderer must not reach.
//
// The model gateway is one: it is the loopback endpoint the local agent
// subprocesses use to run a turn on an official model, authenticated by a
// credential the page has no way to hold and no business holding. Left
// reachable, it would be a way for anything rendered into the page to spend
// the account's membership directly, skipping every check the agent routes
// apply on the way to a turn.
//
// A prefix rather than an enumeration, deliberately: the sidecar registers
// several spellings of each gateway path because different provider clients
// disagree about where the version segment goes, and a list here would have to
// be kept in step with that. "Nothing under /model-gateway/" cannot fall out
// of step.
var privilegedSidecarPrefixes = []string{"/model-gateway/"}

// isPrivilegedSidecarPath reports whether a proxied path is off limits. The
// comparison is on the cleaned path so "/auth/./login-transaction" and
// "/auth/foo/../login-transaction" cannot slip past.
func isPrivilegedSidecarPath(p string) bool {
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	if _, blocked := privilegedSidecarPaths[cleaned]; blocked {
		return true
	}
	for _, prefix := range privilegedSidecarPrefixes {
		if strings.HasPrefix(cleaned+"/", prefix) {
			return true
		}
	}
	return false
}

// newSidecarProxyAt reverse-proxies prefix/* to the sidecar on loopback,
// injecting the local token and stripping the browser headers the sidecar
// refuses. FlushInterval -1 is what keeps SSE streaming instead of buffering.
func newSidecarProxyAt(prefix string, port int, token string) http.Handler {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// prefix is "/api/", so trimming it leaves the sidecar path with
			// its leading slash intact ("/api/health" -> "/health").
			r.Out.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(r.In.URL.Path, prefix), "/")
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Host = target.Host
			r.Out.Header.Set("X-Local-Token", token)
			// The sidecar rejects any request carrying a browser Origin. Strip
			// it here so that rule — a real defense against other local
			// processes reaching the port from a browser — can stay as strict
			// as it is today.
			r.Out.Header.Del("Origin")
			r.Out.Header.Del("Referer")
			r.Out.Header.Del("Cookie")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarPath := "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
		if isPrivilegedSidecarPath(sidecarPath) {
			http.Error(w, "this route is not reachable from the renderer", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// serveLoopback binds handler to an ephemeral loopback port and returns the
// origin plus a stop func.
func serveLoopback(handler http.Handler) (origin string, stop func() error, err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(listener) }()
	return "http://" + listener.Addr().String(), srv.Close, nil
}
