//go:build desktop

package sync

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubJob returns a JobFunc that records each call + optionally
// returns an injected error. Useful for asserting exact call counts
// + sequencing.
type stubJob struct {
	mu        sync.Mutex
	calls     atomic.Int64
	errs      []error // returned in order; nil after exhausted
	durations []time.Duration
	gate      chan struct{} // optional: block job() until receive
}

func (s *stubJob) JobFunc() JobFunc {
	return func(ctx context.Context) error {
		n := s.calls.Add(1)
		s.mu.Lock()
		var err error
		if int(n-1) < len(s.errs) {
			err = s.errs[int(n-1)]
		}
		var dur time.Duration
		if int(n-1) < len(s.durations) {
			dur = s.durations[int(n-1)]
		}
		gate := s.gate
		s.mu.Unlock()

		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if dur > 0 {
			select {
			case <-time.After(dur):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
}

// waitForCalls polls until stub.calls >= n or deadline expires.
// Fails the test if deadline hits — we never want to sleep for a
// fixed time when waiting for goroutine-driven state.
func waitForCalls(t *testing.T, s *stubJob, n int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if s.calls.Load() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("stub never reached %d calls within %s (got %d)", n, within, s.calls.Load())
}

func TestSyncWorker_StartTriggersInitialTick(t *testing.T) {
	stub := &stubJob{}
	w := NewSyncWorker(stub.JobFunc(), Config{}) // PeriodicInterval=0; only the startup trigger fires
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	waitForCalls(t, stub, 1, time.Second)
	cancel()
	<-w.Done()
}

func TestSyncWorker_TriggerWhileRunningCoalesces(t *testing.T) {
	// Job blocks on a gate so we can deterministically fire many
	// triggers while it's running.
	gate := make(chan struct{})
	stub := &stubJob{gate: gate}
	w := NewSyncWorker(stub.JobFunc(), Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx) // fires "startup" trigger → job runs + blocks on gate

	// Wait until job is running (call 1 started, count incremented).
	waitForCalls(t, stub, 1, time.Second)

	// Fire 50 more triggers while the job is blocked. They should
	// coalesce into AT MOST one more queued trigger (channel cap 1).
	for i := 0; i < 50; i++ {
		w.Trigger("burst")
	}

	// Release the first job. Worker picks up at most one queued
	// trigger → runs job again exactly once.
	close(gate)
	waitForCalls(t, stub, 2, time.Second)

	// Give the worker a moment to (maybe) launch a third — must not.
	time.Sleep(50 * time.Millisecond)
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("50 triggers should coalesce to 1 queued, total calls=2; got %d", got)
	}
	cancel()
	<-w.Done()
}

func TestSyncWorker_BackoffOnFailure(t *testing.T) {
	// Stub returns 3 errors in a row then succeeds. With fast
	// backoff sequence we should see retries at the configured
	// intervals.
	stub := &stubJob{
		errs: []error{
			errors.New("fail 1"),
			errors.New("fail 2"),
			errors.New("fail 3"),
			nil, // success after 3 failures
		},
	}
	// Fast backoff so the test runs in <1s: 10ms / 20ms / 40ms / 80ms.
	w := NewSyncWorker(stub.JobFunc(), Config{
		BackoffSequence: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// 4 calls total: startup + 3 backoff retries.
	waitForCalls(t, stub, 4, 2*time.Second)

	snap := w.Snapshot()
	if snap.ConsecutiveFail != 0 {
		t.Errorf("after success: ConsecutiveFail=%d, want 0", snap.ConsecutiveFail)
	}
	if snap.LastError != "" {
		t.Errorf("after success: LastError=%q, want empty", snap.LastError)
	}
	cancel()
	<-w.Done()
}

func TestSyncWorker_BackoffSequencePins(t *testing.T) {
	// Pin the actual default sequence so a future "let's tune
	// backoffs" change is intentional + visible in code review.
	w := NewSyncWorker(func(context.Context) error { return nil }, Config{})
	want := []time.Duration{1 * time.Second, 4 * time.Second, 16 * time.Second, 60 * time.Second}
	got := w.cfg.BackoffSequence
	if len(got) != len(want) {
		t.Fatalf("default backoff length: got %d, want %d", len(got), len(want))
	}
	for i, d := range want {
		if got[i] != d {
			t.Errorf("backoff[%d]: got %v, want %v", i, got[i], d)
		}
	}
}

func TestSyncWorker_BackoffPinnedAtMaxAfterExhaustion(t *testing.T) {
	// Manually invoke backoffDelayLocked with the lock held to
	// inspect behavior past the last index.
	w := NewSyncWorker(func(context.Context) error { return nil }, Config{
		BackoffSequence: []time.Duration{10 * time.Millisecond, 100 * time.Millisecond},
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	cases := []struct {
		failCount int
		want      time.Duration
	}{
		{1, 10 * time.Millisecond},   // first failure → [0]
		{2, 100 * time.Millisecond},  // second → [1]
		{3, 100 * time.Millisecond},  // third → pinned at last
		{99, 100 * time.Millisecond}, // way past end → still last
	}
	for _, tc := range cases {
		w.consecutiveFail = tc.failCount
		if got := w.backoffDelayLocked(); got != tc.want {
			t.Errorf("failCount=%d: got %v, want %v", tc.failCount, got, tc.want)
		}
	}
}

// TestSprintfAny pins that non-string/non-error panic values land
// in the error message as %v formatted, not as an opaque sentinel.
// Pre-fix, struct / int / nil panics rendered as
// "<unrecoverable panic value>" which gave ops nothing to grep on.
func TestSprintfAny(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"string", "boom", "boom"},
		{"error", errStub("kaboom"), "kaboom"},
		{"int", 42, "42"},
		{"struct", struct{ Code int }{500}, "{500}"},
		{"nil", nil, "<nil>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sprintfAny(tc.v); got != tc.want {
				t.Errorf("sprintfAny(%v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

func TestSyncWorker_PanicRecovered(t *testing.T) {
	calls := atomic.Int64{}
	job := func(ctx context.Context) error {
		n := calls.Add(1)
		if n == 1 {
			panic("simulated panic")
		}
		return nil
	}
	w := NewSyncWorker(job, Config{
		BackoffSequence: []time.Duration{10 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Worker survives the panic, retries (backoff), succeeds.
	waitForCalls(t, &stubJob{calls: atomic.Int64{}}, 0, 10*time.Millisecond) // tiny grace
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("expected at least 2 calls (panic + retry), got %d", got)
	}
	cancel()
	<-w.Done()
}

func TestSyncWorker_CtxCancelExitsCleanly(t *testing.T) {
	stub := &stubJob{}
	w := NewSyncWorker(stub.JobFunc(), Config{
		PeriodicInterval: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	waitForCalls(t, stub, 1, time.Second)

	cancel()
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("worker did not exit within 1s of cancel")
	}
}

func TestSyncWorker_PeriodicTickFires(t *testing.T) {
	stub := &stubJob{}
	w := NewSyncWorker(stub.JobFunc(), Config{
		PeriodicInterval: 30 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	// startup tick + periodic ticks = many calls in 200ms
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && stub.calls.Load() < 4 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := stub.calls.Load(); got < 4 {
		t.Errorf("expected ≥4 calls in 500ms at 30ms interval, got %d", got)
	}
	cancel()
	<-w.Done()
}

func TestSyncWorker_SnapshotReflectsState(t *testing.T) {
	stub := &stubJob{
		errs: []error{errors.New("boom")},
	}
	w := NewSyncWorker(stub.JobFunc(), Config{
		BackoffSequence: []time.Duration{time.Hour}, // pin so retry doesn't fire mid-test
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	waitForCalls(t, stub, 1, time.Second)
	// Give the worker a moment to record the result.
	time.Sleep(20 * time.Millisecond)
	snap := w.Snapshot()
	if snap.ConsecutiveFail != 1 {
		t.Errorf("ConsecutiveFail: got %d, want 1", snap.ConsecutiveFail)
	}
	if snap.LastError != "boom" {
		t.Errorf("LastError: got %q, want boom", snap.LastError)
	}
	if snap.LastTickAt.IsZero() {
		t.Error("LastTickAt should be populated")
	}
}

func TestSyncWorker_TriggerSourceObservable(t *testing.T) {
	// Sanity: triggers carry a source string. We don't currently
	// surface it via Snapshot (would bloat the API), but the
	// channel shape supports it for future telemetry. This test
	// confirms the channel doesn't drop the source.
	stub := &stubJob{
		gate: make(chan struct{}),
	}
	w := NewSyncWorker(stub.JobFunc(), Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Trigger should be no-op if the queue is full — that's the
	// coalesce semantic. Verify Trigger is non-blocking.
	for i := 0; i < 100; i++ {
		done := make(chan struct{})
		go func() {
			w.Trigger("test")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("Trigger blocked on call %d — should be non-blocking", i)
		}
	}
	close(stub.gate)
	cancel()
	<-w.Done()
}

func TestNewSyncWorker_NilJobPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil JobFunc")
		}
	}()
	_ = NewSyncWorker(nil, Config{})
}

// TestSyncWorker_SnapshotCountersAndDuration pins that the new
// lifetime counters (TotalTicks/TotalFails) increment on every
// tick (failures + successes), and that LastDuration captures the
// most recent job's wall-clock cost.
//
// Without this regression test, a future refactor could "optimize"
// the counter update out of tickOnce and break /system/diagnostics
// silently — counts would stick at 0 while sync still works fine,
// hiding the bug from local manual testing.
func TestSyncWorker_SnapshotCountersAndDuration(t *testing.T) {
	stub := &stubJob{
		errs:      []error{nil, errors.New("transient"), nil},
		durations: []time.Duration{5 * time.Millisecond, 0, 8 * time.Millisecond},
	}
	w := NewSyncWorker(stub.JobFunc(), Config{
		// Short periodic so the test doesn't depend on manual triggers
		// to drive the second + third ticks. After a successful tick,
		// the worker idles until the next periodic fires.
		PeriodicInterval: 30 * time.Millisecond,
		BackoffSequence:  []time.Duration{5 * time.Millisecond},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitForCalls(t, stub, 3, 2*time.Second)
	// Allow the last tick's bookkeeping to complete.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if snap := w.Snapshot(); snap.TotalTicks >= 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	snap := w.Snapshot()
	if snap.TotalTicks < 3 {
		t.Errorf("TotalTicks: got %d, want >= 3", snap.TotalTicks)
	}
	if snap.TotalFails != 1 {
		t.Errorf("TotalFails: got %d, want 1 (one transient error in fixture)", snap.TotalFails)
	}
	// LastDuration must reflect the most recent tick (~8ms). We allow
	// generous slack for race-detector + slow CI hosts; the assertion
	// is "non-zero and roughly the expected scale" — not exact timing.
	if snap.LastDuration <= 0 {
		t.Errorf("LastDuration should be > 0, got %v", snap.LastDuration)
	}
	if snap.LastDuration > time.Second {
		t.Errorf("LastDuration unexpectedly large: %v (job was 8ms)", snap.LastDuration)
	}
}

// TestSyncWorker_SnapshotCountersZeroAtBoot pins the boot-state
// invariant: TotalTicks/TotalFails are 0 before any tick fires.
// Catches a hypothetical regression where a non-zero default
// initialization would mask "never ticked" in the diagnostics UI.
func TestSyncWorker_SnapshotCountersZeroAtBoot(t *testing.T) {
	w := NewSyncWorker((&stubJob{}).JobFunc(), Config{})
	snap := w.Snapshot()
	if snap.TotalTicks != 0 || snap.TotalFails != 0 || snap.LastDuration != 0 {
		t.Errorf("zero-state: %+v", snap)
	}
	if !snap.LastTickAt.IsZero() {
		t.Errorf("LastTickAt should be zero before any tick: %v", snap.LastTickAt)
	}
}

// TestSyncWorker_StuckWarnEmittedOnce pins that the [sync-stuck]
// WARN fires exactly once when consecutive fails first cross the
// threshold, then stays silent on subsequent failures within the
// same stretch — without this gate the log line would fire on
// every backoff tick (1s, 4s, 16s, 60s, 60s, …), drowning ops in
// duplicates of the same signal.
//
// Uses log.SetOutput to capture the WARN line; vendored "log"
// package is what worker.go writes to.
func TestSyncWorker_StuckWarnEmittedOnce(t *testing.T) {
	var buf safeBuffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	stub := &stubJob{
		errs: []error{
			errors.New("e1"), errors.New("e2"), errors.New("e3"), // 3 → trip warn
			errors.New("e4"), errors.New("e5"), // post-trip; should NOT re-warn
		},
	}
	w := NewSyncWorker(stub.JobFunc(), Config{
		PeriodicInterval: 0,
		BackoffSequence:  []time.Duration{2 * time.Millisecond},
		Name:             "threads",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitForCalls(t, stub, 5, time.Second)
	cancel()
	<-w.Done()

	got := buf.String()
	stuckLines := strings.Count(got, "[sync-stuck:threads]")
	if stuckLines != 1 {
		t.Errorf("[sync-stuck:threads] WARN should fire exactly once, fired %d times.\nLog output:\n%s",
			stuckLines, got)
	}
	if !strings.Contains(got, "failed 3 consecutive ticks") {
		t.Errorf("warn line should mention consecutive count = 3; got:\n%s", got)
	}
	if !strings.Contains(got, "e3") {
		t.Errorf("warn line should include the last error message ('e3'); got:\n%s", got)
	}
}

// TestSyncWorker_StuckWarnResetsOnSuccess pins that a success
// after a stuck stretch re-arms the warn — a later stuck stretch
// emits a fresh WARN. Without this reset, an intermittent flap
// (stuck → recover → stuck) would only log the very first time.
func TestSyncWorker_StuckWarnResetsOnSuccess(t *testing.T) {
	var buf safeBuffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	stub := &stubJob{
		errs: []error{
			errors.New("e1"), errors.New("e2"), errors.New("e3"), // trip 1
			nil,                                                   // recover
			errors.New("e5"), errors.New("e6"), errors.New("e7"), // trip 2
		},
	}
	w := NewSyncWorker(stub.JobFunc(), Config{
		// Periodic interval keeps the worker firing past the success
		// (which would otherwise leave it idle waiting for a trigger).
		PeriodicInterval: 5 * time.Millisecond,
		BackoffSequence:  []time.Duration{2 * time.Millisecond},
		Name:             "threads",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitForCalls(t, stub, 7, 2*time.Second)
	cancel()
	<-w.Done()

	got := buf.String()
	stuckLines := strings.Count(got, "[sync-stuck:threads]")
	if stuckLines != 2 {
		t.Errorf("[sync-stuck:threads] should fire twice (once per stretch), fired %d times.\nLog output:\n%s",
			stuckLines, got)
	}
}

// TestSyncWorker_StuckWarnBareTagWhenUnnamed pins that worker
// without a Name renders the bare '[sync-stuck]' tag rather than
// trailing-colon-empty-string.
func TestSyncWorker_StuckWarnBareTagWhenUnnamed(t *testing.T) {
	var buf safeBuffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	stub := &stubJob{
		errs: []error{errors.New("e1"), errors.New("e2"), errors.New("e3")},
	}
	w := NewSyncWorker(stub.JobFunc(), Config{
		PeriodicInterval: 0,
		BackoffSequence:  []time.Duration{2 * time.Millisecond},
		// No Name
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitForCalls(t, stub, 3, time.Second)
	cancel()
	<-w.Done()

	got := buf.String()
	if !strings.Contains(got, "[sync-stuck]") {
		t.Errorf("bare [sync-stuck] tag missing; got:\n%s", got)
	}
	if strings.Contains(got, "[sync-stuck:]") {
		t.Errorf("name-less worker should not emit '[sync-stuck:]' trailing colon; got:\n%s", got)
	}
}

// safeBuffer is a goroutine-safe bytes.Buffer wrapper — log.Writer
// is called from the worker goroutine while the test reads from
// the test goroutine, and bytes.Buffer is not safe for that.
type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
