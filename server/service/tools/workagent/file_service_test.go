package workagent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// TestLockForThread_SerializesSameThread pins the per-thread mutex
// contract: two callers asking for the same threadID can't both hold
// the lock at once. This is the existing-files-map race the global
// mutex used to prevent.
func TestLockForThread_SerializesSameThread(t *testing.T) {
	s := &FileService{}
	const threadID = uint(42)

	unlock := s.lockForThread(threadID)

	gotLock := make(chan struct{})
	go func() {
		s.lockForThread(threadID)()
		close(gotLock)
	}()

	select {
	case <-gotLock:
		t.Fatal("second caller for same thread should have blocked while first holds the lock")
	case <-time.After(20 * time.Millisecond):
		// expected: second goroutine is still waiting
	}

	unlock()

	select {
	case <-gotLock:
		// expected: second goroutine acquired after first released
	case <-time.After(50 * time.Millisecond):
		t.Fatal("second caller didn't acquire after first released")
	}
}

// TestLockForThread_DifferentThreadsIndependent verifies the whole
// point of this refactor: a long-running sync on thread A doesn't
// block a sync on thread B. The previous global s.mu serialized
// everyone behind whoever was running.
func TestLockForThread_DifferentThreadsIndependent(t *testing.T) {
	s := &FileService{}
	unlockA := s.lockForThread(1)
	defer unlockA()

	gotB := make(chan struct{})
	go func() {
		s.lockForThread(2)()
		close(gotB)
	}()

	select {
	case <-gotB:
		// expected: thread 2 wasn't blocked by thread 1's lock
	case <-time.After(50 * time.Millisecond):
		t.Fatal("thread 2 should not have been blocked by thread 1's lock")
	}
}

// TestLockForThread_ConcurrentLoadOrStoreSafe — many goroutines all
// asking for the same threadID's mutex must end up sharing one mutex,
// never getting separate ones (which would defeat the lock).
func TestLockForThread_ConcurrentLoadOrStoreSafe(t *testing.T) {
	s := &FileService{}
	const threadID = uint(7)

	var counter int
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := s.lockForThread(threadID)
			defer unlock()
			counter++
		}()
	}
	wg.Wait()

	if counter != 100 {
		t.Errorf("counter = %d, want 100 — lock didn't serialize properly", counter)
	}
}

// TestIsDuplicateKeyError pins the unwrap path. GORM wraps the driver
// error inside its own; if isDuplicateKeyError ever stops using
// errors.As (or the errno changes), AddFile silently turns a
// duplicate-key conflict into a 500 instead of recovering — exactly
// the regression this test catches.

func TestIsDuplicateKeyError_RecognizesMySQL1062(t *testing.T) {
	dup := &mysql.MySQLError{Number: mysqlDuplicateKey, Message: "Duplicate entry 'x' for key 'uk_uid_thread_dedup'"}
	if !isDuplicateKeyError(dup) {
		t.Error("expected #1062 to be recognized as duplicate-key")
	}
}

func TestIsDuplicateKeyError_OtherMySQLNumbers(t *testing.T) {
	cases := []uint16{
		1064, // SQL syntax error
		1452, // FK constraint
		2002, // connection refused
	}
	for _, n := range cases {
		err := &mysql.MySQLError{Number: n, Message: "x"}
		if isDuplicateKeyError(err) {
			t.Errorf("MySQL error #%d should not be classified as duplicate-key", n)
		}
	}
}

func TestIsDuplicateKeyError_UnwrapsWrappedError(t *testing.T) {
	// GORM wraps driver errors via fmt.Errorf("...: %w", ...). The
	// helper relies on errors.As to peel that back. A plain wrap with
	// %w must still classify correctly.
	dup := &mysql.MySQLError{Number: mysqlDuplicateKey, Message: "x"}
	wrapped := fmt.Errorf("failed to create record: %w", dup)
	if !isDuplicateKeyError(wrapped) {
		t.Error("expected wrapped #1062 to still be recognized")
	}

	doubleWrapped := fmt.Errorf("outer layer: %w", wrapped)
	if !isDuplicateKeyError(doubleWrapped) {
		t.Error("expected double-wrapped #1062 to still be recognized")
	}
}

func TestIsDuplicateKeyError_NonMySQLError(t *testing.T) {
	if isDuplicateKeyError(errors.New("plain string error")) {
		t.Error("plain error should not be classified as duplicate-key")
	}
	if isDuplicateKeyError(nil) {
		t.Error("nil should not be classified as duplicate-key")
	}
}

func TestAddFile_RejectsCrossTenantThread(t *testing.T) {
	db := testutil.NewTestDB(t)
	previous := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previous })

	thread := workagentModel.ChatThread{
		UID:  100,
		UUID: "thread-owned-by-100",
		Name: "owned",
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	s := &FileService{}
	_, err := s.AddFile(workagentModel.ThreadFileRequest{
		UID:      42,
		ThreadID: thread.Id,
		FileName: "x.txt",
		FilePath: "agent_workspace/uid/42/thread/uploads/x.txt",
		FileSize: 1,
		FileType: "text",
	})
	if err == nil {
		t.Fatal("expected AddFile to reject a thread owned by another uid")
	}
	if !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("error = %v, want generic thread not found", err)
	}

	var count int64
	if err := db.Model(&workagentModel.ThreadFile{}).Count(&count).Error; err != nil {
		t.Fatalf("count files: %v", err)
	}
	if count != 0 {
		t.Fatalf("cross-tenant AddFile inserted %d rows", count)
	}
}

func TestUploadContentAllowed_RejectsGenericOctetStreamForLegacyOffice(t *testing.T) {
	randomBytes := []byte("not an ole compound document")
	if uploadContentAllowed("invoice.doc", "word", "application/octet-stream", randomBytes) {
		t.Fatal("generic octet-stream .doc should be rejected without OLE magic")
	}
	if uploadContentAllowed("sheet.xls", "excel", "application/octet-stream", randomBytes) {
		t.Fatal("generic octet-stream .xls should be rejected without OLE magic")
	}
	oleMagic := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1, 0x00}
	if !uploadContentAllowed("invoice.doc", "word", "application/octet-stream", oleMagic) {
		t.Fatal("legacy .doc with OLE magic should be accepted")
	}
	if !uploadContentAllowed("sheet.xls", "excel", "application/octet-stream", oleMagic) {
		t.Fatal("legacy .xls with OLE magic should be accepted")
	}
}

// writeUploadCapped happy path: source under the cap writes through
// fully and returns the byte count.
func TestWriteUploadCapped_UnderLimitWritesAll(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "good.bin")
	src := bytes.NewReader([]byte("hello world"))

	n, err := writeUploadCapped(dst, src, 1024)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 11 {
		t.Errorf("written = %d; want 11", n)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "hello world" {
		t.Errorf("contents = %q; want %q", got, "hello world")
	}
}

// Regression: a multipart client can declare header.Size=1KB and
// then stream 100GB. The size-cap check on declared header is not
// enforced by the runtime, so the actual copy must enforce it
// itself — otherwise we fill the disk.
func TestWriteUploadCapped_OverLimitErrorsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "huge.bin")
	// Reader yields 200 bytes; cap is 100. The +1-probe in
	// writeUploadCapped should detect overflow and refuse.
	src := bytes.NewReader(bytes.Repeat([]byte("x"), 200))

	_, err := writeUploadCapped(dst, src, 100)
	if err == nil {
		t.Fatal("expected error on overflow, got nil")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error should mention overflow; got %q", err.Error())
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("partial dst not cleaned up: %v", err)
	}
}

// O_NOFOLLOW: a pre-existing symlink at the destination must NOT
// be followed for write. Otherwise an attacker who pre-plants a
// symlink at the (predictable) upload filename pattern can redirect
// our write to a system path.
func TestWriteUploadCapped_RefusesSymlinkAtDest(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	dst := filepath.Join(dir, "dst-link")
	if err := os.Symlink(target, dst); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	_, err := writeUploadCapped(dst, bytes.NewReader([]byte("evil")), 1024)
	if err == nil {
		t.Error("expected error opening through symlink, got nil")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "untouched" {
		t.Errorf("symlink target was modified: contents = %q", got)
	}
}

// Re-upload-replaces UX: an existing regular file at dst MUST be
// overwritten. saveUploadedFileWithConversion uses a stable per-
// thread/per-filename customPath, so a user re-uploading a file
// to update its contents lands at the same path. O_TRUNC
// preserves that flow; O_NOFOLLOW (tested below) is what stops
// it from being abused via a symlink.
func TestWriteUploadCapped_OverwritesExistingRegularFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "occupied.bin")
	if err := os.WriteFile(dst, []byte("previous"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	n, err := writeUploadCapped(dst, bytes.NewReader([]byte("new bytes")), 1024)
	if err != nil {
		t.Fatalf("re-upload should succeed: %v", err)
	}
	if n != 9 {
		t.Errorf("written = %d; want 9", n)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new bytes" {
		t.Errorf("dst not replaced: contents = %q", got)
	}
}

// Empty body: zero bytes still counts as a successful upload.
func TestWriteUploadCapped_EmptyBody(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "empty.bin")

	n, err := writeUploadCapped(dst, strings.NewReader(""), 1024)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 0 {
		t.Errorf("written = %d; want 0", n)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("dst missing for empty body: %v", err)
	}
}

// Reader that errors mid-stream: writeUploadCapped must clean up
// the partial file rather than leaving a zero-byte dst behind.
type erroringReader struct {
	yielded int
	limit   int
}

func (r *erroringReader) Read(p []byte) (int, error) {
	if r.yielded >= r.limit {
		return 0, io.ErrUnexpectedEOF
	}
	n := len(p)
	if r.yielded+n > r.limit {
		n = r.limit - r.yielded
	}
	for i := 0; i < n; i++ {
		p[i] = 'a'
	}
	r.yielded += n
	if r.yielded >= r.limit {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}

// RegisterOutputFile must refuse an absolutePath that lives outside
// the configured workspace root. Without this, a future caller could
// poison the DB / asset ledger with rows that point at /etc/* or
// other system files.
func TestRegisterOutputFile_RefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// Pin manager singleton at the test root.
	prev := globalManager
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() { globalManager = prev })

	// A real file outside the workspace.
	evilPath := filepath.Join(outside, "evil.txt")
	if err := os.WriteFile(evilPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed evil: %v", err)
	}

	s := &FileService{}
	_, err := s.RegisterOutputFile(1, 1, evilPath, "")
	if err == nil {
		t.Error("RegisterOutputFile should refuse a path outside workspaceRoot")
	}
}

// SyncOutputFiles must refuse a threadWorkspace outside the
// configured workspace root. Without this guard a future caller
// bug (or admin endpoint accepting arbitrary paths) would walk
// arbitrary directories and pollute the DB with file rows
// pointing at system paths.
func TestSyncOutputFiles_RefusesThreadWorkspaceOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// Pin the manager singleton at our test root.
	prev := globalManager
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() { globalManager = prev })

	s := &FileService{}
	_, err := s.SyncOutputFiles(1, 1, outside, "uuid-irrelevant")
	if err == nil {
		t.Error("SyncOutputFiles should refuse a threadWorkspace outside root")
	}
}

func TestSyncOutputFiles_AttachesVariationDraftManifestMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	root := t.TempDir()
	threadWorkspace := filepath.Join(root, "uid-42", "thread-abc")
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(filepath.Join(outputs, ".workagent"), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "bold-poster.html"), []byte("<html>bold</html>"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	manifest := `{"variations":[{"id":"bold","label":"Bold","stance":"bold","file_path":"bold-poster.html","design_system_basename":"expressive-grid","asset_contract":"brand locked"}]}`
	if err := os.WriteFile(filepath.Join(outputs, ".workagent", "pass_1_variations.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := db.Create(&workagentModel.ChatThread{
		UID:  42,
		UUID: "thread-abc",
		Name: "variation thread",
	}).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	prevManager := globalManager
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() { globalManager = prevManager })

	result, err := (&FileService{}).SyncOutputFiles(42, 1, threadWorkspace, "thread-abc")
	if err != nil {
		t.Fatalf("SyncOutputFiles: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("added = %d, want 1 (manifest should stay hidden)", result.Added)
	}

	var file workagentModel.ThreadFile
	if err := db.Where("file_name = ?", "bold-poster.html").First(&file).Error; err != nil {
		t.Fatalf("load synced file: %v", err)
	}
	if !strings.Contains(file.Description, `"kind":"workagent_variation_draft"`) ||
		!strings.Contains(file.Description, `"variation_id":"bold"`) ||
		!strings.Contains(file.Description, `"design_system_basename":"expressive-grid"`) {
		t.Fatalf("description missing variation metadata: %s", file.Description)
	}

	var count int64
	if err := db.Model(&workagentModel.ThreadFile{}).Where("file_name = ?", "pass_1_variations.json").Count(&count).Error; err != nil {
		t.Fatalf("count manifest rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("manifest registered as thread file")
	}

	views, err := ListArtifactViewsByThread(NewFileRepository(db), 42, 1)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if len(views) != 1 || !strings.Contains(views[0].Description, `"variation_id":"bold"`) {
		t.Fatalf("artifact view missing variation description: %#v", views)
	}
}

func TestSyncOutputFiles_ReportsInvalidVariationDraftManifestReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	root := t.TempDir()
	threadWorkspace := filepath.Join(root, "uid-42", "thread-abc")
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(filepath.Join(outputs, ".workagent"), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "balanced.html"), []byte("<html>balanced</html>"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	manifest := `{"variations":[
		{"id":"balanced","label":"Balanced","file_path":"balanced.html"},
		{"id":"balanced","label":"Duplicate","file_path":"missing.html"}
	]}`
	if err := os.WriteFile(filepath.Join(outputs, ".workagent", "pass_1_variations.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := db.Create(&workagentModel.ChatThread{
		UID:  42,
		UUID: "thread-abc",
		Name: "variation thread",
	}).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	prevManager := globalManager
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() { globalManager = prevManager })

	result, err := (&FileService{}).SyncOutputFiles(42, 1, threadWorkspace, "thread-abc")
	if err != nil {
		t.Fatalf("SyncOutputFiles: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("added = %d, want only the existing draft file", result.Added)
	}
	if !hasSyncError(result.Errors, "missing file \"missing.html\"") {
		t.Fatalf("expected missing variation draft file error, got %#v", result.Errors)
	}
	if !hasSyncError(result.Errors, "repeats variation id \"balanced\"") {
		t.Fatalf("expected duplicate variation id error, got %#v", result.Errors)
	}
}

func TestSyncOutputFiles_ReportsDuplicateVariationDraftManifestFilePaths(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	root := t.TempDir()
	threadWorkspace := filepath.Join(root, "uid-42", "thread-abc")
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(filepath.Join(outputs, ".workagent"), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "shared.html"), []byte("<html>shared</html>"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	manifest := `{"variations":[
		{"id":"balanced","label":"Balanced","file_path":"shared.html"},
		{"id":"bold","label":"Bold","file_path":"shared.html"}
	]}`
	if err := os.WriteFile(filepath.Join(outputs, ".workagent", "pass_1_variations.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := db.Create(&workagentModel.ChatThread{
		UID:  42,
		UUID: "thread-abc",
		Name: "variation thread",
	}).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	prevManager := globalManager
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() { globalManager = prevManager })

	result, err := (&FileService{}).SyncOutputFiles(42, 1, threadWorkspace, "thread-abc")
	if err != nil {
		t.Fatalf("SyncOutputFiles: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("added = %d, want shared draft file still synced once", result.Added)
	}
	if !hasSyncError(result.Errors, `repeats file_path "shared.html"`) {
		t.Fatalf("expected duplicate file_path manifest error, got %#v", result.Errors)
	}
	var file workagentModel.ThreadFile
	if err := db.Where("file_name = ?", "shared.html").First(&file).Error; err != nil {
		t.Fatalf("load synced file: %v", err)
	}
	if !strings.Contains(file.Description, `"variation_id":"balanced"`) || strings.Contains(file.Description, `"variation_id":"bold"`) {
		t.Fatalf("description should keep first variation binding, got %s", file.Description)
	}
}

func TestSyncOutputFiles_ReportsMalformedVariationDraftManifest(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	root := t.TempDir()
	threadWorkspace := filepath.Join(root, "uid-42", "thread-abc")
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(filepath.Join(outputs, ".workagent"), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "balanced.html"), []byte("<html>balanced</html>"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputs, ".workagent", "pass_1_variations.json"), []byte(`{"variations":[`), 0o600); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	if err := db.Create(&workagentModel.ChatThread{
		UID:  42,
		UUID: "thread-abc",
		Name: "variation thread",
	}).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	prevManager := globalManager
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() { globalManager = prevManager })

	result, err := (&FileService{}).SyncOutputFiles(42, 1, threadWorkspace, "thread-abc")
	if err != nil {
		t.Fatalf("SyncOutputFiles: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("added = %d, want draft file still synced", result.Added)
	}
	if !hasSyncError(result.Errors, "invalid pass_1_variations manifest") {
		t.Fatalf("expected malformed manifest error, got %#v", result.Errors)
	}
}

func TestSyncOutputFiles_ReportsMissingVariationDraftManifestDuringDraftPass(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	root := t.TempDir()
	threadWorkspace := filepath.Join(root, "uid-42", "thread-abc")
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(outputs, 0o755); err != nil {
		t.Fatalf("mkdir outputs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "balanced.html"), []byte("<html>balanced</html>"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	if err := db.Create(&workagentModel.ChatThread{
		UID:  42,
		UUID: "thread-abc",
		Name: "variation thread",
	}).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	if !PersistPassModeState(42, 1, WorkAgentPassModeState{
		Mode:   WorkAgentPassModeDraft,
		Source: "test",
	}) {
		t.Fatalf("persist draft pass mode")
	}

	prevManager := globalManager
	managerOnce.Do(func() {})
	globalManager = &AgentClientManager{
		workspaceRoot:  root,
		workspaceCache: sync.Map{},
	}
	t.Cleanup(func() { globalManager = prevManager })

	result, err := (&FileService{}).SyncOutputFiles(42, 1, threadWorkspace, "thread-abc")
	if err != nil {
		t.Fatalf("SyncOutputFiles: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("added = %d, want draft file still synced", result.Added)
	}
	if !hasSyncError(result.Errors, "draft pass missing outputs/.workagent/pass_1_variations.json manifest") {
		t.Fatalf("expected missing draft manifest error, got %#v", result.Errors)
	}
}

func TestVariationDraftManifestIssuesForOutputScan_ReportsDraftManifestProblems(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	thread := workagentModel.ChatThread{
		UID:  42,
		UUID: "thread-abc",
		Name: "variation thread",
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	if !PersistPassModeState(42, thread.Id, WorkAgentPassModeState{
		Mode:   WorkAgentPassModeDraft,
		Source: "test",
	}) {
		t.Fatalf("persist draft pass mode")
	}

	threadWorkspace := t.TempDir()
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(outputs, 0o755); err != nil {
		t.Fatalf("mkdir outputs: %v", err)
	}
	draftPath := filepath.Join(outputs, "balanced.html")
	if err := os.WriteFile(draftPath, []byte("<html>balanced</html>"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	issues := VariationDraftManifestIssuesForOutputScan(threadWorkspace, thread.Id, []string{draftPath})
	if !hasSyncError(issues, "draft pass missing outputs/.workagent/pass_1_variations.json manifest") {
		t.Fatalf("expected missing draft manifest issue, got %#v", issues)
	}

	manifestDir := filepath.Join(outputs, ".workagent")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "pass_1_variations.json"), []byte(`{"variations":[{"id":"balanced","file_path":"missing.html"}]}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	issues = VariationDraftManifestIssuesForOutputScan(threadWorkspace, thread.Id, []string{draftPath})
	if !hasSyncError(issues, `variation draft manifest references missing file "missing.html"`) {
		t.Fatalf("expected missing manifest file issue, got %#v", issues)
	}
}

func hasSyncError(errors []string, needle string) bool {
	for _, err := range errors {
		if strings.Contains(err, needle) {
			return true
		}
	}
	return false
}

func TestWriteUploadCapped_MidStreamErrorCleansUp(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "broken.bin")

	_, err := writeUploadCapped(dst, &erroringReader{limit: 50}, 1024)
	if err == nil {
		t.Error("expected error from broken reader, got nil")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("partial dst not cleaned up: %v", statErr)
	}
}
