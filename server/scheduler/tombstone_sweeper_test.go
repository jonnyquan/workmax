package scheduler

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"server/globals"
)

// installSystemDB swaps globals.GraDBs["system"] for the test DB
// and restores the previous binding when the test exits.
//
// Schema mirrors migrations/20260642_create_workagent_tombstone.sql
// so sweepOnce's PruneTombstones call succeeds cleanly.
func installSystemDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "tombstone.db")),
		&gorm.Config{Logger: gormlogger.Discard},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_tombstone (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL,
		entity_type TEXT NOT NULL,
		entity_id INTEGER NOT NULL,
		entity_uuid TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	prev := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = prev })
}

// stopWithin runs s.Stop() in a goroutine and fails the test if it
// doesn't return within the deadline. This is the regression pin for
// the pre-fix bug where Stop() blocked on `stopChan <- struct{}{}`
// while sweepOnce held the run() goroutine away from <-stopChan.
func stopWithin(t *testing.T, stop func(), deadline time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatal("Stop deadlocked")
	}
}

func TestTombstoneSweeper_StopWithoutStartIsNoop(t *testing.T) {
	s := NewTombstoneSweeper()
	stopWithin(t, s.Stop, time.Second) // must not block or panic
}

func TestTombstoneSweeper_StopAfterStartDoesNotDeadlock(t *testing.T) {
	installSystemDB(t)
	s := NewTombstoneSweeper()
	s.Start()
	// Give run() a moment to enter its select loop so we exercise
	// the "channel-closed wakeup" path, not the start-race path.
	time.Sleep(20 * time.Millisecond)
	stopWithin(t, s.Stop, 2*time.Second)
}

func TestTombstoneSweeper_StopIsIdempotent(t *testing.T) {
	installSystemDB(t)
	s := NewTombstoneSweeper()
	s.Start()
	time.Sleep(20 * time.Millisecond)
	stopWithin(t, s.Stop, 2*time.Second)
	// Second Stop must not panic on close-of-closed-channel.
	stopWithin(t, s.Stop, time.Second)
	// Third Stop also fine.
	stopWithin(t, s.Stop, time.Second)
}

// TestTombstoneSweeper_StartIsIdempotent pins that a second Start()
// call doesn't spawn a duplicate goroutine. We can't observe the
// goroutine count directly without flakiness, but we can prove the
// invariant by calling Start twice and verifying a single Stop
// suffices to cleanly tear down.
func TestTombstoneSweeper_StartIsIdempotent(t *testing.T) {
	installSystemDB(t)
	s := NewTombstoneSweeper()
	s.Start()
	s.Start() // should be a no-op
	s.Start() // should be a no-op
	time.Sleep(20 * time.Millisecond)
	stopWithin(t, s.Stop, 2*time.Second)
}

// TestTombstoneSweeper_ConcurrentStopRace exercises the atomic.Bool
// guard around isRunning under -race. With the pre-fix `bool` field,
// the race detector flagged read/write conflicts between Start/Stop
// callers and the panic-recover branch.
func TestTombstoneSweeper_ConcurrentStopRace(t *testing.T) {
	installSystemDB(t)
	s := NewTombstoneSweeper()
	s.Start()
	time.Sleep(20 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Stop()
		}()
	}
	doneAll := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneAll)
	}()
	select {
	case <-doneAll:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Stop deadlocked")
	}
}
