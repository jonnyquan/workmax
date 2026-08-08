package workagent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	systemReq "server/model/system/request"

	"github.com/gin-gonic/gin"
)

// withCallerUID returns a gin.HandlerFunc that injects a CustomClaims
// row matching the JWTAuth middleware's contract, so the production
// utils.GetUserID(c) read inside serveWorkspaceFileWithRoot resolves
// to a known caller without spinning up the real JWT layer.
func withCallerUID(uid uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("claims", &systemReq.CustomClaims{
			BaseClaims: systemReq.BaseClaims{Id: uid},
		})
		c.Next()
	}
}

// newTestRouter wires up a Gin engine that mirrors the production route
// shape (`GET /agent_workspace/*filepath`) but uses a fixed workspace
// root and a synthetic-uid middleware. Returns the engine and the
// chosen root.
func newTestRouter(t *testing.T, callerUID uint) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	r := gin.New()
	r.GET("/agent_workspace/*filepath", withCallerUID(callerUID), func(c *gin.Context) {
		serveWorkspaceFileWithRoot(c, root)
	})
	return r, root
}

// writeFile creates `<root>/<rel>` with the given bytes, mkdir-p'ing
// parents. Helper to keep test setup readable.
func writeFile(t *testing.T, root, rel string, body []byte) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

func TestServeWorkspaceFile_AuthRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	writeFile(t, root, "uid/42/file.txt", []byte("hi"))

	r := gin.New()
	// No claims middleware — caller is anonymous.
	r.GET("/agent_workspace/*filepath", func(c *gin.Context) {
		serveWorkspaceFileWithRoot(c, root)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/agent_workspace/uid/42/file.txt", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous caller: got status %d, want 401", w.Code)
	}
}

func TestServeWorkspaceFile_CrossUIDBlocked(t *testing.T) {
	r, root := newTestRouter(t, 12)
	// File belongs to uid=99, caller is uid=12.
	writeFile(t, root, "uid/99/secret.txt", []byte("nope"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/agent_workspace/uid/99/secret.txt", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("cross-uid read: got %d, want 404", w.Code)
	}
}

func TestServeWorkspaceFile_NoSniffAlwaysSet(t *testing.T) {
	r, root := newTestRouter(t, 7)
	writeFile(t, root, "uid/7/data.csv", []byte("a,b\n1,2\n"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/agent_workspace/uid/7/data.csv", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options=%q, want nosniff", got)
	}
	// Non-active-content extension must NOT be forced to attachment —
	// inline preview of CSV/PNG/PDF is the legitimate UX.
	if cd := w.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition for .csv should be empty, got %q", cd)
	}
}

func TestServeWorkspaceFile_HTMLForcedToAttachment(t *testing.T) {
	cases := []struct {
		ext  string
		body []byte
	}{
		{".html", []byte("<script>alert(1)</script>")},
		{".htm", []byte("<script>alert(1)</script>")},
		{".svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{".xhtml", []byte("<script>alert(1)</script>")},
		{".xml", []byte("<?xml version=\"1.0\"?><a/>")},
		{".xsl", []byte("<?xml version=\"1.0\"?>")},
	}

	for _, tc := range cases {
		t.Run(tc.ext, func(t *testing.T) {
			r, root := newTestRouter(t, 3)
			writeFile(t, root, "uid/3/report"+tc.ext, tc.body)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/agent_workspace/uid/3/report"+tc.ext, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status %d", w.Code)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("nosniff missing")
			}
			cd := w.Header().Get("Content-Disposition")
			if !strings.HasPrefix(cd, "attachment;") {
				t.Errorf("active-content %s must force attachment, got Content-Disposition=%q", tc.ext, cd)
			}
		})
	}
}

func TestServeWorkspaceFile_PathTraversalBlocked(t *testing.T) {
	r, root := newTestRouter(t, 5)
	// Plant a file outside the workspace.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("forbidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = root

	cases := []string{
		"/agent_workspace/../../../../etc/passwd",
		"/agent_workspace/uid/5/../../../../../etc/passwd",
		"/agent_workspace/..%2F..%2Fetc/passwd",
		"/agent_workspace/uid/../12/file.txt",
	}
	for _, url := range cases {
		t.Run(url, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			r.ServeHTTP(w, req)
			if w.Code == http.StatusOK {
				t.Errorf("traversal %s returned 200 — must be blocked", url)
			}
		})
	}
}

func TestServeWorkspaceFile_LegitimateUploadsSymlinkServed(t *testing.T) {
	// PrepareFilesForAgent installs `<thread>/uploads/<name>` as a
	// symlink to the real upload backing file (workspace_file_manager
	// .go:179). Verify the hardened serve still follows that
	// legitimate intermediate symlink to a real file inside the
	// workspace.
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin on windows")
	}
	r, root := newTestRouter(t, 8)

	// Real backing file lives at <root>/uploads-store/data.txt.
	backing := writeFile(t, root, "uploads-store/data.txt", []byte("legit-content"))

	// Thread workspace symlinks uid/8/.../uploads/data.txt -> backing.
	threadUploads := filepath.Join(root, "uid", "8", "20260507", "thread_x", "uploads")
	if err := os.MkdirAll(threadUploads, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(threadUploads, "data.txt")
	if err := os.Symlink(backing, link); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/agent_workspace/uid/8/20260507/thread_x/uploads/data.txt", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("legit symlink: status %d, body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "legit-content" {
		t.Errorf("body=%q, want legit-content", got)
	}
}

func TestServeWorkspaceFile_RefusesEscapingSymlink(t *testing.T) {
	// The agent could plant a symlink at uid/{caller}/leak.txt
	// pointing at /etc/passwd or similar. EvalSymlinks resolves
	// through it; the prefix check must catch that the resolved
	// target is outside workspaceRoot.
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin on windows")
	}
	r, root := newTestRouter(t, 9)

	// Outside-the-workspace target.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("escaped-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	threadDir := filepath.Join(root, "uid", "9", "thread_y")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(threadDir, "leak.txt")); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/agent_workspace/uid/9/thread_y/leak.txt", nil)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Errorf("escaping symlink served! status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestServeWorkspaceFile_DirectoryListingRefused(t *testing.T) {
	r, root := newTestRouter(t, 4)
	if err := os.MkdirAll(filepath.Join(root, "uid", "4", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/agent_workspace/uid/4/subdir", nil)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Errorf("directory served as file: status=%d", w.Code)
	}
}

// TestServeWorkspaceFile_NoFollowOnCanonicalSwap simulates the TOCTOU
// the O_NOFOLLOW guard exists to defend against. EvalSymlinks resolves
// the request to a canonical path; before Open, the agent replaces
// that canonical path with a symlink to a sensitive file. With the
// hardening in place, OpenFile(O_NOFOLLOW) refuses to traverse the
// late-arriving symlink.
//
// We can't truly time-shift the gap in a unit test, but we can
// pre-stage the swapped state: the canonical path IS already a
// symlink at the time of the request. EvalSymlinks resolves through
// it once, and then the second EvalSymlinks-then-Open will hit
// O_NOFOLLOW on the resolved target's still-symlink form. Concretely
// we stage `uid/{n}/canonical` → `outside`; EvalSymlinks resolves to
// `outside`, prefix check rejects (outside workspace) → 404. This is
// already covered by the escape-symlink test, but kept here as a
// named regression test for the documented swap scenario.
func TestServeWorkspaceFile_NoFollowOnCanonicalSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin on windows")
	}
	r, root := newTestRouter(t, 11)

	outside := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(outside, []byte("root:x:0:0"), 0o644); err != nil {
		t.Fatal(err)
	}
	threadDir := filepath.Join(root, "uid", "11", "thread_z")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(threadDir, "canonical")); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/agent_workspace/uid/11/thread_z/canonical", nil)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Errorf("swapped-canonical served! status=%d body=%q", w.Code, w.Body.String())
	}
}
