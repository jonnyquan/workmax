//go:build desktop

package cloud_proxy

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestAllCloudRoutes_RegisteredOnCloudSide pins that every URL the
// sidecar plans to talk to is actually registered on the cloud's
// gin router. The original motivation: the P1.B.6 prereq bug
// (proxy.go POSTing /api/workagent/agent/chat which doesn't exist)
// went undetected because per-side tests stubbed against the wrong
// URL too. With the contracts centralized in cloud_routes.go, this
// test closes the loop: walk the cloud-side router source files,
// build the set of Method+Path registrations, and assert each
// CloudRouteSpec is present.
//
// Strategy: source-scan rather than importing the cloud router.
// Importing server/router/desktop + the AIChatRouter would pull
// the entire api.ApiGroupApp global and its transitive
// initializers — high cost, brittle, and only catches what a
// source scan catches anyway since the routes are static strings.
//
// What this catches:
//   - constant references a path nobody registers (the original bug)
//   - constant typo (e.g. "/api/desktoP/sync/threads")
//   - method drift (e.g. sidecar POST against a cloud GET route)
//
// What this does NOT catch:
//   - cloud registers a Method+Path no spec references (different
//     direction; that's the cloud-side concern, not ours)
func TestAllCloudRoutes_RegisteredOnCloudSide(t *testing.T) {
	registered := scanRegisteredRoutes(t)
	for _, spec := range CurrentCloudRouteSpecs() {
		route := spec.Method + " " + spec.Path
		if !registered[route] {
			t.Errorf("cloud-side Method+Path NOT FOUND in router source: %q\n"+
				"  This means the sidecar would 404 calling this URL.\n"+
				"  Either fix the spec in cloud_routes.go or register\n"+
				"  the route on the cloud side (server/router/...).\n"+
				"  All detected routes: %v",
				route, sortedKeys(registered))
		}
	}
}

// scanRegisteredRoutes walks the known router source files and
// returns the set of full Method+Path identities registered with Gin. Each
// group path is combined with each verb-call path to form the final identity.
//
// We intentionally hardcode the file list (not glob) so a new
// router file doesn't silently slip past the scan — adding a new
// /api/desktop/* path file means updating this list too.
func scanRegisteredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	// here = .../server/desktop/cloud_proxy/<this-file>.go
	// up 2 = .../server/
	serverRoot := filepath.Join(filepath.Dir(here), "..", "..")

	files := []string{
		"router/desktop/desktop_agent_router.go",
		"router/desktop/desktop_sync_router.go",
		"router/desktop/desktop_oauth_router.go",
		"router/desktop/desktop_login_router.go",
		"router/desktop/desktop_version_router.go",
		"router/pro/tools/workagent/aichat_router.go",
	}

	routes := map[string]bool{}
	for _, rel := range files {
		path := filepath.Join(serverRoot, rel)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read router file %q: %v", rel, err)
		}
		for _, route := range extractRoutes(string(content)) {
			routes[route] = true
		}
	}
	return routes
}

// extractRoutes pulls gin-style route registrations from a file's
// source. Matches:
//
//	X.Group("group/path")
//	X.GET("/sub/path", ...)  // or POST/PUT/DELETE/PATCH
//
// Combines each Group with each verb-call in the same file. Same-file
// only — multi-file groups would need cross-file tracking; none of
// the desktop routes use that today.
func extractRoutes(src string) []string {
	src = stripLineComments(src)
	// "api/path" or "/api/path"; capture inside quotes.
	groupRE := regexp.MustCompile(`\.Group\("([^"]+)"\)`)
	verbRE := regexp.MustCompile(`\.(GET|POST|PUT|DELETE|PATCH)\("([^"]+)"`)

	groups := groupRE.FindAllStringSubmatch(src, -1)
	verbs := verbRE.FindAllStringSubmatch(src, -1)

	var out []string
	for _, g := range groups {
		groupPath := strings.TrimSuffix(g[1], "/")
		if !strings.HasPrefix(groupPath, "/") {
			groupPath = "/" + groupPath
		}
		for _, v := range verbs {
			method := v[1]
			sub := v[2]
			if !strings.HasPrefix(sub, "/") {
				sub = "/" + sub
			}
			out = append(out, method+" "+groupPath+sub)
		}
	}
	return out
}

func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if comment := strings.Index(line, "//"); comment >= 0 {
			lines[i] = line[:comment]
		}
	}
	return strings.Join(lines, "\n")
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — list is small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// TestExtractRoutes_Smoke pins the parser itself so a router-file
// formatting change can't silently make scanRegisteredRoutes
// return an empty set (which would make TestAllCloudRoutes_...
// pass for the wrong reason).
func TestExtractRoutes_Smoke(t *testing.T) {
	src := `
		g := router.Group("api/desktop/sync")
		g.GET("/threads", apis.ListThreads)
		g.GET("/threads/:id", apis.GetThread)
		g.POST("/messages", apis.ListMessages)
	`
	got := extractRoutes(src)
	want := map[string]bool{
		"GET /api/desktop/sync/threads":     true,
		"GET /api/desktop/sync/threads/:id": true,
		"POST /api/desktop/sync/messages":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d routes, want %d: %v", len(got), len(want), got)
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected route: %q", r)
		}
	}
}
