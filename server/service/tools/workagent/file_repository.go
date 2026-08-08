package workagent

import (
	"fmt"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

// FileRepository is the pure-CRUD slice of the file service. It owns
// the SELECT / COUNT shapes against w_workagent_thread_file with no
// filesystem dependencies, no asset-ledger upsert, no per-thread
// locking. Pulled out of FileService (which still owns those
// orchestration concerns) so callers that just need to read file
// metadata can do so without dragging in the upload pipeline.
//
// FileService delegates its read-side methods to a FileRepository
// instance so existing call sites keep working unchanged. New
// callers in the workagent package should reach for
// DefaultFileRepository directly when they only need a row read.
//
// Why a struct (and not just package-level functions): mirrors
// ThreadRepository / MessageRepository so a test can inject a
// fixture *gorm.DB via NewFileRepository(db) without touching the
// globals map. Keeps the test surface symmetric across the three
// data-access types in this package.
type FileRepository struct {
	db *gorm.DB
}

// NewFileRepository wraps a *gorm.DB. Tests construct against a
// fixture DB; production calls DefaultFileRepository for the system
// connection.
func NewFileRepository(db *gorm.DB) *FileRepository {
	return &FileRepository{db: db}
}

// DefaultFileRepository binds to globals.GraDBs["system"]. Convenience
// for handler code with no other reason to plumb a *gorm.DB through.
func DefaultFileRepository() *FileRepository {
	return NewFileRepository(globals.GraDBs["system"])
}

// LoadByIDForOwner reads one ThreadFile by id, scoped to uid in the
// WHERE clause. Returns gorm.ErrRecordNotFound for both missing and
// cross-tenant cases — same CWE-639 defence pattern as
// ThreadRepository.LoadByIDForOwner. Centralises the file-ownership
// load that the api package was repeating in 3 different handlers
// (DownloadFile, PreviewFile, DeleteFile).
//
// uid==0 short-circuits before the DB hit. fileID is the numeric
// primary key (not the string UUID) — file_api.go's handlers parse
// the URL :id param into a uint64 first.
func (r *FileRepository) LoadByIDForOwner(fileID uint, uid uint) (*workagentModel.ThreadFile, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	file := &workagentModel.ThreadFile{}
	if err := r.db.
		Where("id = ? AND uid = ?", fileID, uid).
		First(file).Error; err != nil {
		return nil, err
	}
	return file, nil
}

// LoadByStringIDForOwner is the string-id variant. agent_api.go's
// resolveAgentInputFiles flow carries file IDs as strings (they
// arrive from the SDK as JSON values); keeping the string shape
// avoids forcing the caller to parse-then-pass.
func (r *FileRepository) LoadByStringIDForOwner(fileID string, uid uint) (*workagentModel.ThreadFile, error) {
	if uid == 0 || fileID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	file := &workagentModel.ThreadFile{}
	if err := r.db.
		Where("id = ? AND uid = ?", fileID, uid).
		First(file).Error; err != nil {
		return nil, err
	}
	return file, nil
}

// LoadByIDsForOwner is the bulk equivalent: one SELECT for many file
// IDs, scoped to uid so cross-tenant ids are silently dropped rather
// than leaked. Used by resolveAgentInputFiles to avoid an N+1 when
// the user attaches multiple files to one chat turn.
//
// Returns a map keyed by fileID's string form so the caller doesn't
// have to re-marshal IDs to look up results. Empty input → empty
// map. DB errors are logged-and-swallowed (best-effort prefetch);
// callers fall back to per-id lookups if any expected file is
// missing from the map.
func (r *FileRepository) LoadByIDsForOwner(fileIDs []string, uid uint) map[string]*workagentModel.ThreadFile {
	out := make(map[string]*workagentModel.ThreadFile, len(fileIDs))
	if len(fileIDs) == 0 || uid == 0 {
		return out
	}
	var rows []workagentModel.ThreadFile
	if err := r.db.
		Where("id IN ? AND uid = ?", fileIDs, uid).
		Find(&rows).Error; err != nil {
		globals.Warn(fmt.Sprintf("[FileRepository] Batch lookup failed: %v", err))
		return out
	}
	for i := range rows {
		row := rows[i]
		out[fmt.Sprintf("%d", row.Id)] = &row
	}
	return out
}

// ListByThread reads up to threadFileListLimit files for one thread,
// optionally filtered by source (upload / output / system). uid
// scopes the WHERE so an admin path that forgets the predicate
// doesn't iterate every user's files.
//
// Returns ThreadFileResponse rather than the raw model so the caller
// gets the canonical wire shape — same ToResponse mapping the
// previous FileService inline path emitted.
func (r *FileRepository) ListByThread(uid int, threadID uint, source *workagentModel.FileSource) ([]workagentModel.ThreadFileResponse, error) {
	files, err := r.ListRawByThread(uid, threadID, source)
	if err != nil {
		return nil, err
	}
	responses := make([]workagentModel.ThreadFileResponse, len(files))
	for i, f := range files {
		responses[i] = f.ToResponse()
	}
	return responses, nil
}

// ListRawByThread is the raw-row variant used by P1 artifact views
// and lifecycle cleanup paths that need fields not exposed on
// ThreadFileResponse (hash, global asset id, last sync). Same query
// shape and uid/source scoping as ListByThread.
func (r *FileRepository) ListRawByThread(uid int, threadID uint, source *workagentModel.FileSource) ([]workagentModel.ThreadFile, error) {
	var files []workagentModel.ThreadFile
	q := r.db.Where("uid = ? AND thread_id = ?", uid, threadID)
	if source != nil {
		q = q.Where("file_source = ?", *source)
	}
	if err := q.Order("created_at DESC").Limit(threadFileListLimit).Find(&files).Error; err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	return files, nil
}

// CountForOwner returns the file count for one (uid, threadID) pair.
// uid-scoped variant — for the user-facing file-count display.
func (r *FileRepository) CountForOwner(uid int, threadID uint) (int, error) {
	var count int64
	if err := r.db.Model(&workagentModel.ThreadFile{}).
		Where("uid = ? AND thread_id = ?", uid, threadID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count files for owner: %w", err)
	}
	return int(count), nil
}

// CountByThread is the uid-less variant — used by the per-turn
// statistics path where the uid isn't locally available and the
// count is owner-agnostic anyway (every file in a thread shares the
// thread's uid by table invariant).
//
// Distinct from CountForOwner so a uid-scoped caller can't
// accidentally drop the uid predicate by calling the wrong helper.
func (r *FileRepository) CountByThread(threadID uint) (int64, error) {
	var count int64
	if err := r.db.Model(&workagentModel.ThreadFile{}).
		Where("thread_id = ?", threadID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count files by thread: %w", err)
	}
	return count, nil
}
