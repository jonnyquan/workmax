//go:build desktop

package main

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Verifying the real app, not its parts.
//
// Everything else checks a piece: the behaviour suite drives renderer.js
// against mocks in a VM, --verify-shim checks the bridge surface and streams a
// turn through it, --kill-check checks the transport. None of them runs the
// combination a user actually gets — the unmodified index.html, the shipped
// shim, a real sidecar, in a real webview.
//
// The assertion comes from the proxy rather than from inside the page, because
// the page must not be modified to be observed: the moment a probe script is
// injected into index.html, the thing under test is no longer what ships (and
// script-src 'self' would refuse it anyway). What renderer.js does on boot is
// visible from the outside — it calls /auth/status before anything else — so
// watching the proxy proves the shim installed its globals, the renderer found
// them, and the same-origin path carried the request.

// appBootExpectations are the requests renderer.js makes on boot.
//
// Only one is required, and that is not a weak check — it is the whole chain.
// For /auth/status to arrive, shim.js must have installed window.workmaxLocal,
// renderer.js must have accepted it as a valid bridge, the fetch must have
// resolved against the capability path, the proxy must have injected the token,
// and the sidecar must have answered. Nothing downstream of that is a new kind
// of proof.
//
// Everything else renderer.js does on boot is gated on being signed in:
// threads, the skill catalog and recoverable turns are only loaded when
// /auth/status reports "authenticated". This harness has no cloud session, so
// their absence is the renderer behaving correctly. Listing them as required
// would make the check fail for the wrong reason — which it did, the first
// time it ran.
var appBootExpectations = struct {
	required []bootRequest
	optional []bootRequest
}{
	required: []bootRequest{
		{"auth status", "GET", "/auth/status",
			"renderer.js calls this first; reaching it proves shim.js installed the globals, renderer.js accepted them, and the same-origin path carried the request to the sidecar"},
	},
	optional: []bootRequest{
		{"thread list", "GET", "/agent/threads",
			"session-gated: renderer.js only loads threads once /auth/status reports authenticated"},
		{"skill catalog", "GET", "/agent/skills/catalog",
			"session-gated, and goes through the typed bridge rather than the legacy fetch"},
		{"model route", "GET", "/settings/model-route",
			"typed settings namespace; only read when the settings panel is opened"},
	},
}

type bootRequest struct {
	label  string
	method string
	path   string
	why    string
}

// bootWatcher records proxied requests so the boot sequence can be checked
// after the window has had a chance to load.
type bootWatcher struct {
	mu   sync.Mutex
	seen []string
}

func (w *bootWatcher) record(method, path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, method+" "+path)
}

func (w *bootWatcher) sawPrefix(method, path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, entry := range w.seen {
		if strings.HasPrefix(entry, method+" "+path) {
			return true
		}
	}
	return false
}

func (w *bootWatcher) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.seen...)
}

// watchBoot wraps the UI handler and records what the page asks the sidecar
// for, stripping the capability and API prefixes so the recorded paths read
// like the sidecar routes they become.
func watchBoot(next http.Handler, capability string, watcher *bootWatcher) http.Handler {
	prefix := "/" + capability + uiAPIPrefix
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix) {
			watcher.record(r.Method, "/"+strings.TrimPrefix(r.URL.Path, prefix))
		}
		next.ServeHTTP(w, r)
	})
}

// External-link interception is NOT checked here.
//
// It was attempted, and the attempt is worth recording: ExecJS queues its
// script until the Wails runtime reports itself loaded, and on a loopback
// origin the runtime never does — the same root cause that disables bindings
// (§0.5.11). Proxying /wails/* from this origin did not revive it either. So
// the host cannot drive the page at all, which is a third consequence of the
// same-origin shape.
//
// The check therefore lives in the behaviour suite, which runs the shim
// against a stub DOM and can dispatch a click directly. That is a better home
// anyway: it runs in CI, where a webview cannot.

// reportAppBoot turns the recorded traffic into a verdict.
func reportAppBoot(watcher *bootWatcher, waited time.Duration) int {
	seen := watcher.all()
	log.Printf("verify-app: %d proxied request(s) in %s", len(seen), waited)
	for _, entry := range seen {
		log.Printf("verify-app:   %s", entry)
	}

	failed := false
	for _, want := range appBootExpectations.required {
		if watcher.sawPrefix(want.method, want.path) {
			log.Printf("verify-app: ok   %s (%s %s)", want.label, want.method, want.path)
			continue
		}
		failed = true
		log.Printf("verify-app: FAIL %s (%s %s) — %s", want.label, want.method, want.path, want.why)
	}
	for _, want := range appBootExpectations.optional {
		if watcher.sawPrefix(want.method, want.path) {
			log.Printf("verify-app: ok   %s (optional)", want.label)
		} else {
			log.Printf("verify-app: --   %s not requested (optional: %s)", want.label, want.why)
		}
	}

	if failed {
		log.Printf("verify-app: VERDICT — the shipped renderer did not complete its boot sequence.")
		log.Printf("verify-app:   Nothing was modified to observe it, so this is what a user would get.")
		return 1
	}
	log.Printf("verify-app: VERDICT — the unmodified renderer booted against a real sidecar in a real")
	log.Printf("verify-app:   webview and reached it through the shim. This is the whole path, not a")
	log.Printf("verify-app:   piece of it. The session-gated requests are absent because there is no")
	log.Printf("verify-app:   cloud session here; run this signed in to exercise them too.")
	return 0
}
