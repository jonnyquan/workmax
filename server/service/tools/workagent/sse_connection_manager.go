package workagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"server/globals"
)

// sseConnSeq monotonically increments to disambiguate connection IDs
// minted in the same nanosecond. UnixNano alone is NOT a guarantee on
// every platform (some clocks resolve to microseconds, and a tight
// retry loop can return the same value twice). Combined with an atomic
// counter the ID is collision-free for the process lifetime.
var sseConnSeq atomic.Uint64

// SSEConnection represents an active SSE connection.
//
// Locking discipline (matters under TCP backpressure):
//   - mu      : guards the state fields (Connected, counters,
//               LastActivity). Held only for short pure-memory
//               operations.
//   - writeMu : serialises I/O against Writer (Writer.Write +
//               Flusher.Flush are not concurrent-safe). Held only
//               while issuing the syscall by the writer goroutine,
//               NOT by WriteChunk.
//
// Concurrency model (matters under slow clients):
//
//   - WriteChunk: producers (SDK callbacks, billing flushes, etc.)
//     copy their data, enqueue into writeQueue (bounded), and
//     return immediately. If the queue is full, the chunk is
//     dropped (chunksDropped counter bumped) so the producer
//     never blocks on the network — this matters because the SDK
//     callback path is on the upstream-billed token stream.
//
//   - runWriter: a single dedicated goroutine drains writeQueue,
//     issues Writer.Write + Flusher.Flush under a per-write
//     deadline, and on any error flips Connected=false so the
//     next WriteChunk fails fast. Exits on Context cancellation
//     or write failure.
//
// Splitting locks AND decoupling producer from consumer means a
// slow client (kernel send-buffer full, network jitter, paused
// frontend tab) can't block IsConnected, Close, GetStats, the
// cleanup goroutine, OR the SDK iterator emitting events. The
// pre-decoupled design held writeMu through Writer.Write and
// blocked the SDK callback for the duration of the slow write,
// which billed upstream tokens we couldn't deliver.
type SSEConnection struct {
	ID             string
	UserID         uint
	ThreadID       string
	Writer         gin.ResponseWriter
	Context        context.Context
	Cancel         context.CancelFunc
	StartTime      time.Time
	LastActivity   time.Time
	BytesSent      int64
	ChunkCount     int64
	ChunksDropped  int64 // updated atomically; read for stats
	Connected      bool
	disconnectChan chan struct{}
	writeQueue     chan []byte   // nil until initWriter runs
	writerDone     chan struct{} // closed when runWriter exits
	mu             sync.RWMutex
	writeMu        sync.Mutex
}

// writeQueueCapacity bounds the per-connection write backlog. 256
// chunks at typical agent-event sizes (<2 KB serialised) is ~512 KB
// peak per slow client — small enough that 500 simultaneously-stuck
// clients fit in 256 MB of resident memory, large enough to absorb
// momentary network jitter without dropping.
const writeQueueCapacity = 256

// writeDeadlineDuration caps how long a single Writer.Write can
// block before we declare the client dead and tear the conn down.
// 10s is generous for a healthy client (typical RTT + a single
// retransmit window) but well under agentTimeout — so a hung TCP
// send can't keep an agent run billing past the user-visible
// timeout.
const writeDeadlineDuration = 10 * time.Second

// heartbeatInterval is how often the writer goroutine emits an SSE
// comment line during a quiet stretch. Two purposes:
//
//   1. cleanupStaleConnections reaps connections idle >10 min. A
//      legit agent turn doing deep tool work (long Bash, embedding
//      compute, paginated upstream) can fall silent for that long
//      and get killed mid-flight. The heartbeat keeps LastActivity
//      fresh so legit quiet tasks survive.
//   2. Intermediate proxies (CDN, ingress, corporate firewalls) often
//      idle-close TCP connections that go silent for 60-120s. The
//      comment line keeps the path visibly alive end-to-end.
//
// 30s gives ~20 heartbeats inside the 10-min reaper window — plenty
// of margin for a few skipped ticks under load. Comment frames are
// ":heartbeat\n\n" which SSE parsers ignore by spec, so no client-
// side change is needed.
const heartbeatInterval = 30 * time.Second

// heartbeatPayload is the on-the-wire bytes for one heartbeat. Held
// as a package-level var so we don't allocate a new slice 20×/min ×
// active-connection-count.
var heartbeatPayload = []byte(":heartbeat\n\n")

// NewSSEConnection creates a new SSE connection with monitoring
func NewSSEConnection(c *gin.Context, userID uint, threadID string, timeout time.Duration) *SSEConnection {
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)

	// Connection ID needs to be unique per registration; the previous
	// 1-second-granularity Unix() timestamp let two registrations from
	// the same user/thread inside the same second collide and silently
	// overwrite each other in the manager's connections map. UnixNano
	// + an atomic counter is collision-free even under tight retries.
	conn := &SSEConnection{
		ID:             fmt.Sprintf("sse-%d-%s-%d-%d", userID, threadID, time.Now().UnixNano(), sseConnSeq.Add(1)),
		UserID:         userID,
		ThreadID:       threadID,
		Writer:         c.Writer,
		Context:        ctx,
		Cancel:         cancel,
		StartTime:      time.Now(),
		LastActivity:   time.Now(),
		Connected:      true,
		disconnectChan: make(chan struct{}),
	}

	conn.initWriter()
	// Monitor client disconnection
	go conn.monitorConnection(c)

	return conn
}

// initWriter sets up the bounded write queue and starts the dedicated
// writer goroutine. Called by NewSSEConnection during normal
// construction; exposed so tests that build SSEConnection from a
// struct literal can opt in to the same queueing behaviour.
//
// Idempotent: subsequent calls are no-ops so a test helper can call
// it after struct-literal init without crashing if NewSSEConnection
// already ran.
func (conn *SSEConnection) initWriter() {
	conn.mu.Lock()
	if conn.writeQueue != nil {
		conn.mu.Unlock()
		return
	}
	conn.writeQueue = make(chan []byte, writeQueueCapacity)
	conn.writerDone = make(chan struct{})
	conn.mu.Unlock()
	go conn.runWriter()
}

// runWriter drains writeQueue and issues the actual TCP writes.
//
// Exits on:
//   - Context cancellation (Close, request timeout, client
//     disconnect detected by monitorConnection).
//   - Write failure (TCP reset, write deadline, peer closed).
//
// During quiet stretches a heartbeat comment line is emitted every
// heartbeatInterval — see that const for rationale. Heartbeats go
// through the same writeOne path so a heartbeat write failure (TCP
// reset on a paused tab, etc.) tears the connection down with the
// same fast-fail semantics as a real chunk.
//
// On exit it closes writerDone so Close() can wait for the
// goroutine to finish if it cares (production today doesn't, but
// graceful-shutdown work will).
func (conn *SSEConnection) runWriter() {
	defer close(conn.writerDone)

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		// Snapshot Connected before pulling. If Close already ran the
		// queue may still hold items; we want to drain those quickly
		// without trying to write to a torn-down stream.
		conn.mu.RLock()
		stillConnected := conn.Connected
		conn.mu.RUnlock()
		if !stillConnected {
			return
		}

		select {
		case <-conn.Context.Done():
			return
		case data, ok := <-conn.writeQueue:
			if !ok {
				return
			}
			if !conn.writeOne(data) {
				return
			}
		case <-heartbeat.C:
			// Quiet-stretch keepalive. We deliberately don't reset the
			// ticker on real-chunk writes — a 30s "did you send a
			// heartbeat soon after a real chunk?" emission is a
			// rounding error, and skipping the reset keeps the
			// concurrency model simple (one ticker, no Stop+Reset
			// race window).
			if !conn.writeOne(heartbeatPayload) {
				return
			}
		}
	}
}

// writeOne issues one Write+Flush with a write deadline. Returns
// false on failure so runWriter can exit. Splits out so the test
// surface for "what happens when Write fails" is one function.
func (conn *SSEConnection) writeOne(data []byte) bool {
	// Per-write deadline. http.NewResponseController is Go 1.20+ and
	// returns a non-nil *ResponseController; SetWriteDeadline returns
	// an error only when the underlying ResponseWriter doesn't
	// implement deadline-setting. We log-and-continue in that case
	// rather than refuse the write — better to risk a stuck-forever
	// write on the few test/mock writers than to drop frames on a
	// healthy production client.
	if rc := http.NewResponseController(conn.Writer); rc != nil {
		if err := rc.SetWriteDeadline(time.Now().Add(writeDeadlineDuration)); err != nil {
			// Don't spam logs at every write — only when the
			// deadline actually fires (handled in the err path
			// below).
			_ = err
		}
	}

	conn.writeMu.Lock()
	n, err := conn.Writer.Write(data)
	if err == nil {
		if flusher, ok := conn.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	conn.writeMu.Unlock()

	if err != nil {
		conn.mu.Lock()
		conn.Connected = false
		conn.mu.Unlock()
		globals.Warn(fmt.Sprintf("[SSE] Connection %s write failed (will tear down): %v",
			conn.ID, err))
		return false
	}

	conn.mu.Lock()
	conn.BytesSent += int64(n)
	conn.ChunkCount++
	conn.LastActivity = time.Now()
	conn.mu.Unlock()
	return true
}

// monitorConnection monitors for client disconnection
func (conn *SSEConnection) monitorConnection(c *gin.Context) {
	// Try to get CloseNotifier (deprecated but still works in Gin)
	if notifier, ok := c.Writer.(http.CloseNotifier); ok {
		select {
		case <-notifier.CloseNotify():
			conn.handleDisconnect("client_closed")
		case <-conn.Context.Done():
			conn.handleDisconnect("context_done")
		case <-conn.disconnectChan:
			// Manual disconnect
		}
	} else {
		// Fallback: just wait for context
		<-conn.Context.Done()
		conn.handleDisconnect("context_done")
	}
}

// handleDisconnect handles connection disconnection
func (conn *SSEConnection) handleDisconnect(reason string) {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	if !conn.Connected {
		return // Already disconnected
	}

	conn.Connected = false

	globals.Info(fmt.Sprintf("[SSE] Connection %s disconnected: %s (Duration: %v, Chunks: %d, Bytes: %d)",
		conn.ID, reason, time.Since(conn.StartTime), conn.ChunkCount, conn.BytesSent))

	// Cancel context to stop all operations
	conn.Cancel()
}

// IsConnected checks if connection is still alive
func (conn *SSEConnection) IsConnected() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.Connected
}

// WriteChunk enqueues a chunk for the writer goroutine to send. Never
// blocks: if the queue is full (slow client backpressure), the chunk
// is dropped and ChunksDropped is bumped so operators can see the
// pressure in /sse/stats.
//
// Behaviour change vs. the prior synchronous design:
//   - Returns nil immediately on successful enqueue (or on overflow
//     drop). Callers no longer get write-error feedback synchronously.
//   - Returns "connection closed" once a previous write actually
//     failed (writer goroutine flips Connected=false in writeOne).
//     The next WriteChunk call sees the flag and errors fast — so
//     the SDK callback path stops emitting at most one chunk after
//     the real failure.
//
// Why drop-on-overflow instead of block-on-full: the producer side is
// the SDK callback that consumes upstream-billed tokens. Blocking it
// on a slow frontend client means we keep paying the upstream gateway
// for tokens the user can't even see. Dropping the visible-side bits
// is the lesser of two evils.
//
// We copy `data` because the caller (sendEvent in agent_api.go) holds
// the byte slice in a closure and may reuse the underlying buffer
// across calls.
func (conn *SSEConnection) WriteChunk(data []byte) error {
	conn.mu.RLock()
	connected := conn.Connected
	queue := conn.writeQueue
	conn.mu.RUnlock()
	if !connected {
		return fmt.Errorf("connection closed")
	}
	if queue == nil {
		// Construction error — initWriter was never called. Surface
		// loudly rather than block forever on a nil channel.
		return fmt.Errorf("write queue not initialised")
	}

	buf := make([]byte, len(data))
	copy(buf, data)

	select {
	case queue <- buf:
		return nil
	default:
		dropped := atomic.AddInt64(&conn.ChunksDropped, 1)
		// Log at warn so an operator can spot a chronic slow-client
		// pattern but at coarse intervals — every Nth drop, not every
		// drop. Otherwise a stuck client floods the log faster than
		// the chunks were arriving.
		if dropped == 1 || dropped%32 == 0 {
			globals.Warn(fmt.Sprintf("[SSE] Connection %s write queue full; dropped chunk %d (slow client)",
				conn.ID, dropped))
		}
		return nil
	}
}

// Close gracefully closes the connection
func (conn *SSEConnection) Close() {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	if !conn.Connected {
		return
	}

	conn.Connected = false
	conn.Cancel()
	close(conn.disconnectChan)

	globals.Info(fmt.Sprintf("[SSE] Connection %s closed gracefully (Duration: %v, Chunks: %d)",
		conn.ID, time.Since(conn.StartTime), conn.ChunkCount))
}

// SSEConnectionManager manages all active SSE connections
type SSEConnectionManager struct {
	connections    map[string]*SSEConnection
	maxConnections int
	mu             sync.RWMutex

	// userCounts is a denormalised view of how many connections each
	// userID currently holds. Maintained alongside `connections` under
	// the same `mu` so the per-user limit check in Register is O(1)
	// instead of an O(N) full-map iteration. Without this, Register's
	// hot path scans every entry while holding mu.Lock — at the
	// 500-conn global cap that's an N² locking pattern, and any slow
	// reader (cleanupStaleConnections taking conn.mu.RLock per entry)
	// can interleave to stall every concurrent registration.
	//
	// Keys are removed when the count hits zero so the map size stays
	// bounded by active-user count, not lifetime user count.
	userCounts map[uint]int

	// stop signals the cleanup goroutine to exit. Without this the
	// ticker goroutine ran for the entire process lifetime, so graceful
	// shutdown (or a test that constructs a manager and tears it back
	// down) leaked goroutines indefinitely.
	stopCtx    context.Context
	stopCancel context.CancelFunc
}

// NewSSEConnectionManager creates a new connection manager
func NewSSEConnectionManager(maxConnections int) *SSEConnectionManager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &SSEConnectionManager{
		connections:    make(map[string]*SSEConnection),
		userCounts:     make(map[uint]int),
		maxConnections: maxConnections,
		stopCtx:        ctx,
		stopCancel:     cancel,
	}

	// Start cleanup goroutine
	go manager.cleanupStaleConnections(ctx)

	return manager
}

// MaxConcurrentSSEConnections caps the number of in-flight SSE streams
// across all users. Set high enough to absorb a full work-session cohort,
// low enough to keep Go-routine fan-out bounded under a connection storm.
const MaxConcurrentSSEConnections = 500

// MaxConcurrentSSEConnectionsPerUser caps the number of in-flight SSE
// streams per individual user. Without this, a single abusive (or buggy)
// client could open the full 500 global slots and deny service to every
// other user. 5 is enough to cover a power-user with the chat tab plus
// one or two background tools running; the cap surfaces as ErrPerUserSSE
// from Register, which the handler maps to a user-facing "you have too
// many sessions" message — distinct from the generic "server busy".
const MaxConcurrentSSEConnectionsPerUser = 5

var (
	globalSSEManager     *SSEConnectionManager
	globalSSEManagerOnce sync.Once
)

// GetGlobalSSEManager returns the global SSE connection manager,
// constructing it on first call. Lazy via sync.Once so the cleanup
// goroutine doesn't spawn at package-init time before main has even
// decided whether work-agent is enabled.
func GetGlobalSSEManager() *SSEConnectionManager {
	globalSSEManagerOnce.Do(func() {
		globalSSEManager = NewSSEConnectionManager(MaxConcurrentSSEConnections)
	})
	return globalSSEManager
}

// Shutdown stops the cleanup goroutine and closes every active
// connection. Safe to call multiple times. Used by tests that want a
// clean teardown; production wires this into the SIGTERM handler when
// graceful-shutdown is added.
func (m *SSEConnectionManager) Shutdown() {
	if m.stopCancel != nil {
		m.stopCancel()
	}
	m.CloseAll()
}

// ErrPerUserSSELimit is returned from Register when the calling user is
// already at MaxConcurrentSSEConnectionsPerUser concurrent streams.
// Distinct from the global-cap error so the handler can return a more
// specific user-facing message ("you have too many active sessions"
// vs. "server busy").
var ErrPerUserSSELimit = errors.New("per-user SSE connection limit reached")

// Register registers a new connection. Enforces the per-user cap first
// (so an abusive user can't fill all the global slots), then the global
// cap. Returns ErrPerUserSSELimit when the user is at their personal
// max so the caller can tell the user from a generic-busy.
func (m *SSEConnectionManager) Register(conn *SSEConnection) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Per-user cap first — even if global slots are available, one user
	// holding too many of them is a fairness/abuse concern. O(1) via
	// the denormalised userCounts; previously this was an O(N) scan
	// over every entry while holding mu.Lock — see userCounts comment.
	if conn.UserID != 0 {
		if m.userCounts[conn.UserID] >= MaxConcurrentSSEConnectionsPerUser {
			return ErrPerUserSSELimit
		}
	}

	// Global cap.
	if len(m.connections) >= m.maxConnections {
		return fmt.Errorf("max connections reached: %d", m.maxConnections)
	}

	m.connections[conn.ID] = conn
	if conn.UserID != 0 {
		m.userCounts[conn.UserID]++
	}
	globals.Info(fmt.Sprintf("[SSE] Registered connection %s (Total: %d)", conn.ID, len(m.connections)))

	return nil
}

// decrementUserCountLocked drops the userCounts entry for uid, removing
// the key entirely when the count reaches zero so the map size stays
// bounded by active users rather than lifetime users. Must be called
// with m.mu held.
func (m *SSEConnectionManager) decrementUserCountLocked(uid uint) {
	if uid == 0 {
		return
	}
	if c := m.userCounts[uid]; c <= 1 {
		delete(m.userCounts, uid)
	} else {
		m.userCounts[uid] = c - 1
	}
}

// Unregister removes a connection
func (m *SSEConnectionManager) Unregister(connID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if conn, exists := m.connections[connID]; exists {
		conn.Close()
		delete(m.connections, connID)
		m.decrementUserCountLocked(conn.UserID)
		globals.Info(fmt.Sprintf("[SSE] Unregistered connection %s (Total: %d)", connID, len(m.connections)))
	}
}

// Get retrieves a connection by ID
func (m *SSEConnectionManager) Get(connID string) (*SSEConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, exists := m.connections[connID]
	return conn, exists
}

// GetStats returns connection statistics
func (m *SSEConnectionManager) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	totalBytes := int64(0)
	totalChunks := int64(0)
	totalDropped := int64(0)
	oldestConnection := time.Now()

	for _, conn := range m.connections {
		conn.mu.RLock()
		totalBytes += conn.BytesSent
		totalChunks += conn.ChunkCount
		if conn.StartTime.Before(oldestConnection) {
			oldestConnection = conn.StartTime
		}
		conn.mu.RUnlock()
		// ChunksDropped is updated atomically (off the conn.mu hot
		// path in WriteChunk) so we read it without holding mu.
		totalDropped += atomic.LoadInt64(&conn.ChunksDropped)
	}

	return map[string]interface{}{
		"active_connections":  len(m.connections),
		"max_connections":     m.maxConnections,
		"total_bytes_sent":    totalBytes,
		"total_chunks_sent":   totalChunks,
		"total_chunks_dropped": totalDropped,
		"oldest_connection":   time.Since(oldestConnection).String(),
	}
}

// cleanupStaleConnections periodically cleans up stale connections.
// Exits when ctx is cancelled (Shutdown called).
//
// Two phases per tick to avoid blocking Register/Unregister while we
// close stale conns:
//
//  1. Under m.mu: scan, decide what's stale, delete from the map,
//     collect a slice of *SSEConnection to close. Pure-memory work,
//     completes in microseconds even at the 500-conn cap.
//  2. After releasing m.mu: call conn.Close() on each. Close grabs
//     conn.mu and may flush a stale TCP send buffer — no reason to
//     hold the manager's lock through that.
//
// Without this split, a cleanup tick that has to close 50 stale
// conns (each Close potentially syscall-bound) would freeze every
// new Register / Unregister / Get call for the duration.
func (m *SSEConnectionManager) cleanupStaleConnections(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		staleTimeout := 10 * time.Minute
		now := time.Now()

		m.mu.Lock()
		toClose := make([]*SSEConnection, 0)
		staleIDs := make([]string, 0)
		for id, conn := range m.connections {
			conn.mu.RLock()
			isStale := !conn.Connected || now.Sub(conn.LastActivity) > staleTimeout
			conn.mu.RUnlock()

			if isStale {
				delete(m.connections, id)
				m.decrementUserCountLocked(conn.UserID)
				toClose = append(toClose, conn)
				staleIDs = append(staleIDs, id)
			}
		}
		remaining := len(m.connections)
		m.mu.Unlock()

		// Close outside the manager lock so a slow Close can't
		// freeze concurrent Register / Unregister / Get callers.
		// Per-conn line is Debug; the post-loop summary INFO below
		// gives operators the shape of the cleanup tick without
		// flooding logs at burst (a tick that closes 50 stale conns
		// used to emit 50 Warn lines plus the summary). Same
		// chatty-log demotion as e25f7d6a / 48cf818a / 4e8f18fb.
		for i, conn := range toClose {
			globals.Debug(fmt.Sprintf("[SSE] Cleaning up stale connection %s", staleIDs[i]))
			conn.Close()
		}

		if len(toClose) > 0 {
			globals.Info(fmt.Sprintf("[SSE] Cleaned up %d stale connections (Total: %d)",
				len(toClose), remaining))
		}
	}
}

// CloseAll closes all connections (for shutdown)
func (m *SSEConnectionManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, conn := range m.connections {
		conn.Close()
		delete(m.connections, id)
	}
	// Reset rather than per-conn decrement — we just zeroed every
	// entry so the counts must all be zero. Safer and faster than
	// looping decrementUserCountLocked.
	for uid := range m.userCounts {
		delete(m.userCounts, uid)
	}

	globals.Info("[SSE] Closed all connections")
}

// GetConnectionsByUser returns all connections for a specific user
func (m *SSEConnectionManager) GetConnectionsByUser(userID uint) []*SSEConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var userConnections []*SSEConnection
	for _, conn := range m.connections {
		if conn.UserID == userID {
			userConnections = append(userConnections, conn)
		}
	}

	return userConnections
}

// GetConnectionsByThread returns all connections for a specific thread
func (m *SSEConnectionManager) GetConnectionsByThread(threadID string) []*SSEConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var threadConnections []*SSEConnection
	for _, conn := range m.connections {
		if conn.ThreadID == threadID {
			threadConnections = append(threadConnections, conn)
		}
	}

	return threadConnections
}
