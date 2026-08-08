package workagent

// agent_file_helpers.go holds the thin file-lookup shims that the
// agent flow uses to fetch ThreadFile rows by id with uid scoping.
// Pulled out of agent_api.go (B1 chunk 5) for grep-readability —
// these aren't part of any one phase of the turn flow, they're
// just the package-internal facade over FileService /
// FileRepository's owner-scoped reads.
//
// getFileByIDFromDB is the test-friendly variant retained for
// agent_api_test.go's existing TestGetFileByIDFromDB_* cases that
// drive against an injected *gorm.DB; the production wrappers
// delegate to FileService so the canonical owner-scoped query
// lives in one place.

import (
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"

	"gorm.io/gorm"
)

// getFileByID retrieves file information from database, scoped to the
// requesting user. Without the uid filter, callers could reference any
// file row by ID alone, letting them symlink another user's uploads or
// generated artifacts into their own agent workspace (CWE-639 IDOR).
func getFileByID(fileID string, uid uint) (*workagentModel.ThreadFile, error) {
	// FileService owns the canonical owner-scoped file load now —
	// keep this thin shim so the existing call sites in this file and
	// agent_chat_helpers.go don't need to switch to the longer name
	// at every call. The test-friendly variant (getFileByIDFromDB) is
	// still around for unit tests that drive against an injected DB.
	return workagentService.GetFileService().LoadFileForOwnerByStringID(fileID, uid)
}

// getFileByIDFromDB is the test-friendly core of getFileByID — same
// query, but uses the supplied *gorm.DB so tests can drive it against
// an in-memory SQLite without touching the package-level globals map.
func getFileByIDFromDB(db *gorm.DB, fileID string, uid uint) (*workagentModel.ThreadFile, error) {
	var file workagentModel.ThreadFile
	if err := db.Where("id = ? AND uid = ?", fileID, uid).First(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

// getFilesByIDs fetches a batch of files in a single query, scoped to
// the requesting user (same CWE-639 boundary as getFileByID — IDs not
// belonging to uid are silently dropped, not leaked). Returns a map
// keyed by the string form of file_id so the caller doesn't have to
// re-marshal IDs to look up results. Empty input → empty map (caller
// can treat as miss-all).
//
// Replaces N+1 lookups in resolveAgentInputFiles, which used to call
// getFileByID once per attached file — typical chat with 10 files =
// 10 round-trips on every send.
func getFilesByIDs(fileIDs []string, uid uint) map[string]*workagentModel.ThreadFile {
	// Same uid-scoped batch as before — moved into FileService so the
	// owner-scoped file query lives in one place. Behaviour preserved:
	// best-effort, errors swallowed with a warn log, missing IDs just
	// don't appear in the map.
	return workagentService.GetFileService().LoadFilesByIDsForOwner(fileIDs, uid)
}

