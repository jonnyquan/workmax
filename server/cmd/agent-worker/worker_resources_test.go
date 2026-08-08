package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

type pointerWorkerResourceCloser struct{}

func (*pointerWorkerResourceCloser) Close(context.Context) error { return nil }

func TestCompositionCloseRevokesAdmissionBeforeFirstResourceClose(t *testing.T) {
	gateWasOpen := make(chan bool, 1)
	var composition *WorkerComposition
	closer := WorkerResourceCloseFunc(func(context.Context) error {
		gateWasOpen <- workerCompositionAdmissionGate(composition).Open()
		return nil
	})
	_, composition = composeForTestWithProbeAndResources(
		t,
		workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{},
		&fakeSettlement{},
		healthyRuntimeProbe{},
		closer,
	)
	gate := workerCompositionAdmissionGate(composition)
	if gate == nil || !gate.Open() {
		t.Fatal("fixture did not start with an open AdmissionGate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := composition.Close(ctx); err != nil {
		t.Fatalf("Composition.Close(): %v", err)
	}
	select {
	case open := <-gateWasOpen:
		if open {
			t.Fatal("resource Close observed AdmissionGate still open")
		}
	case <-time.After(time.Second):
		t.Fatal("resource closer was not invoked")
	}
}

func TestWorkerResourceStackClosesInLIFOOrderAndBecomesClosed(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
	)
	resource := func(name string) WorkerResourceCloser {
		return WorkerResourceCloseFunc(func(context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		})
	}

	stack, err := newWorkerResourceStack([]WorkerResourceCloser{
		resource("database"),
		resource("dispatcher"),
		resource("executor"),
	})
	if err != nil {
		t.Fatalf("newWorkerResourceStack() error = %v", err)
	}
	if !stack.isOpen() {
		t.Fatal("new resource stack is not open")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stack.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if stack.isOpen() {
		t.Fatal("resource stack remained open after Close")
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"executor", "dispatcher", "database"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("close order = %v, want %v", got, want)
	}
}

func TestWorkerResourceStackCloseIsConcurrentAndExactlyOnce(t *testing.T) {
	const callers = 32

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	stack, err := newWorkerResourceStack([]WorkerResourceCloser{
		WorkerResourceCloseFunc(func(context.Context) error {
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-release
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("newWorkerResourceStack() error = %v", err)
	}

	results := make(chan error, callers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			results <- stack.Close(ctx)
		}()
	}
	ready.Wait()
	close(start)

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("underlying resource Close was not called")
	}
	close(release)

	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Close() error = %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stack.Close(ctx); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("underlying Close calls after repeat = %d, want 1", got)
	}
}

func TestNewWorkerResourceStackRejectsTypedNil(t *testing.T) {
	var typedNil *pointerWorkerResourceCloser
	_, err := newWorkerResourceStack([]WorkerResourceCloser{typedNil})
	if !errors.Is(err, errWorkerResourcesInvalid) {
		t.Fatalf("typed-nil error = %v, want %v", err, errWorkerResourcesInvalid)
	}

	_, err = newWorkerResourceStack([]WorkerResourceCloser{nil})
	if !errors.Is(err, errWorkerResourcesInvalid) {
		t.Fatalf("nil error = %v, want %v", err, errWorkerResourcesInvalid)
	}
}

func TestInvalidWorkerResourceStackRetainsValidEntriesForCleanup(t *testing.T) {
	var calls atomic.Int32
	resource := WorkerResourceCloseFunc(func(context.Context) error {
		calls.Add(1)
		return nil
	})
	resources := make([]WorkerResourceCloser, maxWorkerCompositionResources+1)
	for index := range resources {
		resources[index] = resource
	}
	stack, err := newWorkerResourceStack(resources)
	if !errors.Is(err, errWorkerResourcesInvalid) || stack == nil {
		t.Fatalf("oversized stack = (%p, %v), want retained owner and validation error", stack, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stack.Close(ctx); err != nil {
		t.Fatalf("invalid stack cleanup error = %v", err)
	}
	if got := calls.Load(); got != int32(len(resources)) {
		t.Fatalf("valid resource close calls = %d, want %d", got, len(resources))
	}

	var typedNil *pointerWorkerResourceCloser
	stack, err = newWorkerResourceStack([]WorkerResourceCloser{resource, typedNil})
	if !errors.Is(err, errWorkerResourcesInvalid) || stack == nil {
		t.Fatalf("mixed invalid stack = (%p, %v), want retained owner and validation error", stack, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stack.Close(ctx); err != nil {
		t.Fatalf("mixed invalid stack cleanup error = %v", err)
	}
	if got := calls.Load(); got != int32(len(resources)+1) {
		t.Fatalf("valid resource close calls after mixed input = %d, want %d", got, len(resources)+1)
	}
}

func TestWorkerResourceStackRedactsFailureAndPanicAndContinues(t *testing.T) {
	const (
		errorSecret = "database-password-in-error"
		panicSecret = "database-password-in-panic"
	)

	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	stack, err := newWorkerResourceStack([]WorkerResourceCloser{
		WorkerResourceCloseFunc(func(context.Context) error {
			record("oldest")
			return nil
		}),
		WorkerResourceCloseFunc(func(context.Context) error {
			record("error")
			return errors.New(errorSecret)
		}),
		WorkerResourceCloseFunc(func(context.Context) error {
			record("panic")
			panic(panicSecret)
		}),
	})
	if err != nil {
		t.Fatalf("newWorkerResourceStack() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = stack.Close(ctx)
	if !errors.Is(err, errWorkerResourceCloseFailed) {
		t.Fatalf("Close() error = %v, want %v", err, errWorkerResourceCloseFailed)
	}
	if strings.Contains(err.Error(), errorSecret) || strings.Contains(err.Error(), panicSecret) {
		t.Fatalf("Close() exposed a raw resource failure: %q", err)
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"panic", "error", "oldest"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("close order = %v, want %v", got, want)
	}
}

func TestWorkerResourceStackContinuesAfterNonCooperativeCloserTimesOut(t *testing.T) {
	releaseBlocked := make(chan struct{})
	defer close(releaseBlocked)
	blockedEntered := make(chan struct{})
	laterAttempted := make(chan struct{})
	var blockedOnce sync.Once
	var laterOnce sync.Once

	stack, err := newWorkerResourceStack([]WorkerResourceCloser{
		WorkerResourceCloseFunc(func(context.Context) error {
			laterOnce.Do(func() { close(laterAttempted) })
			return nil
		}),
		WorkerResourceCloseFunc(func(context.Context) error {
			blockedOnce.Do(func() { close(blockedEntered) })
			<-releaseBlocked
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("newWorkerResourceStack() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = stack.Close(ctx)
	if !errors.Is(err, errWorkerResourceCloseTimedOut) {
		t.Fatalf("Close() error = %v, want %v", err, errWorkerResourceCloseTimedOut)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close() exceeded its hard bound: %s", elapsed)
	}

	select {
	case <-blockedEntered:
	default:
		t.Fatal("non-cooperative closer was not attempted first")
	}
	select {
	case <-laterAttempted:
	case <-time.After(time.Second):
		t.Fatal("an older resource was not attempted after the newer closer timed out")
	}
	if stack.isOpen() {
		t.Fatal("timed-out resource stack remained open")
	}
}
