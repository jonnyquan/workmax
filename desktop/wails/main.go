//go:build desktop

// workmax-desktop is the single WorkMax Desktop binary, in two modes.
//
//	workmax-desktop                the desktop app: embedded sidecar + webview
//	workmax-desktop --serve-only   the sidecar alone: no window, no webview
//
// Both modes share desktop.Bootstrap, so the wiring cannot drift between what
// a developer curls and what a user runs. --serve-only replaces the separate
// workagent-desktop process: it is the target for smoke scripts and for
// poking at the loopback API by hand.
//
// Design: ProjectDocs/wails-migration-evaluation-2026-08.md §10.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing/fstest"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"server/desktop"
	"server/desktop/buildinfo"
	renderer "workmax/desktop/renderer"
)

// RendererDirEnv overrides the embedded renderer with a directory on disk.
//
// Empty — every packaged launch, and any `go run` — serves the copy compiled
// into this binary (see the renderer package). Set, which is what dev.sh does,
// serves that directory instead so an edit to renderer.js shows up on the next
// relaunch without a rebuild. Only ever a directory; the webview is never
// pointed at a remote origin.
//
// The override is not a security hole in a packaged app: setting an
// environment variable for a process already means being able to replace its
// files. What embedding buys is that the DEFAULT path no longer reads code from
// a writable directory.
const RendererDirEnv = "WORKMAX_DESKTOP_RENDERER_DIR"

// ResourcesDirEnv points at the packaged native assets (ONNX Runtime shared
// library, embedding model, tokenizer). Empty → derived from the executable
// location. Absent resources mean RAG is off, not that the app fails to boot.
const ResourcesDirEnv = "WORKMAX_DESKTOP_RESOURCES_DIR"

func main() {
	serveOnly := flag.Bool("serve-only", false, "run the sidecar without a window (dev + smoke target)")
	killCheck := flag.Bool("kill-check", false, "run the W1 SSE kill check (Go control + webview replay) and exit")
	verifyShim := flag.Bool("verify-shim", false, "load the real renderer shim in the webview, check the bridge surface, and exit")
	verifyApp := flag.Bool("verify-app", false, "load the unmodified renderer in the webview and check it completes its boot sequence")
	verifyAppWait := flag.Duration("verify-app-wait", 12*time.Second, "how long to watch the renderer boot")
	killCheckTimeout := flag.Duration("kill-check-timeout", 45*time.Second, "how long to wait for the webview replay")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	log.SetPrefix("workmax-desktop ")
	log.Printf("starting v%s (serve-only=%v)", buildinfo.Version, *serveOnly)

	boot, err := desktop.Bootstrap(desktop.BootstrapConfig{
		ResourcesDir: resolveResourcesDir(),
		// D3 (login-transaction moves off HTTP onto a Wails binding) lands in
		// W2 together with LoginAPI. Until then the routes stay registered so
		// the renderer shim can reach them exactly as the Electron build does.
	})
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}
	if boot.TokenGenerated {
		log.Printf("local token: generated in-process (%d chars)", len(boot.LocalToken))
	} else {
		log.Printf("local token: inherited from %s (%d chars)", desktop.LocalTokenEnv, len(boot.LocalToken))
	}

	if *serveOnly {
		runServeOnly(boot)
		return
	}
	if *verifyApp {
		runVerifyApp(boot, *verifyAppWait)
		return
	}
	if *killCheck || *verifyShim {
		runKillCheck(boot, *killCheckTimeout, *verifyShim)
		return
	}
	runDesktop(boot)
}

// runServeOnly is the whole --serve-only mode: no Wails runtime is created, so
// this path is exactly the old standalone sidecar minus the stdout handshake
// and the stdin watcher. The port is logged instead, since there is no parent
// process to hand it to.
func runServeOnly(boot *desktop.Boot) {
	log.Printf("serve-only: http://127.0.0.1:%d (X-Local-Token: %s)", boot.Port(), boot.LocalToken)

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		boot.Cancel(fmt.Sprintf("signal %s", <-ch))
	}()

	if err := boot.ServeUntilContextDone(); err != nil && err != http.ErrServerClosed {
		log.Printf("server error: %v", err)
	}
	shutdownBoot(boot)
	log.Printf("bye")
}

// runDesktop is the windowed mode. The webview talks to the same loopback
// server the sidecar always exposed; only the process boundary is gone.
func runDesktop(boot *desktop.Boot) {
	boot.Start()
	go func() {
		if err := boot.Serve(); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()

	rendererFS, rendererSource, err := resolveRendererFS()
	if err != nil {
		shutdownBoot(boot)
		log.Fatalf("resolve renderer: %v", err)
	}
	log.Printf("renderer: %s", rendererSource)

	// The webview lives in a real loopback origin that serves the bundled
	// renderer and proxies the sidecar under /api/. Measured rationale in
	// uiserver.go; the short version is that neither a cross-origin fetch nor
	// the wails:// asset scheme can carry a chat request.
	//
	// The opener is wired after the app exists, since it is the Wails app that
	// owns the browser manager. Until then external links are refused, which
	// is the right answer for the fraction of a second before the window is up.
	var openExternal externalURLOpener = func(string) error { return fmt.Errorf("app not ready") }
	ui, err := newUIServer(rendererFS, boot.Port(), boot.LocalToken,
		func(target string) error { return openExternal(target) })
	if err != nil {
		shutdownBoot(boot)
		log.Fatalf("ui server: %v", err)
	}
	defer func() { _ = ui.Close() }()
	log.Printf("ui origin: %s (capability path minted per launch; api under %s)", ui.Origin(), uiAPIPrefix)
	// Dev-only, default off: the full capability URL, for driving the real UI
	// from an external browser (interactive E2E, DevTools). The capability IS
	// the credential, so this stays behind an explicit opt-in and goes only to
	// the process's own stdout — the same trust domain as the operator who
	// set the variable. Never set in packaged launches.
	if os.Getenv("WORKMAX_DESKTOP_DEV_UI_URL") == "1" {
		log.Printf("dev ui url: %s/", ui.BaseURL())
	}

	app := application.New(application.Options{
		Name:        "WorkMax Desktop",
		Description: "WorkMax local-first desktop agent",
		Assets: application.AssetOptions{
			// The window loads ui.Origin(), not this handler. Wails still
			// wants an asset handler for its own runtime bits; anything else
			// asked of it is a bug, so say so rather than serving files.
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "renderer is served from the UI origin", http.StatusNotFound)
			}),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "ai.workmax.desktop",
		},
		// Boot.Shutdown owns the teardown ordering, including releasing the
		// ONNX Runtime environment. It must run here rather than in a defer:
		// once app.Run() has the control flow, a defer in main is not
		// guaranteed to execute before the process exits.
		OnShutdown: func() { shutdownBoot(boot) },
	})

	// BackgroundColour is deliberately left at its zero value, and the app's
	// in-window appearance switch (renderer.js, data-theme) does not sync it.
	//
	// Wails does expose WebviewWindow.SetBackgroundColour at runtime, but the
	// only caller who knows the theme changed is the page, and the page has no
	// way to tell Go: the Wails JS runtime posts to location.origin +
	// "/wails/runtime", which on our loopback UI origin is our own server, not
	// the Wails app (§0.5.11, the same reason there is no bound service). Wiring
	// it would mean a new renderer-reachable route on the UI server — a new
	// surface, and a boundary-manifest entry, to tint a strip of window that is
	// only visible while resizing. The page paints its own background from the
	// same token the theme switches, so what a reader actually looks at is
	// already correct; what is left is the native chrome during a resize, and
	// color-scheme in styles.css covers the parts of that macOS honours.
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "main",
		Title:  "WorkMax",
		Width:  1200,
		Height: 800,
		URL:    ui.BaseURL(),
		// Off unless explicitly asked for. The window holds an authenticated
		// session and an API the proxy authenticates on its behalf; a devtools
		// surface on a shipped build is a way in that nothing else grants.
		DevToolsEnabled: devToolsRequested(),
	})

	if err := app.Run(); err != nil {
		log.Printf("wails run: %v", err)
		shutdownBoot(boot)
		os.Exit(1)
	}
}

func shutdownBoot(boot *desktop.Boot) {
	ctx, cancel := context.WithTimeout(context.Background(), desktop.GracefulShutdownTimeout)
	defer cancel()
	_ = boot.Shutdown(ctx)
}

// DevToolsEnv opts a development run into webview devtools. Absent means off,
// including in every packaged build — there is deliberately no way to turn it
// on from inside the app.
const DevToolsEnv = "WORKMAX_DESKTOP_DEVTOOLS"

func devToolsRequested() bool { return os.Getenv(DevToolsEnv) == "1" }

// There is no bound service. RuntimeAPI used to hand the renderer the API's
// coordinates, but two measurements removed the need for it: the same-origin
// shape means the renderer addresses everything relatively (nothing to be
// told), and Wails bindings do not work on a loopback origin anyway — the
// runtime posts calls to location.origin + "/wails/runtime", which is our own
// UI server. See ProjectDocs/wails-migration-evaluation-2026-08.md §0.5.11.

// resolveRendererFS returns the renderer to serve, plus a one-line description
// of where it came from for the launch log. The default is the copy embedded in
// this binary; RendererDirEnv swaps in a directory, which is the dev loop.
//
// There is deliberately no probing of the filesystem any more. The old version
// searched Contents/Resources, then the directory beside the executable, then
// the repo — three implicit answers to "which renderer is running", each of
// which could be wrong in a way nobody would notice. Now there are two, and one
// of them has to be asked for by name.
func resolveRendererFS() (fs.FS, string, error) {
	if dir := os.Getenv(RendererDirEnv); dir != "" {
		clean := filepath.Clean(dir)
		if entry, err := os.Stat(filepath.Join(clean, "index.html")); err != nil || entry.IsDir() {
			return nil, "", fmt.Errorf("%s=%s has no index.html", RendererDirEnv, clean)
		}
		return os.DirFS(clean), "on disk (" + RendererDirEnv + "): " + clean, nil
	}
	embedded, err := renderer.FS()
	if err != nil {
		return nil, "", fmt.Errorf("embedded renderer: %w", err)
	}
	return embedded, "embedded in the binary", nil
}

// resolveResourcesDir locates the native assets for the local knowledge index.
//
// Returning empty is the normal answer, and it is what makes the app find them
// at all: Bootstrap then defaults to <DataDir>/resources, which is where the
// downloader puts them. An earlier version returned the bundle's Resources
// directory whenever it existed — which is always, in a packaged build — so a
// packaged app would have downloaded the assets to the data directory and then
// looked for them inside its own bundle.
//
// The bundle is still honoured, but only when it demonstrably contains the
// assets. That keeps a future "ship them inside" variant working without
// hijacking the download path in the meantime.
func resolveResourcesDir() string {
	if dir := os.Getenv(ResourcesDirEnv); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	packaged := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "Resources"))
	for _, name := range []string{"libonnxruntime.dylib", "libonnxruntime.so", "onnxruntime.dll"} {
		if _, err := os.Stat(filepath.Join(packaged, name)); err == nil {
			return packaged
		}
	}
	return ""
}

// runKillCheck executes W1 kill check ①. It replays the same SSE turn twice —
// from Go and from inside the webview — and exits non-zero if the webview
// cannot reproduce the stream byte for byte.
//
// Exit codes are meant to be read by a script: 0 pass, 1 webview failure
// (the migration-stopping outcome), 2 harness failure (the check itself is
// broken and proves nothing).
func runKillCheck(boot *desktop.Boot, timeout time.Duration, verifyShim bool) {
	api := newKillCheckAPI()
	harnessURL, stopHarness, err := startHarnessServer(api)
	if err != nil {
		log.Printf("kill-check: harness listener: %v", err)
		finishKillCheck(boot, 2)
	}
	defer stopHarness()

	if err := configureLocalRoute(boot, harnessURL); err != nil {
		log.Printf("kill-check: configure local route: %v", err)
		finishKillCheck(boot, 2)
	}

	boot.Start()
	go func() {
		if err := boot.Serve(); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()

	// Control first: if Go cannot read the stream either, the harness is at
	// fault and a webview failure would prove nothing.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := ensureKillCheckThread(ctx, boot, killCheckThreadUUID); err != nil {
		log.Printf("kill-check: %v", err)
		finishKillCheck(boot, 2)
	}
	if !logKillCheck(runKillCheckGo(ctx, boot, killCheckTurnUUID, killCheckThreadUUID)) {
		log.Printf("kill-check: the Go control failed, so the harness is broken — not a WKWebView verdict")
		finishKillCheck(boot, 2)
	}

	// The page is served by the PRODUCTION UI routing (UIHandler), with the
	// kill-check page as its only asset and its params/report endpoints
	// mounted alongside. Probing a lookalike would prove nothing about what
	// ships.
	capability, err := mintCapability()
	if err != nil {
		log.Printf("kill-check: %v", err)
		finishKillCheck(boot, 2)
	}
	var assets fs.FS = fstest.MapFS{
		"index.html":   &fstest.MapFile{Data: []byte(killCheckHTML)},
		"killcheck.js": &fstest.MapFile{Data: []byte(killCheckJS)},
		"harness.css":  &fstest.MapFile{Data: []byte(killCheckCSS)},
	}
	entry := ""
	if verifyShim {
		// The whole point of this mode is that the files under test are the
		// shipped ones, so serve the real renderer and add only the
		// verification page beside it. Since the renderer is embedded, "the
		// shipped ones" now means the bytes in this binary rather than whatever
		// happens to be in a directory — which is a stronger claim than the
		// mode used to be able to make.
		base, source, err := resolveRendererFS()
		if err != nil {
			log.Printf("verify-shim: %v", err)
			finishKillCheck(boot, 2)
		}
		log.Printf("verify-shim: serving real renderer %s", source)
		assets = overlayFS{
			base: base,
			extra: fstest.MapFS{
				"verify.html": &fstest.MapFile{Data: []byte(verifyShimHTML)},
				"verify.js":   &fstest.MapFile{Data: []byte(verifyShimJS)},
				"harness.css": &fstest.MapFile{Data: []byte(verifyShimCSS)},
			},
		}
		entry = "verify.html"
	}
	production := UIHandler(assets, capability, boot.Port(), boot.LocalToken)
	uiMux := http.NewServeMux()
	uiMux.Handle("/", production)
	uiMux.HandleFunc("/"+capability+"/killcheck/params", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.Params())
	})
	uiMux.HandleFunc("/"+capability+"/killcheck/report", func(w http.ResponseWriter, r *http.Request) {
		var rep killCheckReport
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			http.Error(w, "bad report", http.StatusBadRequest)
			return
		}
		api.Report(rep)
		w.WriteHeader(http.StatusNoContent)
	})
	uiOrigin, stopUI, err := serveLoopback(uiMux)
	if err != nil {
		log.Printf("kill-check: ui listener: %v", err)
		finishKillCheck(boot, 2)
	}
	defer func() { _ = stopUI() }()
	api.SetLoopback(boot.Port(), boot.LocalToken, "killcheck/report")
	api.SetProbeTargets(uiOrigin, harnessURL)
	log.Printf("kill-check: ui origin = %s, harness (foreign) origin = %s", uiOrigin, harnessURL)

	app := application.New(application.Options{
		Name: "WorkMax kill check",
		Assets: application.AssetOptions{
			Handler: killCheckAssetHandler(boot),
		},
		Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "killcheck",
		Title:           "WorkMax kill check",
		Width:           900,
		Height:          600,
		URL:             uiOrigin + "/" + capability + "/" + entry,
		DevToolsEnabled: devToolsRequested(),
	})

	go func() {
		select {
		case rep := <-api.report:
			finishKillCheck(boot, reportKillCheckVerdict(rep))
		case <-time.After(timeout):
			log.Printf("kill-check[webview]: FAIL — no report within %s (the page never finished)", timeout)
			finishKillCheck(boot, 1)
		}
	}()

	if err := app.Run(); err != nil {
		log.Printf("kill-check: wails run: %v", err)
		finishKillCheck(boot, 2)
	}
}

// killCheckAssetHandler serves the test page and, under /sidecar/, the
// same-origin reverse proxy the second probe exercises.
// runVerifyApp opens the real app and watches whether the shipped renderer
// completes its boot sequence against a real sidecar. Nothing about the page
// is altered to observe it — see verifyapp.go for why that constraint shapes
// the whole check.
func runVerifyApp(boot *desktop.Boot, wait time.Duration) {
	rendererFS, rendererSource, err := resolveRendererFS()
	if err != nil {
		log.Printf("verify-app: %v", err)
		finishKillCheck(boot, 2)
	}
	log.Printf("verify-app: serving the shipped renderer %s", rendererSource)

	capability, err := mintCapability()
	if err != nil {
		log.Printf("verify-app: %v", err)
		finishKillCheck(boot, 2)
	}
	watcher := &bootWatcher{}
	// External links are refused here rather than opened: this harness must not
	// launch a browser. The interception itself is checked in the behaviour
	// suite, which can drive a click — see check-bundled-renderer-behavior.mjs.
	refuse := func(string) error { return fmt.Errorf("verify-app does not open external links") }
	handler := watchBoot(UIHandlerWithOpener(rendererFS, capability, boot.Port(), boot.LocalToken, refuse), capability, watcher)
	uiOrigin, stopUI, err := serveLoopback(handler)
	if err != nil {
		log.Printf("verify-app: ui listener: %v", err)
		finishKillCheck(boot, 2)
	}
	defer func() { _ = stopUI() }()

	boot.Start()
	go func() {
		if err := boot.Serve(); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()

	app := application.New(application.Options{
		Name:   "WorkMax verify-app",
		Assets: application.AssetOptions{Handler: killCheckAssetHandler(boot)},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "verify-app",
		Title:  "WorkMax verify-app",
		Width:  1200,
		Height: 800,
		URL:    uiOrigin + "/" + capability + "/",
	})

	started := time.Now()
	go func() {
		time.Sleep(wait)
		finishKillCheck(boot, reportAppBoot(watcher, time.Since(started)))
	}()

	if err := app.Run(); err != nil {
		log.Printf("verify-app: wails run: %v", err)
		finishKillCheck(boot, 2)
	}
}

// killCheckAssetHandler exists only to satisfy the Wails runtime, which wants
// somewhere to serve its own bits from. The page under test is served over
// loopback HTTP instead — see startHarnessServer.
func killCheckAssetHandler(_ *desktop.Boot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the kill check serves its page over loopback HTTP", http.StatusNotFound)
	})
}

// reportKillCheckVerdict turns the page's probes into an exit code and, more
// importantly, into a sentence a human can act on.
func reportKillCheckVerdict(rep killCheckReport) int {
	log.Printf("kill-check: webview origin = %s", rep.Origin)
	results := map[string]bool{}
	for _, r := range rep.Probes {
		results[r.Source] = logKillCheck(r)
	}
	reportContainment(rep.Containment)
	if _, isShimRun := results["shim"]; isShimRun {
		if results["shim"] {
			log.Printf("kill-check: VERDICT — the shipped shim satisfies the renderer's bridge contract")
			log.Printf("kill-check:   and streams a full turn through it. W3 acceptance passes.")
			return 0
		}
		log.Printf("kill-check: VERDICT — the shipped shim does not satisfy the renderer contract yet.")
		return 1
	}
	switch {
	case results["direct"]:
		log.Printf("kill-check: VERDICT — direct loopback fetch works; decision D2 stands as designed")
		return 0
	case results["sameorigin"]:
		log.Printf("kill-check: VERDICT — SSE through WKWebView is FINE. The proxy probe streamed it")
		log.Printf("kill-check:   byte-for-byte, matching the Go control exactly. The migration continues.")
		log.Printf("kill-check:   What fails is only D2's *direct cross-origin* fetch, for two reasons:")
		log.Printf("kill-check:     1. the sidecar rejects any browser Origin and answers no CORS preflight;")
		log.Printf("kill-check:     2. wails:// custom-scheme pages cannot POST a body at all (WebKit's")
		log.Printf("kill-check:        WKURLSchemeHandler drops it), so serving the renderer from the")
		log.Printf("kill-check:        asset scheme is not an option either.")
		log.Printf("kill-check:   Remedy: load the renderer over real loopback HTTP and proxy the API")
		log.Printf("kill-check:   under that same origin in Go. Same-origin, no CORS, body intact.")
		log.Printf("kill-check:   Bonus: the token never reaches JS, restoring the secrecy D4/A2 gave up.")
		return 0
	default:
		log.Printf("kill-check: VERDICT — neither probe streamed. WKWebView cannot carry the SSE path.")
		log.Printf("kill-check:   This is the migration-stopping outcome (design doc §15).")
		return 1
	}
}

// reportContainment prints the kill ③ findings. They do not gate the exit
// code: containment is a posture question with compensating controls, not a
// binary the transport depends on.
func reportContainment(c map[string]any) {
	if c == nil {
		log.Printf("kill-check[containment]: no data reported")
		return
	}
	for _, k := range []string{"cspBlocksForeignOrigin", "unknownPath", "traversal", "windowOpenReturnedNull", "wailsRuntime", "wailsInternal", "wailsCallApi", "wailsCall"} {
		if v, ok := c[k]; ok {
			log.Printf("kill-check[containment]: %s = %v", k, v)
		}
	}
}

// overlayFS serves extra on top of base. Used so the verification page can sit
// beside the real renderer without being copied into the shipped directory.
type overlayFS struct {
	base  fs.FS
	extra fs.FS
}

func (o overlayFS) Open(name string) (fs.File, error) {
	if f, err := o.extra.Open(name); err == nil {
		return f, nil
	}
	return o.base.Open(name)
}

// finishKillCheck tears the sidecar down before exiting. Going through
// Shutdown rather than a bare os.Exit is not politeness: it releases the ONNX
// Runtime environment, without which the process aborts and the exit code the
// caller reads is the abort, not our verdict.
func finishKillCheck(boot *desktop.Boot, code int) {
	shutdownBoot(boot)
	os.Exit(code)
}
