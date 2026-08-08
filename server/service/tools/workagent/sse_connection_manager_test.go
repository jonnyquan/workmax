package workagent

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// fakeSSEConnection skips the gin-bound monitor goroutine NewSSEConnection
// would otherwise spawn. We only need an SSEConnection with a real UserID
// for the registration cap tests; the monitor's purpose (watching for
// upstream client disconnect) is irrelevant to in-memory cap behavior.
func fakeSSEConnection(uid uint, threadID string) *SSEConnection {
	ctx, cancel := context.WithCancel(context.Background())
	return &SSEConnection{
		ID:             threadID, // unique per test connection
		UserID:         uid,
		ThreadID:       threadID,
		Context:        ctx,
		Cancel:         cancel,
		StartTime:      time.Now(),
		LastActivity:   time.Now(),
		Connected:      true,
		disconnectChan: make(chan struct{}),
	}
}

func newTestManager(t *testing.T, max int) *SSEConnectionManager {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	m := &SSEConnectionManager{
		connections:    make(map[string]*SSEConnection),
		userCounts:     make(map[uint]int),
		maxConnections: max,
		stopCtx:        ctx,
		stopCancel:     cancel,
	}
	t.Cleanup(func() {
		m.Shutdown()
	})
	return m
}

func TestRegister_PerUserLimit_AllowsUpToMax(t *testing.T) {
	m := newTestManager(t, 100)

	for i := 0; i < MaxConcurrentSSEConnectionsPerUser; i++ {
		conn := fakeSSEConnection(42, "thread-allowed-"+string(rune('a'+i)))
		if err := m.Register(conn); err != nil {
			t.Fatalf("Register #%d unexpected error: %v", i+1, err)
		}
	}
}

func TestRegister_PerUserLimit_BlocksAtMax(t *testing.T) {
	m := newTestManager(t, 100)

	// Fill the per-user cap.
	for i := 0; i < MaxConcurrentSSEConnectionsPerUser; i++ {
		conn := fakeSSEConnection(42, "thread-fill-"+string(rune('a'+i)))
		if err := m.Register(conn); err != nil {
			t.Fatalf("setup: Register #%d failed: %v", i+1, err)
		}
	}

	// One more must be rejected with the typed error.
	overflow := fakeSSEConnection(42, "thread-overflow")
	err := m.Register(overflow)
	if !errors.Is(err, ErrPerUserSSELimit) {
		t.Fatalf("Register over per-user cap returned %v, want ErrPerUserSSELimit", err)
	}
}

func TestRegister_PerUserLimit_DoesNotBlockOtherUsers(t *testing.T) {
	m := newTestManager(t, 100)

	for i := 0; i < MaxConcurrentSSEConnectionsPerUser; i++ {
		conn := fakeSSEConnection(42, "thread-uid42-"+string(rune('a'+i)))
		if err := m.Register(conn); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Different user must still be allowed.
	other := fakeSSEConnection(99, "thread-uid99")
	if err := m.Register(other); err != nil {
		t.Errorf("different user should not be blocked: %v", err)
	}
}

// TestUserCounts_DroppedToZeroFreesMapKey verifies the bounded-by-active
// -users invariant called out in the userCounts field doc: when the last
// connection for a uid unregisters, the map entry must be deleted (not
// left at zero), so that lifetime-unique uids over a long-running
// process don't slowly grow the map.
func TestUserCounts_DroppedToZeroFreesMapKey(t *testing.T) {
	m := newTestManager(t, 100)

	conn := fakeSSEConnection(7, "thread-counts")
	if err := m.Register(conn); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m.mu.RLock()
	if got := m.userCounts[7]; got != 1 {
		m.mu.RUnlock()
		t.Fatalf("after Register: userCounts[7]=%d, want 1", got)
	}
	m.mu.RUnlock()

	m.Unregister(conn.ID)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, present := m.userCounts[7]; present {
		t.Errorf("after Unregister: userCounts[7] still present (count=%d), want key deleted",
			m.userCounts[7])
	}
}

func TestRegister_PerUserLimit_FreesSlotOnUnregister(t *testing.T) {
	m := newTestManager(t, 100)

	conns := make([]*SSEConnection, 0, MaxConcurrentSSEConnectionsPerUser)
	for i := 0; i < MaxConcurrentSSEConnectionsPerUser; i++ {
		conn := fakeSSEConnection(42, "thread-cycle-"+string(rune('a'+i)))
		if err := m.Register(conn); err != nil {
			t.Fatalf("setup: %v", err)
		}
		conns = append(conns, conn)
	}

	// Unregister one — that should free a slot.
	m.Unregister(conns[0].ID)

	replacement := fakeSSEConnection(42, "thread-cycle-replacement")
	if err := m.Register(replacement); err != nil {
		t.Errorf("Register after unregister should succeed: %v", err)
	}
}

func TestRegister_GlobalLimitStillEnforced(t *testing.T) {
	// Global cap of 2 with MaxConcurrentSSEConnectionsPerUser higher
	// proves the global check fires when no per-user violation has
	// occurred. Use distinct uids so per-user never trips.
	m := newTestManager(t, 2)

	for i, uid := range []uint{1, 2} {
		conn := fakeSSEConnection(uid, "thread-global-"+string(rune('a'+i)))
		if err := m.Register(conn); err != nil {
			t.Fatalf("setup #%d: %v", i, err)
		}
	}

	overflow := fakeSSEConnection(3, "thread-global-overflow")
	err := m.Register(overflow)
	if err == nil {
		t.Fatal("expected error at global cap")
	}
	if errors.Is(err, ErrPerUserSSELimit) {
		t.Errorf("global cap hit returned ErrPerUserSSELimit, want generic max-connections err")
	}
}

func TestRegister_ZeroUidSkipsPerUserCheck(t *testing.T) {
	// uid==0 means "unauthenticated / unknown user". The per-user cap
	// is keyed by uid, so registering many uid=0 connections must not
	// be blocked by the per-user check (the global cap still applies).
	m := newTestManager(t, 100)

	for i := 0; i < MaxConcurrentSSEConnectionsPerUser+3; i++ {
		conn := fakeSSEConnection(0, "thread-anon-"+string(rune('a'+i)))
		if err := m.Register(conn); err != nil {
			t.Errorf("uid=0 Register #%d unexpected error: %v", i+1, err)
		}
	}
}

func TestRegister_ConcurrentSafe(t *testing.T) {
	// Ten goroutines try to register for the same user. Exactly
	// MaxConcurrentSSEConnectionsPerUser must succeed; the rest get
	// ErrPerUserSSELimit. Catches races in the per-user counter logic.
	m := newTestManager(t, 100)

	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			conn := fakeSSEConnection(42, "thread-race-"+string(rune('a'+idx)))
			results <- m.Register(conn)
		}()
	}
	wg.Wait()
	close(results)

	allowed := 0
	rejected := 0
	for err := range results {
		if err == nil {
			allowed++
		} else if errors.Is(err, ErrPerUserSSELimit) {
			rejected++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if allowed != MaxConcurrentSSEConnectionsPerUser {
		t.Errorf("allowed = %d, want %d", allowed, MaxConcurrentSSEConnectionsPerUser)
	}
	if rejected != 10-MaxConcurrentSSEConnectionsPerUser {
		t.Errorf("rejected = %d, want %d", rejected, 10-MaxConcurrentSSEConnectionsPerUser)
	}
}

// blockingWriter mimics a slow client: Write blocks until release is
// closed, simulating a stuck TCP send buffer. Implements just enough
// of gin.ResponseWriter for SSEConnection.WriteChunk + Flusher path
// to compile against the real interface.
type blockingWriter struct {
	gin.ResponseWriter
	release   chan struct{}
	written   chan struct{}
	flushHit  chan struct{}
	writeOnce sync.Once
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	// Signal first-write exactly once. The writer goroutine drains
	// the entire queue post-release, calling Write per chunk —
	// closing `written` more than once would panic. sync.Once keeps
	// the "writer reached Write at least once" hook robust against
	// the multi-chunk path.
	b.writeOnce.Do(func() { close(b.written) })
	<-b.release
	return len(p), nil
}

func (b *blockingWriter) Flush() {
	select {
	case <-b.flushHit:
	default:
		close(b.flushHit)
	}
}

// fakeConnWithWriter constructs a fully-initialised SSEConnection
// with a custom gin.ResponseWriter and a running writer goroutine.
// Cleans up the writer at test teardown so a stuck Write doesn't
// leak the goroutine across tests.
func fakeConnWithWriter(t *testing.T, w gin.ResponseWriter) *SSEConnection {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn := &SSEConnection{
		ID:             "slow-client",
		UserID:         1,
		ThreadID:       "thread-slow",
		Writer:         w,
		Context:        ctx,
		Cancel:         cancel,
		StartTime:      time.Now(),
		LastActivity:   time.Now(),
		Connected:      true,
		disconnectChan: make(chan struct{}),
	}
	conn.initWriter()
	t.Cleanup(func() {
		cancel()
	})
	return conn
}

// WriteChunk under a slow client must NOT block IsConnected — the
// SSE plane keeps cleanup-loop / GetStats / IsConnected responsive
// while the writer goroutine is blocked on Writer.Write. The
// post-C2 design strengthens this further: WriteChunk itself
// returns immediately on a successful enqueue, so the SDK
// callback path is no longer coupled to network speed at all.
func TestWriteChunk_DoesNotBlockStateReaders(t *testing.T) {
	bw := &blockingWriter{
		release:  make(chan struct{}),
		written:  make(chan struct{}),
		flushHit: make(chan struct{}),
	}
	conn := fakeConnWithWriter(t, bw)

	// WriteChunk must return promptly — the new async design returns
	// nil right after enqueue regardless of consumer speed. Pin a
	// tight bound; any blocking here means we regressed to the
	// pre-C2 sync path.
	wcDone := make(chan error, 1)
	go func() { wcDone <- conn.WriteChunk([]byte("data: hello\n\n")) }()
	select {
	case err := <-wcDone:
		if err != nil {
			t.Errorf("WriteChunk returned error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WriteChunk blocked — async-enqueue contract regressed")
	}

	// Wait until the writer goroutine is actually inside Writer.Write
	// so we know it's parked on the slow client.
	select {
	case <-bw.written:
	case <-time.After(time.Second):
		t.Fatal("writer goroutine never reached Writer.Write")
	}

	// State readers must complete promptly even though Write is
	// blocked. Same property as before — verifies the locking split
	// (writeMu held only by writer goroutine) didn't regress.
	stateReadDone := make(chan struct{})
	go func() {
		_ = conn.IsConnected()
		stateReadDone <- struct{}{}
	}()
	select {
	case <-stateReadDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("IsConnected blocked on slow Write — locking discipline regressed")
	}

	// Release the slow Write so the writer goroutine can drain.
	close(bw.release)

	// Wait for counters to reflect the completed write. The writer
	// goroutine updates them after Writer.Write returns; we don't
	// have a direct hook so poll briefly.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn.mu.RLock()
		chunks := conn.ChunkCount
		conn.mu.RUnlock()
		if chunks == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	conn.mu.RLock()
	bytes := conn.BytesSent
	chunks := conn.ChunkCount
	conn.mu.RUnlock()
	if chunks != 1 {
		t.Errorf("ChunkCount = %d; want 1", chunks)
	}
	if bytes == 0 {
		t.Errorf("BytesSent = 0; want > 0")
	}
}

// TestWriteChunk_DropsOnQueueOverflow verifies the bounded-queue
// drop policy: when the writer goroutine is parked on a slow Write,
// every additional chunk past writeQueueCapacity is dropped (with
// ChunksDropped bumped) instead of blocking the producer.
func TestWriteChunk_DropsOnQueueOverflow(t *testing.T) {
	bw := &blockingWriter{
		release:  make(chan struct{}),
		written:  make(chan struct{}),
		flushHit: make(chan struct{}),
	}
	conn := fakeConnWithWriter(t, bw)

	// First chunk: writer pulls it and parks in Writer.Write.
	if err := conn.WriteChunk([]byte("first")); err != nil {
		t.Fatalf("first WriteChunk: %v", err)
	}
	select {
	case <-bw.written:
	case <-time.After(time.Second):
		t.Fatal("writer never reached Writer.Write")
	}

	// Fill the queue. After this loop, capacity chunks are buffered;
	// the writer is still parked on the first chunk, so they can't
	// drain. None of these enqueues should block.
	overflowStart := writeQueueCapacity + 10
	deadline := time.Now().Add(time.Second)
	for i := 0; i < overflowStart; i++ {
		done := make(chan error, 1)
		go func(idx int) {
			done <- conn.WriteChunk([]byte("chunk"))
		}(i)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("WriteChunk #%d errored: %v", i, err)
			}
		case <-time.After(time.Until(deadline)):
			t.Fatalf("WriteChunk #%d blocked — bounded queue regressed", i)
		}
	}

	// At least 10 chunks should have been dropped (overflowStart -
	// capacity). We don't pin an exact number because the writer
	// MAY have drained the first one's queue slot back to runWriter
	// in between enqueues.
	dropped := atomic.LoadInt64(&conn.ChunksDropped)
	if dropped < 1 {
		t.Errorf("expected drops, got ChunksDropped=%d", dropped)
	}

	// Release so the writer can finish + clean up via t.Cleanup.
	close(bw.release)
}

// httptest import kept intentionally to make adding a future end-to-end
// test (real gin.Context + Register) easier without re-adding deps.
var _ = httptest.NewRecorder
