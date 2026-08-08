package workagent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// getFileByIDFromDB enforces uid scoping so a user cannot reference
// another user's file row by id alone — the original CWE-639 IDOR was
// "id = ?" without the uid clause. The cases below pin both halves of
// the contract: own-file lookups still work, foreign-uid lookups
// return ErrRecordNotFound (not the row).

func TestGetFileByIDFromDB_OwnUserCanRead(t *testing.T) {
	db := testutil.NewTestDB(t)

	mine := workagentModel.ThreadFile{
		UID:      42,
		ThreadID: 7,
		FileName: "my.pdf",
		FilePath: "uid/42/.../my.pdf",
	}
	if err := db.Create(&mine).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := getFileByIDFromDB(db, strconv.FormatUint(uint64(mine.Id), 10), 42)
	if err != nil {
		t.Fatalf("own user lookup failed: %v", err)
	}
	if got.UID != 42 || got.FileName != "my.pdf" {
		t.Errorf("got %+v, want uid=42 file=my.pdf", got)
	}
}

func TestResolveAgentInputFiles_RejectsClientPathWithoutOwnedID(t *testing.T) {
	testutil.NewTestDB(t)
	root := t.TempDir()
	threadWorkspace := filepath.Join(root, "threads", "1")
	if err := os.MkdirAll(threadWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir thread workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(threadWorkspace, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	files, err := resolveAgentInputFiles([]FileInfo{{
		Name: "secret.txt",
		Path: "threads/1/secret.txt",
	}}, 42, root, threadWorkspace)
	if err == nil {
		t.Fatalf("resolveAgentInputFiles should reject path-only file references")
	}
	if len(files) != 0 {
		t.Fatalf("path-only file should be rejected, got %+v", files)
	}
}

func TestGetFileByIDFromDB_OtherUserBlocked(t *testing.T) {
	db := testutil.NewTestDB(t)

	victim := workagentModel.ThreadFile{
		UID:      11, // owned by user 11
		ThreadID: 3,
		FileName: "secret.pdf",
		FilePath: "uid/11/.../secret.pdf",
	}
	if err := db.Create(&victim).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Attacker is uid=99; they pass the victim's file id but should
	// get ErrRecordNotFound, not the row.
	_, err := getFileByIDFromDB(db, strconv.FormatUint(uint64(victim.Id), 10), 99)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("attacker lookup must return ErrRecordNotFound, got %v", err)
	}
}

func TestGetFileByIDFromDB_NonexistentID(t *testing.T) {
	db := testutil.NewTestDB(t)

	_, err := getFileByIDFromDB(db, "99999", 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("nonexistent id should return ErrRecordNotFound, got %v", err)
	}
}

func TestGetFileByIDFromDB_ZeroUIDDoesNotMatchAll(t *testing.T) {
	// Defensive: a zero/missing uid must not bypass the filter and
	// match orphan rows (uid=0). We seed a uid=0 row to be sure.
	db := testutil.NewTestDB(t)

	orphan := workagentModel.ThreadFile{
		UID:      0,
		ThreadID: 1,
		FileName: "orphan.pdf",
	}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	other := workagentModel.ThreadFile{
		UID:      77,
		ThreadID: 1,
		FileName: "owned.pdf",
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Looking up the uid=77 file with uid=0 must not return it.
	_, err := getFileByIDFromDB(db, strconv.FormatUint(uint64(other.Id), 10), 0)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("uid=0 lookup of uid=77 row must be denied, got err=%v", err)
	}
}
