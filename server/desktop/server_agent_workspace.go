//go:build desktop

package desktop

import (
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	uid, _, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}
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
	uid, _, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}
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
