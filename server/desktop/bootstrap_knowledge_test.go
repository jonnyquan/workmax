//go:build desktop

package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"server/desktop/knowledge/assets"
)

// fakeAssets drives knowledgeAssets without a network or a cgo resolver: it is
// the whole acquisition policy under test, and none of it needs either.
type fakeAssets struct {
	mu sync.Mutex

	presentErr error
	plan       assets.Plan
	planErr    error
	fetchErr   error

	planCalls  int
	fetchCalls int
	fetched    chan struct{}
	release    chan struct{}
	now        time.Time
}

func newFakeAssets(t *testing.T, dir string) (*knowledgeAssets, *fakeAssets) {
	t.Helper()
	f := &fakeAssets{
		presentErr: errors.New("assets not on disk"),
		plan: assets.Plan{
			Origin:     "embedded",
			Platform:   "darwin/arm64",
			Assets:     []assets.Asset{{Name: "model", Path: "knowledge/model.onnx", Size: 100}},
			TotalBytes: 100,
		},
		fetched: make(chan struct{}, 4),
		release: make(chan struct{}),
		now:     time.Unix(1_700_000_000, 0),
	}
	k := &knowledgeAssets{
		dir: dir,
		present: func() error {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.presentErr
		},
		plan: func(string) (assets.Plan, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.planCalls++
			return f.plan, f.planErr
		},
		fetch: func(context.Context, string, assets.Plan) error {
			f.mu.Lock()
			f.fetchCalls++
			err := f.fetchErr
			f.mu.Unlock()
			f.fetched <- struct{}{}
			<-f.release
			return err
		},
		now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.now
		},
	}
	return k, f
}

func (f *fakeAssets) set(fn func(*fakeAssets)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeAssets) counts() (plans, fetches int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.planCalls, f.fetchCalls
}

// The offline path: assets already on disk must never consult a manifest, let
// alone reach for the network. A user who installed the assets once has to keep
// working on a plane.
func TestKnowledgeAssetsPresentSkipsEverything(t *testing.T) {
	k, f := newFakeAssets(t, t.TempDir())
	f.set(func(f *fakeAssets) { f.presentErr = nil })

	if err := k.ensure(); err != nil {
		t.Fatalf("ensure with assets present = %v, want nil", err)
	}
	if plans, fetches := f.counts(); plans != 0 || fetches != 0 {
		t.Errorf("present assets triggered %d manifest reads and %d downloads, want 0 and 0", plans, fetches)
	}
}

// The state the open-source tree actually ships in: nothing pinned for this
// platform. It must be a named, actionable answer — not a silent no-op, and not
// a retry loop.
func TestKnowledgeAssetsUnpinnedPlatformIsExplicit(t *testing.T) {
	dir := t.TempDir()
	k, f := newFakeAssets(t, dir)
	f.set(func(f *fakeAssets) {
		f.planErr = fmt.Errorf("%w: darwin/arm64 (manifest: embedded)", assets.ErrUnsupportedPlatform)
	})

	err := k.ensure()
	if err == nil {
		t.Fatal("ensure with nothing pinned must report why")
	}
	msg := err.Error()
	for _, want := range []string{dir, assets.ManifestPathEnv, "model.onnx"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q; a user cannot act on it", msg, want)
		}
	}
	if _, fetches := f.counts(); fetches != 0 {
		t.Errorf("an unpinned platform started %d downloads, want 0", fetches)
	}

	// Repeated calls inside the retry window reuse the answer instead of
	// re-reading the manifest on every turn.
	if err2 := k.ensure(); err2.Error() != msg {
		t.Errorf("second ensure = %v, want the remembered %v", err2, err)
	}
	if plans, _ := f.counts(); plans != 1 {
		t.Errorf("manifest read %d times inside the retry window, want 1", plans)
	}

	// Past the window it tries again — plugging in a manifest must eventually
	// take effect without a restart.
	f.set(func(f *fakeAssets) { f.now = f.now.Add(knowledgeAssetRetry + time.Second) })
	_ = k.ensure()
	if plans, _ := f.counts(); plans != 2 {
		t.Errorf("manifest read %d times after the retry window, want 2", plans)
	}
}

// A first run downloads in the background: the caller is told "not yet", not
// made to wait, and only one download runs however many turns ask.
func TestKnowledgeAssetsDownloadsOnceInBackground(t *testing.T) {
	k, f := newFakeAssets(t, t.TempDir())

	if err := k.ensure(); !errors.Is(err, errKnowledgeAssetsFetching) {
		t.Fatalf("first ensure = %v, want the downloading sentinel", err)
	}
	select {
	case <-f.fetched:
	case <-time.After(2 * time.Second):
		t.Fatal("no download started")
	}

	// While it runs, more callers arrive and must not pile on.
	for range 3 {
		if err := k.ensure(); !errors.Is(err, errKnowledgeAssetsFetching) {
			t.Fatalf("ensure during download = %v, want the downloading sentinel", err)
		}
	}
	if _, fetches := f.counts(); fetches != 1 {
		t.Fatalf("%d downloads running, want 1", fetches)
	}

	// It finishes and the assets appear.
	f.set(func(f *fakeAssets) { f.presentErr = nil })
	close(f.release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := k.ensure(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ensure never reported the assets as ready after the download finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A failed download is remembered for a while and then retried. Forgetting it
// immediately would mean one download attempt per turn on a broken network.
func TestKnowledgeAssetsBacksOffAfterFailure(t *testing.T) {
	k, f := newFakeAssets(t, t.TempDir())
	f.set(func(f *fakeAssets) { f.fetchErr = errors.New("connection reset") })
	close(f.release)

	if err := k.ensure(); !errors.Is(err, errKnowledgeAssetsFetching) {
		t.Fatalf("first ensure = %v, want the downloading sentinel", err)
	}
	<-f.fetched
	// Wait for the failure to be recorded.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := k.ensure()
		if err != nil && strings.Contains(err.Error(), "connection reset") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download failure was never surfaced, last = %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, fetches := f.counts(); fetches != 1 {
		t.Errorf("%d download attempts inside the backoff window, want 1", fetches)
	}

	f.set(func(f *fakeAssets) { f.now = f.now.Add(knowledgeAssetRetry + time.Second) })
	if err := k.ensure(); !errors.Is(err, errKnowledgeAssetsFetching) {
		t.Fatalf("ensure after the backoff = %v, want a fresh attempt", err)
	}
	<-f.fetched
}

// Downloads must land under the per-user data directory. The packaged
// resources directory is inside a signed app bundle and is not writable.
func TestKnowledgeDownloadDirIsUnderTheDataDir(t *testing.T) {
	if got, want := knowledgeDownloadDir("/data"), filepath.Join("/data", knowledgeDownloadDirName); got != want {
		t.Errorf("knowledgeDownloadDir = %q, want %q", got, want)
	}
	if got := knowledgeDownloadDir(""); got != knowledgeDownloadDirName {
		t.Errorf("knowledgeDownloadDir(\"\") = %q, want a relative fallback", got)
	}
}
