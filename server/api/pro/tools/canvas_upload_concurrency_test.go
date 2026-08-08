package tools

import (
	"sync"
	"testing"
)

// canvas_upload_concurrency_test.go pins the per-uid upload semaphore
// added to UploadAsset. Without it, a scripted client could drive N
// parallel reads each holding ~maxSize bytes resident — see the
// canvas_api.go comment for the threat model.

func TestAcquireCanvasUploadSlot_AllowsCapConcurrent(t *testing.T) {
	// Reset the global state for this test by spawning a unique uid
	// (tests share the package var, so use a marker that's unlikely to
	// collide with other suites: pick uids in the 7e6+ range).
	const uid = 7000001

	releases := make([]func(), 0, canvasUploadConcurrencyPerUID)
	for i := 0; i < canvasUploadConcurrencyPerUID; i++ {
		release, ok := acquireCanvasUploadSlot(uid)
		if !ok {
			t.Fatalf("slot %d should be available, cap=%d", i, canvasUploadConcurrencyPerUID)
		}
		releases = append(releases, release)
	}

	// (cap+1)-th must be rejected.
	if _, ok := acquireCanvasUploadSlot(uid); ok {
		t.Fatalf("slot %d should be rejected (cap exceeded)", canvasUploadConcurrencyPerUID+1)
	}

	// Releasing one frees a slot for the next attempt.
	releases[0]()
	if _, ok := acquireCanvasUploadSlot(uid); !ok {
		t.Fatalf("slot should be available after release")
	}
}

func TestAcquireCanvasUploadSlot_PerUIDIsolation(t *testing.T) {
	// Two distinct uids each get their own bucket — uid A saturating
	// its cap must NOT block uid B from uploading. Stops a noisy-
	// neighbor user from starving the rest of the tenant pool.
	const uidA = 7000002
	const uidB = 7000003

	for i := 0; i < canvasUploadConcurrencyPerUID; i++ {
		if _, ok := acquireCanvasUploadSlot(uidA); !ok {
			t.Fatalf("uidA slot %d should be available", i)
		}
	}
	if _, ok := acquireCanvasUploadSlot(uidA); ok {
		t.Fatalf("uidA cap+1 should be rejected")
	}
	// uidB unaffected.
	for i := 0; i < canvasUploadConcurrencyPerUID; i++ {
		if _, ok := acquireCanvasUploadSlot(uidB); !ok {
			t.Fatalf("uidB slot %d should be available even with uidA saturated", i)
		}
	}
}

func TestAcquireCanvasUploadSlot_ConcurrentAcquireRelease(t *testing.T) {
	// Stress test: many goroutines acquire/release in a tight loop.
	// The semaphore must never let more than the cap hold concurrently.
	// Surfaces races in the LoadOrStore + chan-send path under -race.
	const uid = 7000004
	const goroutines = 100
	const iterations = 200

	var (
		live    int
		liveMax int
		mu      sync.Mutex
		wg      sync.WaitGroup
	)
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				release, ok := acquireCanvasUploadSlot(uid)
				if !ok {
					continue
				}
				mu.Lock()
				live++
				if live > liveMax {
					liveMax = live
				}
				mu.Unlock()
				// Simulate a tiny bit of work with no sleep — the more
				// the goroutines fight over the slot, the better the
				// race detector exercises the semaphore.
				mu.Lock()
				live--
				mu.Unlock()
				release()
			}
		}()
	}
	wg.Wait()

	if liveMax > canvasUploadConcurrencyPerUID {
		t.Errorf("max concurrent holders = %d, cap = %d (semaphore violated under contention)", liveMax, canvasUploadConcurrencyPerUID)
	}
}
