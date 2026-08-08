package workagent

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// confinedToWorkspaceRoot is the defence-in-depth gate before any
// destructive os.RemoveAll on a resolved thread path. The contract:
//
//   - true  → safe: candidate is a strict descendant of workspaceRoot
//   - false → refuse: workspace root itself, outside the root, escapes
//     via symlink, missing inputs, or any abs() failure
//
// Pin both directions so a future "harden everything" pass can't
// silently flip the boolean and let a path-resolution bug nuke
// arbitrary directories.
func TestConfinedToWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()

	inside := filepath.Join(root, "uid", "1", "today", "thread_abc")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("seed inside: %v", err)
	}

	if !confinedToWorkspaceRoot(root, inside) {
		t.Errorf("descendant path should be confined")
	}
	// Empty inputs are explicit-no.
	if confinedToWorkspaceRoot("", inside) {
		t.Errorf("empty root must not pass")
	}
	if confinedToWorkspaceRoot(root, "") {
		t.Errorf("empty candidate must not pass")
	}

	// The root itself must NOT pass — refusing to RemoveAll the
	// workspace root is a critical safety bound.
	if confinedToWorkspaceRoot(root, root) {
		t.Errorf("workspace root itself must not be confined-as-deletable")
	}

	// Outside the root: refuse.
	outside := filepath.Join(other, "stuff")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("seed outside: %v", err)
	}
	if confinedToWorkspaceRoot(root, outside) {
		t.Errorf("path outside root must not be confined")
	}

	// Same-prefix-but-actually-sibling: /tmp/a vs /tmp/aaa-different.
	// confinedToWorkspaceRoot uses prefix+separator so this should
	// also be refused.
	prefixSibling := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-other")
	if err := os.MkdirAll(prefixSibling, 0o755); err != nil {
		t.Fatalf("seed sibling: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(prefixSibling) })
	if confinedToWorkspaceRoot(root, prefixSibling) {
		t.Errorf("prefix-sibling %q falsely accepted as inside %q", prefixSibling, root)
	}
}

// A symlink AT the candidate pointing outside the root must fail
// the confinement check — we want a malicious or buggy symlink to
// refuse, not transparently follow it and have os.RemoveAll nuke
// whatever is on the other end.
func TestConfinedToWorkspaceRoot_SymlinkAtCandidateEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "elsewhere")
	if err := os.MkdirAll(outsideTarget, 0o755); err != nil {
		t.Fatalf("seed outside target: %v", err)
	}

	// A symlink that lives inside the root but points outside.
	link := filepath.Join(root, "thread_evil")
	if err := os.Symlink(outsideTarget, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if confinedToWorkspaceRoot(root, link) {
		t.Errorf("symlink-at-candidate pointing outside root must NOT be confined")
	}
}

// macOS ships /var as a symlink to /private/var; without
// EvalSymlinks on the root the prefix check returns false for legit
// paths under /var/folders/.../TestX/123. Pin the symlink-handling
// behaviour so a future refactor that drops EvalSymlinks gets caught.
func TestConfinedToWorkspaceRoot_RootSymlinkResolves(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("seed real root: %v", err)
	}
	linkRoot := filepath.Join(parent, "link-root")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	descendant := filepath.Join(realRoot, "thread_abc")
	if err := os.MkdirAll(descendant, 0o755); err != nil {
		t.Fatalf("seed descendant: %v", err)
	}

	// Caller passes the symlink form as workspace root; descendant
	// resolves to the real path. EvalSymlinks on the root must
	// reconcile both sides so this returns true.
	if !confinedToWorkspaceRoot(linkRoot, descendant) {
		t.Errorf("symlinked root should resolve so descendant is confined")
	}
}

func newPutThreadLifecycleTestService(t *testing.T) (*ThreadLifecycleService, *gorm.DB) {
	t.Helper()
	db := testutil.NewTestDB(t)
	if err := db.Exec(`CREATE UNIQUE INDEX uk_thread_lifecycle_uuid ON w_workagent_thread(uuid)`).Error; err != nil {
		t.Fatalf("create thread UUID unique index: %v", err)
	}
	return NewThreadLifecycleService(db), db
}

func TestPutThreadNewFirstWriterWins(t *testing.T) {
	service, db := newPutThreadLifecycleTestService(t)
	const threadUUID = "123e4567-e89b-42d3-a456-426614174000"

	first, created, err := service.PutThreadNew(42, threadUUID, "Original", "ppt", 7)
	if err != nil {
		t.Fatalf("first PutThreadNew: %v", err)
	}
	if !created || first.Id == 0 || first.UID != 42 || first.UUID != threadUUID || first.Name != "Original" || first.AgentMode != "ppt" || first.ProjectID != 7 {
		t.Fatalf("first resource = %+v, created=%v", first, created)
	}

	if err := db.Model(&workagent.ChatThread{}).Where("id = ?", first.Id).Update("name", "Renamed Later").Error; err != nil {
		t.Fatalf("rename stored thread: %v", err)
	}
	replay, replayCreated, err := service.PutThreadNew(42, threadUUID, "Ignored Replay", "flashCard", 99)
	if err != nil {
		t.Fatalf("replay PutThreadNew: %v", err)
	}
	if replayCreated || replay.Id != first.Id || replay.Name != "Renamed Later" || replay.AgentMode != "ppt" || replay.ProjectID != 7 {
		t.Fatalf("replay resource = %+v, created=%v", replay, replayCreated)
	}

	var count int64
	if err := db.Model(&workagent.ChatThread{}).Where("uuid = ?", threadUUID).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows for UUID = %d, want 1", count)
	}
}

func TestPutThreadNewRejectsCrossOwnerUUIDReplay(t *testing.T) {
	service, db := newPutThreadLifecycleTestService(t)
	const threadUUID = "123e4567-e89b-42d3-a456-426614174000"

	first, created, err := service.PutThreadNew(42, threadUUID, "Owner A", "ppt", 0)
	if err != nil || !created {
		t.Fatalf("seed owner A: thread=%+v created=%v err=%v", first, created, err)
	}
	got, gotCreated, err := service.PutThreadNew(99, threadUUID, "Owner B", "ppt", 0)
	if !errors.Is(err, ErrThreadUUIDOwnedByAnotherUser) {
		t.Fatalf("cross-owner error = %v, want ErrThreadUUIDOwnedByAnotherUser", err)
	}
	if got != nil || gotCreated {
		t.Fatalf("cross-owner result = %+v, created=%v", got, gotCreated)
	}

	var stored workagent.ChatThread
	if err := db.Where("uuid = ?", threadUUID).First(&stored).Error; err != nil {
		t.Fatalf("load stored owner: %v", err)
	}
	if stored.UID != 42 || stored.Id != first.Id || stored.Name != "Owner A" {
		t.Fatalf("cross-owner replay changed resource: %+v", stored)
	}
}

func TestPutThreadNewConcurrentCreateIfAbsentCreatesOneRow(t *testing.T) {
	service, db := newPutThreadLifecycleTestService(t)
	const (
		threadUUID = "123e4567-e89b-42d3-a456-426614174000"
		callers    = 24
	)

	type result struct {
		thread  *workagent.ChatThread
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			thread, created, err := service.PutThreadNew(42, threadUUID, "Name", "ppt", uint(index))
			results <- result{thread: thread, created: created, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	createdCount := 0
	var winnerID uint
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent PutThreadNew: %v", result.err)
		}
		if result.thread == nil || result.thread.Id == 0 {
			t.Fatalf("concurrent result missing thread: %+v", result)
		}
		if winnerID == 0 {
			winnerID = result.thread.Id
		}
		if result.thread.Id != winnerID {
			t.Fatalf("thread ID = %d, want winner %d", result.thread.Id, winnerID)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created=true results = %d, want 1", createdCount)
	}

	var count int64
	if err := db.Model(&workagent.ChatThread{}).Where("uuid = ?", threadUUID).Count(&count).Error; err != nil {
		t.Fatalf("count concurrent rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("concurrent rows = %d, want 1", count)
	}
}

func TestPutThreadNewRejectsInvalidConfigurationAndPropagatesDatabaseFailure(t *testing.T) {
	validService, db := newPutThreadLifecycleTestService(t)
	const threadUUID = "123e4567-e89b-42d3-a456-426614174000"

	tests := []struct {
		name    string
		service *ThreadLifecycleService
		uid     int
		uuid    string
	}{
		{name: "nil receiver", service: nil, uid: 42, uuid: threadUUID},
		{name: "nil database", service: NewThreadLifecycleService(nil), uid: 42, uuid: threadUUID},
		{name: "zero user", service: validService, uid: 0, uuid: threadUUID},
		{name: "empty UUID", service: validService, uid: 42},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if thread, created, err := test.service.PutThreadNew(test.uid, test.uuid, "Name", "ppt", 0); err == nil || thread != nil || created {
				t.Fatalf("result = thread=%+v created=%v err=%v", thread, created, err)
			}
		})
	}

	if err := db.Exec("DROP TABLE w_workagent_thread").Error; err != nil {
		t.Fatalf("drop thread table: %v", err)
	}
	if thread, created, err := validService.PutThreadNew(42, threadUUID, "Name", "ppt", 0); err == nil || thread != nil || created {
		t.Fatalf("database failure result = thread=%+v created=%v err=%v", thread, created, err)
	}
}
