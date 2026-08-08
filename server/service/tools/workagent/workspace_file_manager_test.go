package workagent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// PrepareFilesForAgent must reject any source path whose symlink chain
// escapes the workspace root. The original implementation only verified
// the path string was inside root, not the resolved target — letting
// the agent's own Write tool plant a symlink at <thread>/sneaky →
// /etc/passwd and have it re-linked into uploads/ on the next turn.
// These tests pin the boundary at the EvalSymlinks gate.

// withTestWorkspaceRoot temporarily replaces the agent client manager
// singleton with one whose WorkspaceRoot points at the supplied dir.
// We can't reset sync.Once mid-process safely, so we reach into the
// package-private globalManager pointer directly. Cleanup restores the
// original instance so this doesn't leak across tests.
func withTestWorkspaceRoot(t *testing.T, root string) {
	t.Helper()
	prev := globalManager
	// Force the singleton initialised + replaced.
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() {
		globalManager = prev
	})
}

func TestPrepareFilesForAgent_RegularFileLinks(t *testing.T) {
	root := t.TempDir()
	withTestWorkspaceRoot(t, root)

	threadWS := filepath.Join(root, "uid", "1", "thread_a")
	if err := os.MkdirAll(threadWS, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file inside the workspace root.
	src := filepath.Join(root, "shared", "real.txt")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareFilesForAgent([]AgentFileInfo{{
		ID:   "1",
		Name: "real.txt",
		Path: src,
	}}, threadWS)
	if err != nil {
		t.Fatalf("PrepareFilesForAgent error: %v", err)
	}
	if len(prepared) != 1 || prepared[0] != "real.txt" {
		t.Errorf("prepared = %v, want [real.txt]", prepared)
	}
}

func TestPrepareFilesForAgent_RejectsSymlinkEscapingRoot(t *testing.T) {
	root := t.TempDir()
	withTestWorkspaceRoot(t, root)

	// Create a target outside the workspace root.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("PWNED"), 0o644); err != nil {
		t.Fatal(err)
	}

	threadWS := filepath.Join(root, "uid", "1", "thread_a")
	if err := os.MkdirAll(threadWS, 0o755); err != nil {
		t.Fatal(err)
	}

	// Plant a symlink inside the workspace pointing at the outside file
	// — this is the shape of the attack: the agent's Write tool drops
	// such a symlink in its own thread, then on the next turn the user
	// references it as a "file" and we'd link it into uploads/ for the
	// SDK to read.
	planted := filepath.Join(threadWS, "sneaky")
	if err := os.Symlink(outside, planted); err != nil {
		t.Fatalf("symlink setup: %v", err)
	}

	prepared, err := PrepareFilesForAgent([]AgentFileInfo{{
		ID:   "1",
		Name: "sneaky.txt",
		Path: planted,
	}}, threadWS)
	if err != nil {
		t.Fatalf("PrepareFilesForAgent error: %v", err)
	}
	if len(prepared) != 0 {
		t.Errorf("planted symlink should have been refused, got prepared = %v", prepared)
	}

	// Sanity: the upload dir wasn't populated with the offending link.
	uploadsDir := filepath.Join(threadWS, "uploads")
	entries, _ := os.ReadDir(uploadsDir)
	if len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("uploads dir should be empty, contains %s", strings.Join(names, ","))
	}
}

func TestPrepareFilesForAgent_AllowsSymlinkInsideRoot(t *testing.T) {
	// Symlinks that resolve inside the workspace root are legitimate
	// (e.g. a "shared/templates/foo" symlink to "shared/master/foo")
	// and must continue to work.
	root := t.TempDir()
	withTestWorkspaceRoot(t, root)

	target := filepath.Join(root, "master", "doc.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}

	threadWS := filepath.Join(root, "uid", "1", "thread_a")
	if err := os.MkdirAll(threadWS, 0o755); err != nil {
		t.Fatal(err)
	}

	innerLink := filepath.Join(threadWS, "doc-link")
	if err := os.Symlink(target, innerLink); err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareFilesForAgent([]AgentFileInfo{{
		ID:   "1",
		Name: "doc-link",
		Path: innerLink,
	}}, threadWS)
	if err != nil {
		t.Fatalf("PrepareFilesForAgent error: %v", err)
	}
	if len(prepared) != 1 {
		t.Errorf("symlink inside root should be accepted, got prepared = %v", prepared)
	}
}

// copyFile must refuse to follow a symlink at the destination.
// Regression: the previous implementation used os.Create(dst) which
// follows symlinks, so a leftover symlink at the target — whether
// planted by an earlier turn's Write tool or surviving a flaky
// os.Remove — would redirect our write to wherever the link points.
// O_CREATE|O_EXCL|O_NOFOLLOW makes that case fail-closed.
func TestCopyFile_RefusesToWriteThroughSymlinkAtDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed src: %v", err)
	}

	// Sensitive file the symlink would redirect writes to. Real
	// attack surface: /etc/something we don't have permission for
	// — but the test reads back and verifies the bytes never
	// landed there.
	target := filepath.Join(dir, "should-not-be-touched.txt")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	dst := filepath.Join(dir, "dst-symlink")
	if err := os.Symlink(target, dst); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	err := copyFile(src, dst)
	if err == nil {
		t.Error("copyFile through dst-symlink should error (O_NOFOLLOW)")
	}

	// Sensitive file must still hold its original bytes — the
	// open with O_NOFOLLOW must have refused before the write.
	got, _ := os.ReadFile(target)
	if string(got) != "unchanged" {
		t.Errorf("target was modified through symlink; bytes = %q", got)
	}
}

// copyFile preserves the source file's permission bits at create
// time (no chmod-after-create gap). Pin the behaviour so a future
// refactor that drops the perm forwarding doesn't silently change
// security semantics.
func TestCopyFile_PreservesSourceMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dst := filepath.Join(dir, "dst.txt")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("dst mode = %v; want 0o600", info.Mode().Perm())
	}
}

func TestPrepareFilesForAgent_RejectsMissingFile(t *testing.T) {
	root := t.TempDir()
	withTestWorkspaceRoot(t, root)

	threadWS := filepath.Join(root, "uid", "1", "thread_a")
	if err := os.MkdirAll(threadWS, 0o755); err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareFilesForAgent([]AgentFileInfo{{
		ID:   "1",
		Name: "ghost.txt",
		Path: filepath.Join(root, "does-not-exist.txt"),
	}}, threadWS)
	if err != nil {
		t.Fatalf("PrepareFilesForAgent error: %v", err)
	}
	if len(prepared) != 0 {
		t.Errorf("missing file should not be prepared, got %v", prepared)
	}
}
