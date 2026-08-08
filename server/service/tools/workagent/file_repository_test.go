package workagent

import (
	"fmt"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// FileRepository tests pin three contracts:
//
//  1. uid is in the WHERE clause for owner-scoped reads — never load-
//     then-compare. Cross-tenant returns gorm.ErrRecordNotFound, same
//     shape as the missing-row case (CWE-639 defence).
//
//  2. uid==0 short-circuits to ErrRecordNotFound. Defence in depth so
//     a future caller path that lost the uid context can't iterate
//     all users' files.
//
//  3. Bulk lookup silently drops cross-tenant ids rather than leaking
//     existence. Mirrors the per-row defence at scale.

func newFileRepo(t *testing.T) (*FileRepository, *gorm.DB) {
	t.Helper()
	db := testutil.NewTestDB(t)
	return NewFileRepository(db), db
}

func seedFile(t *testing.T, db *gorm.DB, override func(*workagentModel.ThreadFile)) uint {
	t.Helper()
	file := workagentModel.ThreadFile{
		UID:        1,
		ThreadID:   100,
		FileName:   "seed.txt",
		FilePath:   "uploads/seed.txt",
		FileSize:   1024,
		FileType:   "text/plain",
		FileSource: workagentModel.FileSourceUpload,
	}
	if override != nil {
		override(&file)
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return file.Id
}

func TestFileRepository_LoadByIDForOwner_HappyPath(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 42
	})

	got, err := repo.LoadByIDForOwner(fileID, 42)
	if err != nil {
		t.Fatalf("LoadByIDForOwner: %v", err)
	}
	if got.Id != fileID {
		t.Errorf("Id = %d, want %d", got.Id, fileID)
	}
}

func TestFileRepository_LoadByIDForOwner_CrossTenantReturnsNotFound(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 100
	})

	_, err := repo.LoadByIDForOwner(fileID, 99)
	if err != gorm.ErrRecordNotFound {
		t.Errorf("cross-tenant err = %v, want ErrRecordNotFound (CWE-639 regression)", err)
	}
}

func TestFileRepository_LoadByIDForOwner_RefusesZeroUID(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, nil)

	_, err := repo.LoadByIDForOwner(fileID, 0)
	if err != gorm.ErrRecordNotFound {
		t.Errorf("uid=0 err = %v, want ErrRecordNotFound", err)
	}
}

func TestFileRepository_LoadByStringIDForOwner_HappyPath(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 7
	})

	got, err := repo.LoadByStringIDForOwner(fmt.Sprintf("%d", fileID), 7)
	if err != nil {
		t.Fatalf("LoadByStringIDForOwner: %v", err)
	}
	if got.Id != fileID {
		t.Errorf("Id = %d, want %d", got.Id, fileID)
	}
}

func TestFileRepository_LoadByStringIDForOwner_EmptyAndZeroUIDShortCircuit(t *testing.T) {
	repo, _ := newFileRepo(t)
	if _, err := repo.LoadByStringIDForOwner("", 5); err != gorm.ErrRecordNotFound {
		t.Errorf("empty fileID err = %v, want ErrRecordNotFound", err)
	}
	if _, err := repo.LoadByStringIDForOwner("1", 0); err != gorm.ErrRecordNotFound {
		t.Errorf("uid=0 err = %v, want ErrRecordNotFound", err)
	}
}

func TestFileRepository_LoadByIDsForOwner_DropsCrossTenant(t *testing.T) {
	repo, db := newFileRepo(t)
	mineID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 11
	})
	theirsID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 99
	})

	got := repo.LoadByIDsForOwner(
		[]string{fmt.Sprintf("%d", mineID), fmt.Sprintf("%d", theirsID), "999999"},
		11,
	)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 (cross-tenant + missing both dropped silently)", len(got))
	}
	if _, ok := got[fmt.Sprintf("%d", mineID)]; !ok {
		t.Errorf("own file %d missing from result", mineID)
	}
	if _, ok := got[fmt.Sprintf("%d", theirsID)]; ok {
		t.Errorf("cross-tenant file %d leaked into result — IDOR regression", theirsID)
	}
}

func TestFileRepository_LoadByIDsForOwner_EmptyInputs(t *testing.T) {
	repo, _ := newFileRepo(t)
	if got := repo.LoadByIDsForOwner(nil, 1); len(got) != 0 {
		t.Errorf("nil input: got %d entries", len(got))
	}
	if got := repo.LoadByIDsForOwner([]string{"1"}, 0); len(got) != 0 {
		t.Errorf("uid=0: got %d entries", len(got))
	}
}

func TestFileRepository_ListByThread(t *testing.T) {
	repo, db := newFileRepo(t)
	for i := 0; i < 3; i++ {
		idx := i
		seedFile(t, db, func(f *workagentModel.ThreadFile) {
			f.UID = 5
			f.ThreadID = 50
			f.FileName = fmt.Sprintf("file-%d.txt", idx)
			f.FileSource = workagentModel.FileSourceUpload
		})
	}
	// Sibling thread row.
	seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 51
	})

	got, err := repo.ListByThread(5, 50, nil)
	if err != nil {
		t.Fatalf("ListByThread: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3 (sibling thread leaked in)", len(got))
	}

	// With source filter — only uploads.
	source := workagentModel.FileSourceUpload
	got, err = repo.ListByThread(5, 50, &source)
	if err != nil {
		t.Fatalf("ListByThread (source filter): %v", err)
	}
	if len(got) != 3 {
		t.Errorf("filtered len = %d, want 3 (all 3 are uploads)", len(got))
	}
}

func TestFileRepository_CountForOwner(t *testing.T) {
	repo, db := newFileRepo(t)
	for i := 0; i < 4; i++ {
		seedFile(t, db, func(f *workagentModel.ThreadFile) {
			f.UID = 3
			f.ThreadID = 33
		})
	}
	// Different uid — must NOT count.
	seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 99
		f.ThreadID = 33
	})

	n, err := repo.CountForOwner(3, 33)
	if err != nil {
		t.Fatalf("CountForOwner: %v", err)
	}
	if n != 4 {
		t.Errorf("count = %d, want 4 (cross-tenant row leaked into uid-scoped count)", n)
	}
}

func TestFileRepository_CountByThread(t *testing.T) {
	repo, db := newFileRepo(t)
	for _, uid := range []int{1, 2, 3} {
		oid := uid
		seedFile(t, db, func(f *workagentModel.ThreadFile) {
			f.UID = oid
			f.ThreadID = 77
		})
	}

	n, err := repo.CountByThread(77)
	if err != nil {
		t.Fatalf("CountByThread: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3 (uid-less variant must include all uids on the thread)", n)
	}
}
