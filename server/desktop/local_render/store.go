//go:build desktop

// Package local_render owns local-first file attachment storage for the
// sidecar: uploading, disk persistence, and w_workagent_thread_file metadata.
// It is the desktop-side counterpart of the cloud workagent file pipeline
// (server/service/tools/workagent/file_service_upload.go), stripped of MySQL,
// globals, account pool, and the SDK workspace symlink layer — pure local disk
// + SQLite, scoped under the sidecar DataDir.
package local_render

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gorm.io/gorm"

	localinference "server/desktop/local_inference"
)

// MaxThreadFileUploadBytes is the per-file upload cap (50 MiB, aligned with the
// cloud workagent upload limit).
const MaxThreadFileUploadBytes int64 = 50 << 20

var (
	// ErrUploadTooLarge is returned when an upload exceeds MaxThreadFileUploadBytes.
	ErrUploadTooLarge = errors.New("upload exceeded size limit")
	// ErrUnsupportedFileType is returned when the sniffed MIME / extension is not
	// in the local allowlist (images, text, pdf, docx/pptx/xlsx).
	ErrUnsupportedFileType = errors.New("unsupported file type")
)

// SavedFile is the result of a successful upload: the row id (referenced later
// from /agent/chat payload.files) plus the sanitized metadata echoed to the
// renderer.
type SavedFile struct {
	FileID   int64  `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
}

// Store persists file attachments to local disk and writes w_workagent_thread_file
// metadata rows. It is the first writer of that table (cloud sync for thread
// files is still deferred). Safe for concurrent use (GORM/SQLite serializes).
type Store struct {
	db       *gorm.DB
	filesDir string // <DataDir>/thread_files
}

// NewStore wires the SQLite handle and the on-disk files root (ResolveDataDir()
// under <DataDir>/thread_files). filesDir is created lazily on first save.
func NewStore(db *gorm.DB, filesDir string) *Store {
	return &Store{db: db, filesDir: filesDir}
}

// Load resolves file ids to model-ready attachments (implements
// local_inference.AttachmentLoader). Each id is looked up in
// w_workagent_thread_file (scoped by uid so a stale cross-account id cannot
// leak content), read from disk, and extracted by MIME type. A missing or
// unreadable file fails the whole turn rather than silently dropping context.
func (s *Store) Load(fileIDs []int64, uid uint64) ([]localinference.Attachment, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	atts := make([]localinference.Attachment, 0, len(fileIDs))
	for _, id := range fileIDs {
		var filePath, mimeType, fileType string
		if err := s.db.Raw(
			`SELECT file_path, mime_type, file_type FROM w_workagent_thread_file
			  WHERE id = ? AND uid = ?`,
			id, uid,
		).Row().Scan(&filePath, &mimeType, &fileType); err != nil {
			return nil, fmt.Errorf("load attachment %d: %w", id, err)
		}
		att, err := ExtractFile(filepath.Join(s.filesDir, filePath), mimeType, fileType)
		if err != nil {
			return nil, fmt.Errorf("extract attachment %d: %w", id, err)
		}
		atts = append(atts, att)
	}
	return atts, nil
}

// SaveThreadFile sniffs + validates the upload, persists it to
// <filesDir>/<threadUUID>/<filename> (filename-scoped: re-upload of the same
// name overwrites the disk file and the metadata row — idempotent), then upserts
// the w_workagent_thread_file row. Returns the file id for /agent/chat reference.
//
// Security mirrors cloud writeUploadCapped: O_NOFOLLOW on create (symlink
// redirect hardening), CopyN(max+1) to refuse understated sizes, and a 512-byte
// content sniff with http.DetectContentType (filename alone is not trusted).
func (s *Store) SaveThreadFile(uid uint64, threadID uint64, threadUUID, filename string, src io.ReadSeeker) (SavedFile, error) {
	filename = sanitizeFilename(filename)
	if filename == "" {
		return SavedFile{}, errors.New("empty filename")
	}
	if threadID == 0 {
		return SavedFile{}, errors.New("thread_id is required")
	}

	mimeType, err := sniffMIME(src)
	if err != nil {
		return SavedFile{}, err
	}
	if !mimeTypeAllowed(mimeType, filename) {
		return SavedFile{}, fmt.Errorf("%w: %s", ErrUnsupportedFileType, mimeType)
	}

	// Persist to disk, hashing while we copy so we only read the body once.
	dst := filepath.Join(s.filesDir, threadUUID, filename)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return SavedFile{}, fmt.Errorf("create file dir: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := copyCappedHashed(dst, src, hasher)
	if copyErr != nil {
		return SavedFile{}, copyErr
	}
	fileHash := hex.EncodeToString(hasher.Sum(nil))

	relPath := filepath.ToSlash(filepath.Join(threadUUID, filename))
	dedupKey := computeDedupKey(filename, relPath)
	fileType := fileTypeFromMIME(mimeType, filename)

	fileID, err := s.upsertThreadFileRow(threadFileRow{
		uid: uid, threadID: threadID, fileName: filename, displayName: filename,
		size: written, fileType: fileType, mimeType: mimeType, filePath: relPath,
		fileHash: fileHash, dedupKey: dedupKey,
	})
	if err != nil {
		_ = os.Remove(dst) // rollback disk write on metadata failure
		return SavedFile{}, err
	}
	return SavedFile{
		FileID: fileID, FileName: filename, MimeType: mimeType,
		FileType: fileType, FileSize: written,
	}, nil
}

// copyCappedHashed writes src to dst with O_NOFOLLOW|O_CREATE|O_TRUNC, refusing
// to write more than MaxThreadFileUploadBytes, and feeding every byte through
// hasher. On size violation or write error the partial file is removed.
func copyCappedHashed(dst string, src io.Reader, hasher hash.Hash) (int64, error) {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer out.Close()
	written, err := io.CopyN(out, io.TeeReader(src, hasher), MaxThreadFileUploadBytes+1)
	if err != nil && err != io.EOF {
		_ = os.Remove(dst)
		return 0, fmt.Errorf("save file: %w", err)
	}
	if written > MaxThreadFileUploadBytes {
		_ = os.Remove(dst)
		return 0, ErrUploadTooLarge
	}
	return written, nil
}

func sniffMIME(src io.ReadSeeker) (string, error) {
	var buf [512]byte
	n, err := src.Read(buf[:])
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("inspect upload: %w", err)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind upload: %w", err)
	}
	if n == 0 {
		return "", errors.New("empty upload is not allowed")
	}
	return http.DetectContentType(buf[:n]), nil
}

// mimeTypeAllowed is the local allowlist. Images / text / pdf are allowed by
// content type; docx/pptx/xlsx are zip containers sniffed as application/zip so
// we gate them on extension too. text/html is refused (iframe sandbox escape
// risk, same rationale as the cloud workagent allowlist).
func mimeTypeAllowed(mimeType, filename string) bool {
	// http.DetectContentType returns media types with parameters (e.g.
	// "text/html; charset=utf-8"); strip them before comparing.
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case strings.HasPrefix(mt, "image/"):
		return true
	case mt == "application/pdf":
		return true
	case mt == "application/json":
		return true
	case strings.HasPrefix(mt, "text/"):
		return mt != "text/html"
	case mt == "application/zip" && (ext == ".docx" || ext == ".pptx" || ext == ".xlsx" || ext == ".xlsm"):
		return true
	}
	return false
}

func fileTypeFromMIME(mimeType, filename string) string {
	mt := strings.ToLower(mimeType)
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case strings.HasPrefix(mt, "image/"):
		return "image"
	case mt == "application/pdf":
		return "pdf"
	case ext == ".docx":
		return "word"
	case ext == ".pptx":
		return "ppt"
	case ext == ".xlsx" || ext == ".xlsm":
		return "excel"
	case mt == "application/json":
		return "json"
	case strings.HasPrefix(mt, "text/"):
		return "text"
	}
	return "other"
}

// sanitizeFilename strips any directory component and rejects pure-path names,
// preventing path traversal (../etc/passwd) — disk writes are always under
// <filesDir>/<threadUUID>/.
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "." || name == "/" || name == "\\" {
		return ""
	}
	return name
}

// computeDedupKey mirrors the cloud formula: SHA-256 of "filename|filePath".
// The unique index uk_w_workagent_thread_file_dedup on (uid, thread_id,
// dedup_key) makes re-upload of the same filename idempotent.
func computeDedupKey(filename, filePath string) string {
	h := sha256.Sum256([]byte(filename + "|" + filePath))
	return hex.EncodeToString(h[:])
}

type threadFileRow struct {
	uid         uint64
	threadID    uint64
	fileName    string
	displayName string
	size        int64
	fileType    string
	mimeType    string
	filePath    string
	fileHash    string
	dedupKey    string
}

// upsertThreadFileRow inserts a new w_workagent_thread_file row, or — if a row
// with the same (uid, thread_id, dedup_key) already exists (same filename
// re-uploaded) — updates it in place. Returns the row id.
func (s *Store) upsertThreadFileRow(r threadFileRow) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var existingID int64
	scanErr := s.db.Raw(
		`SELECT id FROM w_workagent_thread_file
		  WHERE uid = ? AND thread_id = ? AND dedup_key = ?`,
		r.uid, r.threadID, r.dedupKey,
	).Row().Scan(&existingID)
	if scanErr == nil && existingID > 0 {
		if err := s.db.Exec(
			`UPDATE w_workagent_thread_file
			    SET file_name = ?, display_name = ?, file_size = ?, file_type = ?,
			        mime_type = ?, file_path = ?, file_hash = ?, file_source = 'upload',
			        exists_on_disk = 1, updated_at = ?
			  WHERE id = ?`,
			r.fileName, r.displayName, r.size, r.fileType, r.mimeType,
			r.filePath, r.fileHash, now, existingID,
		).Error; err != nil {
			return 0, fmt.Errorf("update thread file row: %w", err)
		}
		return existingID, nil
	}
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return 0, fmt.Errorf("lookup thread file row: %w", scanErr)
	}

	if err := s.db.Exec(
		`INSERT INTO w_workagent_thread_file
		     (uid, thread_id, message_id, file_name, display_name, file_size,
		      file_type, mime_type, file_path, file_source, file_hash, dedup_key,
		      global_asset_id, exists_on_disk, created_at, updated_at)
		   VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, 'upload', ?, ?, 0, 1, ?, ?)`,
		r.uid, r.threadID, r.fileName, r.displayName, r.size, r.fileType,
		r.mimeType, r.filePath, r.fileHash, r.dedupKey, now, now,
	).Error; err != nil {
		return 0, fmt.Errorf("insert thread file row: %w", err)
	}
	var id int64
	if err := s.db.Raw(`SELECT last_insert_rowid()`).Row().Scan(&id); err != nil {
		return 0, fmt.Errorf("read last insert id: %w", err)
	}
	return id, nil
}

// ThreadFile is one attachment as the renderer needs to show it.
//
// Deliberately not the storage row: file_path stays inside this package. The
// renderer names a file by id when it attaches one to a turn, and never needs
// to know where on disk it lives.
type ThreadFile struct {
	FileID    int64  `json:"file_id"`
	FileName  string `json:"file_name"`
	FileSize  int64  `json:"file_size"`
	FileType  string `json:"file_type"`
	MimeType  string `json:"mime_type"`
	OnDisk    bool   `json:"on_disk"`
	CreatedAt string `json:"created_at"`
}

// ListThreadFiles returns the attachments of one thread, newest first.
//
// The upload path has always persisted these rows, but nothing could read them
// back — the renderer only knew about files it had uploaded during the current
// session, so a reload lost the list while the files stayed on disk. Scoping by
// uid is what keeps one local user's attachments out of another's, the same
// filter every other local-history query uses.
func (s *Store) ListThreadFiles(uid uint64, threadID uint64) ([]ThreadFile, error) {
	rows, err := s.db.Raw(
		`SELECT id, file_name, display_name, file_size, file_type, mime_type,
		        exists_on_disk, created_at
		   FROM w_workagent_thread_file
		  WHERE uid = ? AND thread_id = ?
		  ORDER BY id DESC`,
		uid, threadID,
	).Rows()
	if err != nil {
		return nil, fmt.Errorf("list thread files: %w", err)
	}
	defer rows.Close()

	out := []ThreadFile{}
	for rows.Next() {
		var (
			f           ThreadFile
			displayName string
			onDisk      int
		)
		if err := rows.Scan(&f.FileID, &f.FileName, &displayName, &f.FileSize,
			&f.FileType, &f.MimeType, &onDisk, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan thread file: %w", err)
		}
		// display_name is what the user saw when they picked the file; fall
		// back to the stored name so a row written before it was populated
		// still shows something.
		if displayName != "" {
			f.FileName = displayName
		}
		f.OnDisk = onDisk == 1
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate thread files: %w", err)
	}
	return out, nil
}

// DeleteThreadFiles removes a thread's attachments: the bytes on disk and the
// rows that name them. It returns the ids of every row it deleted, so the
// caller can clear whatever else was derived from those files (the knowledge
// index, today).
//
// Disk removal is best-effort per file and does not abort the delete: the row
// is the authority (a row without bytes is already a state this store
// tolerates — exists_on_disk), and a delete that stops halfway because one
// file was already gone would strand the rest forever.
func (s *Store) DeleteThreadFiles(uid uint64, threadID uint64, threadUUID string) ([]int64, error) {
	rows, err := s.db.Raw(
		`SELECT id, file_path FROM w_workagent_thread_file
		  WHERE uid = ? AND thread_id = ?`,
		uid, threadID,
	).Rows()
	if err != nil {
		return nil, fmt.Errorf("delete thread files: list: %w", err)
	}
	var ids []int64
	var paths []string
	for rows.Next() {
		var (
			id   int64
			path string
		)
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return nil, fmt.Errorf("delete thread files: scan: %w", err)
		}
		ids = append(ids, id)
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("delete thread files: iterate: %w", err)
	}
	rows.Close()
	if len(ids) == 0 {
		return nil, nil
	}

	// Rows first: if this fails nothing has been touched, and if the disk pass
	// fails afterwards the worst outcome is orphaned bytes — recoverable by
	// hand — rather than rows that promise files which are gone.
	if err := s.db.Exec(
		`DELETE FROM w_workagent_thread_file WHERE uid = ? AND thread_id = ?`,
		uid, threadID,
	).Error; err != nil {
		return nil, fmt.Errorf("delete thread files: rows: %w", err)
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		// file_path is stored relative to filesDir — the same join Load uses.
		if err := os.Remove(filepath.Join(s.filesDir, path)); err != nil && !os.IsNotExist(err) {
			log.Printf("local files: delete %s: %v", path, err)
		}
	}
	// The per-thread directory, if it is now empty. Not an error if it isn't:
	// a file that failed to delete above still lives there.
	if threadUUID != "" {
		_ = os.Remove(filepath.Join(s.filesDir, threadUUID))
	}
	return ids, nil
}

// FileNames maps file ids to the name the user knows them by.
//
// Retrieval hands back the ids stored on knowledge chunks; naming them is what
// lets the answer say which document it drew on. Ids not owned by uid are
// simply absent from the result — the caller labels those generically, so an
// id that belongs to another local identity can never surface under a name.
func (s *Store) FileNames(uid uint64, fileIDs []int64) (map[int64]string, error) {
	if len(fileIDs) == 0 {
		return map[int64]string{}, nil
	}
	rows, err := s.db.Raw(
		`SELECT id, file_name, display_name
		   FROM w_workagent_thread_file
		  WHERE uid = ? AND id IN ?`,
		uid, fileIDs,
	).Rows()
	if err != nil {
		return nil, fmt.Errorf("file names: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]string, len(fileIDs))
	for rows.Next() {
		var (
			id                    int64
			fileName, displayName string
		)
		if err := rows.Scan(&id, &fileName, &displayName); err != nil {
			return nil, fmt.Errorf("scan file name: %w", err)
		}
		if displayName != "" {
			fileName = displayName
		}
		out[id] = fileName
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file names: %w", err)
	}
	return out, nil
}
