//go:build desktop

package desktop

import (
	"context"
	"sync"
	"testing"
)

// fakeFileIndexer records IndexFile calls for assertion. Implements
// desktop.FileIndexer (same shape as *knowledge.Indexer).
type fakeFileIndexer struct {
	mu    sync.Mutex
	calls []fakeIndexCall
}

type fakeIndexCall struct {
	uid    uint64
	fileID int64
}

func (f *fakeFileIndexer) IndexFile(_ context.Context, uid uint64, fileID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeIndexCall{uid: uid, fileID: fileID})
	return nil
}

func (f *fakeFileIndexer) RemoveFile(_ context.Context, fileID int64) (int, error) { return 0, nil }

func (f *fakeFileIndexer) snapshot() []fakeIndexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeIndexCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestIndexUploadedFile verifies the L3c-3 hook forwards the saved file's uid
// + id to the indexer. The handler guards the call with a nil check; this
// exercises the non-nil path directly (indexUploadedFile runs synchronously
// when invoked without `go`).
func TestIndexUploadedFile(t *testing.T) {
	fi := &fakeFileIndexer{}
	s := &Server{cfg: ServerConfig{FileIndexer: fi}}

	s.indexUploadedFile(42, 99)

	calls := fi.snapshot()
	if len(calls) != 1 || calls[0].uid != 42 || calls[0].fileID != 99 {
		t.Fatalf("expected one IndexFile(42,99), got %+v", calls)
	}

	// Nil FileIndexer on the config is the disabled-RAG state; the handler's
	// `if FileIndexer != nil` guard (one line above the `go`) keeps this from
	// being dereferenced. Confirm the guard condition holds.
	if (&Server{cfg: ServerConfig{}}).cfg.FileIndexer != nil {
		t.Fatal("zero-value ServerConfig should have a nil FileIndexer")
	}
}
