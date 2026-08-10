//go:build desktop

package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The five files the app is allowed to serve. Named here as well as in
// embed.go so that "what ships" is asserted from the outside: embed.go could
// name a file it does not have, and a glob would silently start shipping
// whatever landed in the directory.
var shippedRendererFiles = []string{
	"index.html",
	"styles.css",
	"renderer.js",
	"shim.js",
	"lib/desktop-bridge.js",
}

// The default is the copy compiled into this binary, not a directory beside it.
// That is the whole point of embedding: a directory is writable by anyone who
// can write to the installation, and the code in it runs inside the window that
// holds the capability path.
func TestEmbeddedRendererIsTheDefault(t *testing.T) {
	t.Setenv(RendererDirEnv, "")

	rendererFS, source, err := resolveRendererFS()
	if err != nil {
		t.Fatalf("resolveRendererFS: %v", err)
	}
	if !strings.Contains(source, "embedded") {
		t.Fatalf("default renderer source = %q, want the embedded copy", source)
	}

	for _, name := range shippedRendererFiles {
		data, err := fs.ReadFile(rendererFS, name)
		if err != nil {
			t.Fatalf("embedded renderer is missing %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("embedded renderer file %s is empty", name)
		}
	}

	// And nothing else. An embed pattern that started sweeping the directory
	// would show up here as an extra entry rather than as a shipped surprise.
	allowed := map[string]struct{}{}
	for _, name := range shippedRendererFiles {
		allowed[name] = struct{}{}
	}
	err = fs.WalkDir(rendererFS, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if _, ok := allowed[path]; !ok {
			t.Errorf("embedded renderer contains an unexpected file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded renderer: %v", err)
	}
}

// The embedded bytes are the ones in the repository. Without this the embed
// could go stale against a moved or renamed source and nothing would say so.
func TestEmbeddedRendererMatchesTheSource(t *testing.T) {
	rendererFS, _, err := resolveRendererFS()
	if err != nil {
		t.Fatalf("resolveRendererFS: %v", err)
	}
	for _, name := range shippedRendererFiles {
		onDisk, err := os.ReadFile(filepath.Join("..", "renderer", "en", "desktop", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read source %s: %v", name, err)
		}
		embedded, err := fs.ReadFile(rendererFS, name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if string(onDisk) != string(embedded) {
			t.Errorf("embedded %s differs from the source file", name)
		}
	}
}

// The dev loop still works: RendererDirEnv serves a directory so an edit to
// renderer.js shows up on the next relaunch without a rebuild. dev.sh sets it
// on every run, so a regression here breaks the way the app is developed.
func TestRendererDirEnvServesFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>dev</html>"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv(RendererDirEnv, dir)

	rendererFS, source, err := resolveRendererFS()
	if err != nil {
		t.Fatalf("resolveRendererFS: %v", err)
	}
	if !strings.Contains(source, dir) {
		t.Fatalf("renderer source = %q, want it to name %s", source, dir)
	}
	data, err := fs.ReadFile(rendererFS, "index.html")
	if err != nil {
		t.Fatalf("read from the dev directory: %v", err)
	}
	if string(data) != "<html>dev</html>" {
		t.Fatalf("dev directory served %q", data)
	}

	// A directory that is not a renderer fails loudly instead of falling back
	// to the embedded copy. Silently serving something other than what was
	// asked for is how you debug the wrong file for an hour.
	t.Setenv(RendererDirEnv, filepath.Join(dir, "nope"))
	if _, _, err := resolveRendererFS(); err == nil {
		t.Fatal("a directory with no index.html must be an error")
	}
}

// End to end through the real routing: the embedded renderer is what the
// webview would receive, and it carries the containment headers. Together with
// the CSP assertions in uiserver_test.go this is the pair that replaced the
// index.html meta tag — the document now gets its policy from the response,
// so the response is where it has to be checked.
func TestEmbeddedRendererIsServedWithTheContainmentHeaders(t *testing.T) {
	t.Setenv(RendererDirEnv, "")
	rendererFS, _, err := resolveRendererFS()
	if err != nil {
		t.Fatalf("resolveRendererFS: %v", err)
	}
	handler := UIHandler(rendererFS, "cap", 1, "tok")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cap/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /cap/ = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "WorkMax Desktop") {
		t.Fatal("the served document is not the renderer's index.html")
	}
	if got := recorder.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Fatalf("Content-Security-Policy = %q, want the shell's policy", got)
	}
	// The meta tag is gone on purpose; the header is the only policy. A meta
	// tag coming back would mean two policies again, and the webview enforcing
	// an intersection neither file states.
	if strings.Contains(recorder.Body.String(), "http-equiv=\"Content-Security-Policy\"") {
		t.Fatal("index.html must not carry a Content-Security-Policy meta tag")
	}
}
