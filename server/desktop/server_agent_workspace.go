//go:build desktop

package desktop

import (
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// GET /agent/threads/:uuid/workspace — what the tool loop produced.
//
// L2 turns run tools in <DataDir>/agent_workspace/thread_<uuid>, and until
// this route the files they wrote were invisible: the agent could deliver,
// but nothing could show the delivery. This is what the Deliverables panel
// reads. Read-only; the workspace's contents were written by the agent under
// the PreToolUse path policy, and serving a listing adds no new authority.

// workspaceFile is one produced file as the renderer needs to show it.
// Paths are workspace-relative: the renderer names files to the user, and the
// absolute prefix is machine detail that also happens to leak the data dir.
type workspaceFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

type workspaceListResponse struct {
	Items []workspaceFile `json:"items"`
	Count int             `json:"count"`
	// Truncated reports that the cap was hit, so the renderer can say "and
	// more" instead of presenting a partial listing as the whole story.
	Truncated bool `json:"truncated"`
}

// maxWorkspaceListEntries bounds the walk. A runaway agent writing thousands
// of files must not turn a panel refresh into a filesystem crawl.
const maxWorkspaceListEntries = 200

func (s *Server) handleListWorkspaceFiles(c *gin.Context) {
	if s.cfg.DB == nil || s.cfg.DataDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "workspace_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	// Ownership first: the workspace directory is keyed by uuid alone, so
	// without this check any local caller could list another identity's
	// produced files by guessing uuids. The thread row is the authority.
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	uid := identity.UID
	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}

	root := filepath.Join(s.cfg.DataDir, "agent_workspace", "thread_"+threadUUID)
	items, truncated := listWorkspaceFiles(root)
	c.JSON(http.StatusOK, workspaceListResponse{Items: items, Count: len(items), Truncated: truncated})
}

// revealWorkspaceDir opens the directory in the OS file manager. A seam so
// tests can assert the path without opening windows on the test machine.
var revealWorkspaceDir = func(dir string) error {
	return exec.Command("open", dir).Start()
}

// POST /agent/threads/:uuid/workspace/reveal — hand the folder to the user.
//
// A Deliverables panel that lists files nobody can open is a screenshot of
// files. The renderer cannot touch the filesystem, so this is the sidecar's
// job: validate ownership exactly as the listing does, then ask the OS to
// show the one directory this thread owns. No path travels in the request —
// the uuid is the only input, and the path is derived server-side, so there
// is nothing here that can be pointed anywhere else.
func (s *Server) handleRevealWorkspace(c *gin.Context) {
	if s.cfg.DB == nil || s.cfg.DataDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "workspace_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	uid := identity.UID
	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "agent_workspace", "thread_"+threadUUID)
	if info, serr := os.Stat(dir); serr != nil || !info.IsDir() {
		// Nothing to show is a state the renderer should not have offered;
		// answering 404 keeps the button honest if it raced a deletion.
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace_not_found"})
		return
	}
	if err := revealWorkspaceDir(dir); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "reveal_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"revealed": true})
}

// listWorkspaceFiles walks the workspace, newest first, bounded.
//
// A missing directory is an empty listing, not an error: a thread that never
// ran a tool turn has no workspace, and that is a normal state. Symlinks are
// listed but not followed — the walk must not wander wherever a link points.
func listWorkspaceFiles(root string) ([]workspaceFile, bool) {
	items := []workspaceFile{}
	truncated := false
	count := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if count >= maxWorkspaceListEntries {
			truncated = true
			return filepath.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		items = append(items, workspaceFile{
			Path:       filepath.ToSlash(rel),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"),
		})
		count++
		return nil
	})
	sort.Slice(items, func(i, j int) bool {
		if items[i].ModifiedAt != items[j].ModifiedAt {
			return items[i].ModifiedAt > items[j].ModifiedAt
		}
		return strings.Compare(items[i].Path, items[j].Path) < 0
	})
	return items, truncated
}

// GET /agent/threads/:uuid/workspace/diff — what the most recent turn changed.
//
// The workspace is a git repo (ensureWorkspace inits it, Chat snapshots before
// each turn). This route runs `git diff HEAD` and returns the result: a file
// list with added/removed counts, and the full unified diff text (capped).
// Codex and Claude both make this a first-class panel; without it the reader
// can see that files appeared but not what is in them.

// workspaceDiffFile is one changed file as the renderer needs it for a
// clickable, expandable list.
type workspaceDiffFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// workspaceDiffResponse is the full diff payload.
type workspaceDiffResponse struct {
	Files []workspaceDiffFile `json:"files"`
	// Patch is the full unified diff text, capped at maxDiffPatchBytes. The
	// renderer renders it as a code block with +/- colouring.
	Patch string `json:"patch"`
	// Truncated reports that the patch was cut.
	Truncated bool `json:"truncated"`
	// Git reports whether the workspace had a git repo. False when init failed
	// (no git binary, permissions) — the renderer says "diff unavailable"
	// rather than "nothing changed".
	Git bool `json:"git"`
}

const maxDiffPatchBytes = 256 * 1024

func (s *Server) handleWorkspaceDiff(c *gin.Context) {
	if s.cfg.DB == nil || s.cfg.DataDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "workspace_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	// Same ownership check as the file listing: the workspace is keyed by uuid
	// alone, so the thread row is the authority on who owns it.
	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, identity.UID,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}

	root := filepath.Join(s.cfg.DataDir, "agent_workspace", "thread_"+threadUUID)
	resp := workspaceDiffResponse{}

	// numstat: one line per changed file — "<added>\t<removed>\t<path>"
	numstat := exec.Command("git", "diff", "HEAD", "--numstat")
	numstat.Dir = root
	numstatOut, nerr := numstat.Output()
	if nerr != nil {
		// No git repo, or git not found. Report it rather than pretending
		// nothing changed.
		resp.Git = false
		c.JSON(http.StatusOK, resp)
		return
	}
	resp.Git = true

	for _, line := range strings.Split(strings.TrimSpace(string(numstatOut)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		resp.Files = append(resp.Files, workspaceDiffFile{
			Path:    parts[2],
			Added:   added,
			Removed: removed,
		})
	}

	// Full unified diff, capped. The cap is a safety valve, not a design
	// choice: a turn that writes a 50KB file produces a 50KB diff, which is
	// fine; a turn that regenerates a 10MB data file should not ship it over
	// loopback and into the DOM.
	diff := exec.Command("git", "diff", "HEAD", "--no-color")
	diff.Dir = root
	diffOut, derr := diff.Output()
	if derr != nil {
		c.JSON(http.StatusOK, resp)
		return
	}
	patch := string(diffOut)
	if len(patch) > maxDiffPatchBytes {
		patch = patch[:maxDiffPatchBytes] + "\n… (diff truncated)"
		resp.Truncated = true
	}
	resp.Patch = patch

	c.JSON(http.StatusOK, resp)
}
