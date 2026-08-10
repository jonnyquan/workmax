//go:build desktop

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// The renderer reaches the sidecar only through this proxy, so the proxy is
// where the credential routes have to be unreachable — a guard the renderer
// cannot reason its way around, unlike the preload-side check it replaces.
func TestUIProxyBlocksPrivilegedLoginRoutes(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "reached the sidecar: "+r.URL.Path)
	}))
	defer sidecar.Close()
	port := sidecarPortOf(t, sidecar.URL)

	const cap = "test-capability"
	handler := UIHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, cap, port, "tok")

	blocked := []string{
		"/" + cap + "/api/auth/login-transaction",
		"/" + cap + "/api/auth/login-transaction/password",
	}
	for _, p := range blocked {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403 (body: %s)", p, rec.Code, rec.Body.String())
		}
	}

	// Non-canonical spellings must be refused outright rather than redirected
	// to the canonical path: a check that only holds if the client follows a
	// 301 is not a check.
	for _, p := range []string{
		"/" + cap + "/api/auth/./login-transaction",
		"/" + cap + "/api/auth/nested/../login-transaction/password",
	} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code/100 == 3 {
			t.Errorf("%s = %d, want a refusal (not OK, not a redirect)", p, rec.Code)
		}
	}
}

// The model gateway is how a local agent subprocess reaches an official model
// — it spends the account's membership on a request nobody in the page
// approved. The renderer must not be able to name it under any spelling, so
// the whole subtree is refused here rather than route by route.
func TestUIProxyBlocksTheModelGatewaySubtree(t *testing.T) {
	reached := false
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = io.WriteString(w, "reached the sidecar: "+r.URL.Path)
	}))
	defer sidecar.Close()
	port := sidecarPortOf(t, sidecar.URL)

	const cap = "test-capability"
	handler := UIHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, cap, port, "tok")

	for _, p := range []string{
		"/" + cap + "/api/model-gateway/anthropic/v1/messages",
		"/" + cap + "/api/model-gateway/anthropic/messages",
		"/" + cap + "/api/model-gateway/openai/v1/chat/completions",
		"/" + cap + "/api/model-gateway/openai/chat/completions",
		// A spelling nobody registered is refused too: the subtree is the
		// boundary, not the list of paths that happen to exist today.
		"/" + cap + "/api/model-gateway/anything/at/all",
	} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403 (body: %s)", p, rec.Code, rec.Body.String())
		}
	}
	for _, p := range []string{
		"/" + cap + "/api/model-gateway/./anthropic/v1/messages",
		"/" + cap + "/api/agent/../model-gateway/openai/v1/chat/completions",
	} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code/100 == 3 {
			t.Errorf("%s = %d, want a refusal (not OK, not a redirect)", p, rec.Code)
		}
	}
	if reached {
		t.Fatalf("a renderer request reached the model gateway")
	}

	// The neighbouring path must still work: this is a subtree, not a prefix
	// match on the first characters.
	req := httptest.NewRequest(http.MethodGet, "/"+cap+"/api/model-gateways-not-ours", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("an unrelated path was blocked: %d", rec.Code)
	}
}

func TestUIProxyForwardsOrdinaryRoutesAndStripsBrowserHeaders(t *testing.T) {
	var gotToken, gotOrigin, gotPath string
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Local-Token")
		gotOrigin = r.Header.Get("Origin")
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer sidecar.Close()

	const cap = "test-capability"
	handler := UIHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
		cap, sidecarPortOf(t, sidecar.URL), "secret-token")

	req := httptest.NewRequest(http.MethodGet, "/"+cap+"/api/agent/threads?include_paused=false", nil)
	req.Header.Set("Origin", "http://127.0.0.1:1234")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/agent/threads" {
		t.Errorf("forwarded path = %q, want /agent/threads", gotPath)
	}
	if gotToken != "secret-token" {
		t.Errorf("token = %q; the proxy must inject it so the renderer never holds it", gotToken)
	}
	if gotOrigin != "" {
		t.Errorf("Origin = %q; it must be stripped, since every sidecar route rejects requests carrying one", gotOrigin)
	}
}

func TestUIAssetsCarryContainmentHeaders(t *testing.T) {
	const cap = "test-capability"
	handler := UIHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>")}}, cap, 1, "tok")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+cap+"/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	// script-src 'self' is the control that actually stops injected model
	// output from executing; connect-src 'self' keeps the page inside its
	// own origin. Both were verified live by the kill check.
	for _, want := range []string{"script-src 'self'", "style-src 'self'", "connect-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got %q", want, csp)
		}
	}
	// The shipped renderer needs no inline exemption, and one granted is never
	// taken back — so the absence is asserted, not left to review.
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP grants an inline/eval exemption; got %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("assets must be served with nosniff")
	}
}

func TestUIServesNothingOutsideTheAssetFS(t *testing.T) {
	const cap = "test-capability"
	handler := UIHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>")}}, cap, 1, "tok")
	for _, p := range []string{"/" + cap + "/not-bundled.js", "/../../etc/passwd", "/" + cap + "/styles.css"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
}

// Regression guard for a hole introduced when the proxy started injecting the
// token: if the UI origin needs no credential of its own, then any local
// process that finds the ephemeral port drives the sidecar with full
// authority, and the local-token perimeter is bypassed entirely. The Electron
// threat model is explicit that the port is reachable and the token is the
// real protection.
func TestUIOriginRequiresItsOwnCapability(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "sidecar reached")
	}))
	defer sidecar.Close()

	handler := UIHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
		"the-real-capability", sidecarPortOf(t, sidecar.URL), "secret-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unauthenticated /api/health = %d (body %q); a caller with no capability must not reach the sidecar",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}

func sidecarPortOf(t *testing.T, rawURL string) int {
	t.Helper()
	_, portText, ok := strings.Cut(strings.TrimPrefix(rawURL, "http://"), ":")
	if !ok {
		t.Fatalf("cannot parse port from %q", rawURL)
	}
	port := 0
	for _, c := range portText {
		port = port*10 + int(c-'0')
	}
	return port
}

// External links are the one navigation the shell cannot cancel once it
// starts: macOS Wails has no decidePolicyForNavigationAction, so an anchor
// that is allowed to proceed replaces the app with a remote page and there is
// no way back. These pin what the Go side accepts.
func TestOpenExternalAcceptsOnlyRealExternalHTTPURLs(t *testing.T) {
	for _, ok := range []string{
		"https://github.com/jonnyquan/workmax",
		"http://example.com/path?q=1#frag",
		"https://example.com:8443/deep/link",
	} {
		if _, err := normalizeExternalHTTPURL(ok); err != nil {
			t.Errorf("normalizeExternalHTTPURL(%q) = %v, want accepted", ok, err)
		}
	}

	// Each rejection is a different way "open this link" could become a
	// request to something it should not reach.
	for _, bad := range []struct{ url, why string }{
		{"file:///etc/passwd", "file: reaches the local disk"},
		{"workmax://internal", "a custom scheme reaches whatever app registered it"},
		{"javascript:alert(1)", "not a navigation at all"},
		{"https://user:pw@example.com/", "credentials in the URL"},
		{"http://localhost:8080/admin", "localhost"},
		{"http://127.0.0.1:9999/", "loopback"},
		{"http://[::1]:9999/", "loopback v6"},
		{"http://10.0.0.5/", "private range"},
		{"http://192.168.1.1/", "private range"},
		{"http://172.16.0.1/", "private range"},
		{"http://169.254.169.254/latest/meta-data/", "link-local, the cloud metadata endpoint"},
		{"http://0.0.0.0/", "unspecified"},
		{"https://sub.localhost/", "localhost suffix"},
		{"  https://example.com", "padded input"},
		{"", "empty"},
	} {
		if got, err := normalizeExternalHTTPURL(bad.url); err == nil {
			t.Errorf("normalizeExternalHTTPURL(%q) = %q, want refused (%s)", bad.url, got, bad.why)
		}
	}
}

func TestOpenExternalEndpointRefusesWithoutOpening(t *testing.T) {
	var opened []string
	handler := newOpenExternalHandler(func(u string) error {
		opened = append(opened, u)
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/open-external",
		strings.NewReader(`{"url":"http://169.254.169.254/latest/meta-data/"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(opened) != 0 {
		t.Fatalf("a refused URL was still handed to the browser: %v", opened)
	}

	req = httptest.NewRequest(http.MethodPost, "/open-external",
		strings.NewReader(`{"url":"https://example.com/"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(opened) != 1 || opened[0] != "https://example.com/" {
		t.Fatalf("opened = %v, want the accepted URL exactly once", opened)
	}
}

func TestOpenExternalIsReachableOnlyUnderTheCapability(t *testing.T) {
	const cap = "test-capability"
	var opened int
	handler := UIHandlerWithOpener(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
		cap, 1, "tok", func(string) error { opened++; return nil })

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/open-external",
		strings.NewReader(`{"url":"https://example.com/"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unauthenticated /open-external = %d, want 404", rec.Code)
	}
	if opened != 0 {
		t.Fatal("a caller without the capability reached the system browser")
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/"+cap+"/open-external",
		strings.NewReader(`{"url":"https://example.com/"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("capability-bearing /open-external = %d, want 200", rec.Code)
	}
}
