//go:build desktop

package local_render

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	migrationsdesktop "server/desktop/migrations_desktop"
)

func openLocalRenderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:local-render-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// minimalPNG encodes a 1×1 PNG so the content sniff recognizes image/png.
func minimalPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestStore_SaveThreadFile_TextHappyPath(t *testing.T) {
	db := openLocalRenderTestDB(t)
	filesDir := t.TempDir()
	store := NewStore(db, filesDir)

	content := []byte("hello world notes")
	saved, err := store.SaveThreadFile(42, 7, "thr-1", "notes.txt", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}
	if saved.FileID == 0 {
		t.Fatal("FileID = 0")
	}
	if saved.FileName != "notes.txt" {
		t.Fatalf("FileName = %q", saved.FileName)
	}
	if saved.FileSize != int64(len(content)) {
		t.Fatalf("FileSize = %d, want %d", saved.FileSize, len(content))
	}

	// 落盘到 <filesDir>/<threadUUID>/<filename>。
	got, err := os.ReadFile(filepath.Join(filesDir, "thr-1", "notes.txt"))
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("persisted content mismatch: got %q", got)
	}

	// thread_file 行 + 非空 dedup_key + file_hash。
	var (
		rowCount int
		dedup    string
		hash     string
	)
	db.Raw(`SELECT COUNT(*) FROM w_workagent_thread_file WHERE id = ?`, saved.FileID).Row().Scan(&rowCount)
	if rowCount != 1 {
		t.Fatalf("thread_file row count = %d, want 1", rowCount)
	}
	db.Raw(`SELECT dedup_key, file_hash FROM w_workagent_thread_file WHERE id = ?`, saved.FileID).Row().Scan(&dedup, &hash)
	if dedup == "" {
		t.Fatal("dedup_key empty")
	}
	if hash == "" {
		t.Fatal("file_hash empty")
	}
}

func TestStore_SaveThreadFile_PNGAllowed(t *testing.T) {
	db := openLocalRenderTestDB(t)
	store := NewStore(db, t.TempDir())

	pngBytes := minimalPNG(t)
	saved, err := store.SaveThreadFile(42, 7, "thr-1", "pic.png", bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png should be allowed: %v", err)
	}
	if saved.FileType != "image" {
		t.Fatalf("FileType = %q, want image", saved.FileType)
	}
	if !strings.HasPrefix(saved.MimeType, "image/") {
		t.Fatalf("MimeType = %q", saved.MimeType)
	}
}

func TestStore_SaveThreadFile_UnsupportedHTMLRejected(t *testing.T) {
	db := openLocalRenderTestDB(t)
	store := NewStore(db, t.TempDir())

	html := []byte("<html><body><script>alert(1)</script></body></html>")
	_, err := store.SaveThreadFile(42, 7, "thr-1", "bad.html", bytes.NewReader(html))
	if !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("want ErrUnsupportedFileType, got %v", err)
	}
	// 拒绝后不留行、不留盘。
	var n int
	db.Raw(`SELECT COUNT(*) FROM w_workagent_thread_file`).Row().Scan(&n)
	if n != 0 {
		t.Fatalf("rejected upload left %d row(s)", n)
	}
}

func TestStore_SaveThreadFile_IdempotentSameFilename(t *testing.T) {
	db := openLocalRenderTestDB(t)
	filesDir := t.TempDir()
	store := NewStore(db, filesDir)

	first, err := store.SaveThreadFile(42, 7, "thr-1", "doc.txt", bytes.NewReader([]byte("v1")))
	if err != nil {
		t.Fatal(err)
	}
	// 同 filename 重传（不同内容）→ 复用同一行（dedup_key 相同），不新增行。
	second, err := store.SaveThreadFile(42, 7, "thr-1", "doc.txt", bytes.NewReader([]byte("v2-replaced")))
	if err != nil {
		t.Fatal(err)
	}
	if second.FileID != first.FileID {
		t.Fatalf("idempotent re-upload FileID = %d, want %d", second.FileID, first.FileID)
	}
	var n int
	db.Raw(`SELECT COUNT(*) FROM w_workagent_thread_file WHERE file_name = ?`, "doc.txt").Row().Scan(&n)
	if n != 1 {
		t.Fatalf("idempotent re-upload left %d row(s), want 1", n)
	}
	// 盘上内容被覆盖为 v2。
	got, err := os.ReadFile(filepath.Join(filesDir, "thr-1", "doc.txt"))
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(got) != "v2-replaced" {
		t.Fatalf("persisted content = %q, want v2-replaced", got)
	}
}

func TestStore_SaveThreadFile_PathTraversalSanitized(t *testing.T) {
	db := openLocalRenderTestDB(t)
	filesDir := t.TempDir()
	store := NewStore(db, filesDir)

	// 攻击者传入含目录穿越的名字：应被 sanitize 到 base name，落盘仍在 thread 目录内。
	saved, err := store.SaveThreadFile(42, 7, "thr-1", "../../../../etc/passwd", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}
	if saved.FileName != "passwd" {
		t.Fatalf("FileName = %q, want passwd", saved.FileName)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "thr-1", "passwd")); err != nil {
		t.Fatalf("sanitized file not in thread dir: %v", err)
	}
}
