package workagent

import (
	"path/filepath"
	"server/globals"
	"strings"
)

const workspaceRoutePrefix = "agent_workspace"

func WorkspaceRoutePrefix() string {
	return workspaceRoutePrefix
}

// ResolveWorkspaceRoot returns the authoritative workspace root directory.
// Precedence, in order:
//  1. The live AgentClientManager (reflects the currently-active config).
//  2. globals.GraConf.ClaudeAgent.WorkspaceRoot from disk-loaded config.
//  3. "./" + workspaceRoutePrefix as a last-resort default.
//
// This is the single source of truth; api/ and router/ helpers should call
// this rather than re-derive the value, otherwise the layers can drift when
// the manager reinitialises without a config reload (or vice versa).
func ResolveWorkspaceRoot() string {
	if manager := GetAgentClientManager(); manager != nil {
		if root := strings.TrimSpace(manager.WorkspaceRoot()); root != "" {
			return root
		}
	}
	if globals.GraConf.ClaudeAgent != nil {
		if root := strings.TrimSpace(globals.GraConf.ClaudeAgent.WorkspaceRoot); root != "" {
			return root
		}
	}
	return "./" + workspaceRoutePrefix
}

func SanitizeWorkspacePath(path string, workspaceRoot string) string {
	if path == "" || workspaceRoot == "" {
		return path
	}

	cleanedRoot := filepath.Clean(workspaceRoot)
	cleanedPath := filepath.Clean(path)

	if rel, err := filepath.Rel(cleanedRoot, cleanedPath); err == nil && !strings.HasPrefix(rel, "..") {
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return workspaceRoutePrefix
		}
		return workspaceRoutePrefix + "/" + rel
	}

	toSlashRoot := filepath.ToSlash(cleanedRoot)
	toSlashPath := filepath.ToSlash(path)
	if strings.HasPrefix(toSlashPath, toSlashRoot) {
		rel := strings.TrimPrefix(toSlashPath, toSlashRoot)
		rel = strings.TrimLeft(rel, "/")
		if rel == "" {
			return workspaceRoutePrefix
		}
		return workspaceRoutePrefix + "/" + rel
	}

	return path
}

// ReplaceWorkspacePaths rewrites absolute host workspace paths in `input` to
// the safe "agent_workspace/..." form that the frontend understands.
//
// The previous implementation tried to be clever with backslash-escape
// variants using raw-string literals:
//
//	strings.ReplaceAll(p, `\\`, `\\\\`)  // replaces "\\" with "\\\\"
//	strings.ReplaceAll(p, `\\`, `/`)     // replaces "\\" with "/"
//
// In a raw string `\\` is literally two backslashes, so these patterns only
// matched paths that contained *double* backslashes — a form Go never emits
// — while the intended JSON-escaped case (a single backslash rendered as two
// characters in a JSON string) was never handled on one hand, and the
// `\\\\` expansion *corrupted* any legitimate "\\" it did find on the other.
// The backend deploys on Linux/macOS where paths use forward slashes, so we
// only need two variants: the cleaned root and its to-slash form.
func ReplaceWorkspacePaths(input string, workspaceRoot string) string {
	if input == "" || workspaceRoot == "" {
		return input
	}

	cleanedRoot := filepath.Clean(workspaceRoot)
	slashRoot := filepath.ToSlash(cleanedRoot)

	result := strings.ReplaceAll(input, cleanedRoot, workspaceRoutePrefix)
	if slashRoot != cleanedRoot {
		result = strings.ReplaceAll(result, slashRoot, workspaceRoutePrefix)
	}
	return result
}

func SanitizePathForClient(path string) string {
	manager := GetAgentClientManager()
	if manager == nil {
		return path
	}

	sanitized := SanitizeWorkspacePath(path, manager.WorkspaceRoot())
	if sanitized == "" {
		return path
	}
	return sanitized
}

// ResolveInsideWorkspace returns the cleaned absolute path of a workspace-relative
// file — but only if the resolved path provably stays inside workspaceRoot.
//
// It refuses:
//   - empty input
//   - absolute paths (so a DB row storing /etc/passwd can't be served)
//   - any ".." segments that, after cleaning, escape workspaceRoot
//
// Callers should treat an empty return as "file not found" without leaking the
// reason. This is the single authoritative resolver for workspace-scoped file
// access; all download/preview/delete paths should funnel through it.
func ResolveInsideWorkspace(workspaceRoot, relPath string) string {
	if strings.TrimSpace(relPath) == "" || strings.TrimSpace(workspaceRoot) == "" {
		return ""
	}
	if filepath.IsAbs(relPath) {
		return ""
	}

	trimmed := strings.TrimLeft(relPath, "/\\")
	if strings.HasPrefix(trimmed, workspaceRoutePrefix) {
		trimmed = strings.TrimPrefix(trimmed, workspaceRoutePrefix)
		trimmed = strings.TrimLeft(trimmed, "/\\")
	}
	if trimmed == "" {
		return ""
	}

	cleanRoot := filepath.Clean(workspaceRoot)
	absRoot, err := filepath.Abs(cleanRoot)
	if err != nil {
		return ""
	}

	joined := filepath.Join(cleanRoot, filepath.FromSlash(trimmed))
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return ""
	}

	// Must be exactly the root or a descendant; the separator suffix prevents
	// "/foo" matching "/foobar".
	sep := string(filepath.Separator)
	if absJoined == absRoot {
		return absRoot
	}
	if !strings.HasPrefix(absJoined+sep, absRoot+sep) {
		return ""
	}
	return absJoined
}
