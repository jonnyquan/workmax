package workagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureThreadWorkspaceUsesUserScopedDirectory(t *testing.T) {
	manager := &AgentClientManager{
		workspaceRoot: t.TempDir(),
	}

	threadUUID := "550e8400-e29b-41d4-a716-446655440000"
	got, err := manager.EnsureThreadWorkspace(42, threadUUID)
	if err != nil {
		t.Fatalf("EnsureThreadWorkspace() error = %v", err)
	}

	rel, err := filepath.Rel(manager.workspaceRoot, got)
	if err != nil {
		t.Fatalf("filepath.Rel() error = %v", err)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "uid/42/") || !strings.HasSuffix(rel, "/thread_"+threadUUID) {
		t.Fatalf("EnsureThreadWorkspace() relative path = %q", rel)
	}
	for _, dir := range []string{
		got,
		filepath.Join(got, "uploads"),
		filepath.Join(got, "outputs"),
	} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("expected directory %q to exist: %v", dir, err)
		}
	}
}

func TestEnsureThreadWorkspaceRejectsTraversalUUID(t *testing.T) {
	manager := &AgentClientManager{
		workspaceRoot: t.TempDir(),
	}

	if _, err := manager.EnsureThreadWorkspace(42, "../escape"); err == nil {
		t.Fatal("expected traversal thread uuid to be rejected")
	}
}

func TestUserScopedThreadWorkspaceCacheKey(t *testing.T) {
	threadUUID := "550e8400-e29b-41d4-a716-446655440000"
	if got := userScopedThreadWorkspaceCacheKey(42, threadUUID); got != "42:"+threadUUID {
		t.Fatalf("cache key with uid = %q", got)
	}
	if got := userScopedThreadWorkspaceCacheKey(0, threadUUID); got != "0:"+threadUUID {
		t.Fatalf("cache key without uid = %q", got)
	}
}
