package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"server/service/agentturn"
)

var (
	errWorkerResourcesInvalid      = errors.New("agent-worker composition resources are invalid")
	errWorkerResourceCloseFailed   = errors.New("agent-worker composition resource close failed")
	errWorkerResourceCloseTimedOut = errors.New("agent-worker composition resource close timed out")
)

const maxWorkerCompositionResources = 64

// WorkerResourceCloser is the ownership boundary for process-scoped
// dependencies such as SQL pools and provider clients. Implementations must be
// safe to close while an operation that ignored cancellation is still
// returning, and they must honor ctx whenever the underlying client permits it.
//
// Close errors and panic values are deliberately discarded at this boundary;
// callers receive only the stable classifications above.
type WorkerResourceCloser interface {
	Close(context.Context) error
}

// WorkerResourceCloseFunc adapts a function to WorkerResourceCloser.
type WorkerResourceCloseFunc func(context.Context) error

func (close WorkerResourceCloseFunc) Close(ctx context.Context) error {
	if close == nil {
		return errWorkerResourcesInvalid
	}
	return close(ctx)
}

// workerResourceStack is immutable after construction. A composition owns the
// copied entries in acquisition order and closes them in reverse order.
//
// start.Do only publishes one asynchronous close operation. It must never run
// an underlying closer inline: a non-cooperative dependency would otherwise
// make every concurrent Close caller block on sync.Once's mutex.
type workerResourceStack struct {
	resources  []WorkerResourceCloser
	start      sync.Once
	closing    atomic.Bool
	admissionM sync.Mutex
	admission  *agentturn.AdmissionGate
	done       chan struct{}
	result     error
}

// newWorkerResourceStack returns an owner even on validation failure so
// Compose can release every valid entry it was handed. Callers must close a
// non-nil returned stack regardless of err.
func newWorkerResourceStack(resources []WorkerResourceCloser) (*workerResourceStack, error) {
	invalid := len(resources) > maxWorkerCompositionResources
	copyOfResources := make([]WorkerResourceCloser, 0, len(resources))
	for _, resource := range resources {
		if dependencyMissing(resource) {
			invalid = true
			continue
		}
		copyOfResources = append(copyOfResources, resource)
	}
	stack := &workerResourceStack{
		resources: copyOfResources,
		done:      make(chan struct{}),
	}
	if invalid {
		return stack, errWorkerResourcesInvalid
	}
	return stack, nil
}

func (resources *workerResourceStack) isOpen() bool {
	return resources != nil && !resources.closing.Load()
}

// bindAdmissionGate makes admission revocation part of the resource owner's
// close protocol. The mutex linearizes a late build cancellation against Gate
// installation: either the Gate is bound and every closer observes it closed,
// or closing already started and the caller must reject/close the new Gate.
func (resources *workerResourceStack) bindAdmissionGate(gate *agentturn.AdmissionGate) bool {
	if resources == nil || gate == nil || !gate.Open() {
		return false
	}
	resources.admissionM.Lock()
	defer resources.admissionM.Unlock()
	if resources.closing.Load() || resources.admission != nil || !gate.Open() {
		gate.Close()
		return false
	}
	resources.admission = gate
	return true
}

func (resources *workerResourceStack) matchesAdmissionGate(gate *agentturn.AdmissionGate) bool {
	if resources == nil || gate == nil {
		return false
	}
	resources.admissionM.Lock()
	defer resources.admissionM.Unlock()
	return resources.admission == gate
}

func (resources *workerResourceStack) beginClose(timeout time.Duration) {
	if resources == nil {
		return
	}
	if timeout <= 0 {
		timeout = defaultWorkerResourceCloseTimeout
	}
	resources.admissionM.Lock()
	if resources.admission != nil {
		resources.admission.Close()
	}
	resources.start.Do(func() {
		resources.closing.Store(true)
		go resources.closeAll(timeout)
	})
	resources.admissionM.Unlock()
}

// Close is concurrent-safe and idempotent. The first caller fixes the bounded
// close budget; later callers observe the same terminal result, or their own
// context deadline while the shared close operation is still running.
func (resources *workerResourceStack) Close(ctx context.Context) error {
	if resources == nil {
		return errWorkerResourcesInvalid
	}
	timeout := defaultWorkerResourceCloseTimeout
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline)
			if timeout <= 0 {
				timeout = time.Nanosecond
			}
		}
	}
	resources.beginClose(timeout)

	select {
	case <-resources.done:
		return resources.result
	default:
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-resources.done:
		if ctx.Err() != nil {
			return errWorkerResourceCloseTimedOut
		}
		return resources.result
	case <-ctx.Done():
		return errWorkerResourceCloseTimedOut
	}
}

type workerResourceCloseOutcome uint8

const (
	workerResourceCloseSucceeded workerResourceCloseOutcome = iota
	workerResourceCloseFailed
	workerResourceCloseTimedOut
)

func (resources *workerResourceStack) closeAll(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	failed := false
	timedOut := false
	for index := len(resources.resources) - 1; index >= 0; index-- {
		remaining := time.Until(deadline)
		remainingResources := time.Duration(index + 1)
		perResourceTimeout := remaining / remainingResources
		if perResourceTimeout <= 0 {
			perResourceTimeout = time.Nanosecond
		}
		switch executeWorkerResourceClose(resources.resources[index], perResourceTimeout) {
		case workerResourceCloseFailed:
			failed = true
		case workerResourceCloseTimedOut:
			timedOut = true
		}
	}
	switch {
	case timedOut:
		resources.result = errWorkerResourceCloseTimedOut
	case failed:
		resources.result = errWorkerResourceCloseFailed
	default:
		resources.result = nil
	}
	close(resources.done)
}

// executeWorkerResourceClose isolates both panic and a closer that ignores its
// context. A timed-out closer may leave one quarantined goroutine, but it cannot
// prevent later LIFO entries from receiving their own close attempt.
func executeWorkerResourceClose(resource WorkerResourceCloser, timeout time.Duration) workerResourceCloseOutcome {
	if dependencyMissing(resource) {
		return workerResourceCloseFailed
	}
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result := make(chan workerResourceCloseOutcome)
	abandoned := make(chan struct{})
	defer close(abandoned)
	go func() {
		outcome := workerResourceCloseFailed
		defer func() {
			if recover() != nil {
				outcome = workerResourceCloseFailed
			}
			select {
			case result <- outcome:
			case <-abandoned:
			}
		}()
		if err := resource.Close(ctx); err == nil {
			outcome = workerResourceCloseSucceeded
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			outcome = workerResourceCloseTimedOut
		}
	}()

	select {
	case outcome := <-result:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return workerResourceCloseTimedOut
		}
		return outcome
	case <-ctx.Done():
		return workerResourceCloseTimedOut
	}
}

// Close releases every process resource owned by the composition. Closing
// starts immediately and makes the composition permanently ineligible for
// readiness, even if an underlying dependency has not returned yet.
func (composition *WorkerComposition) Close(ctx context.Context) error {
	if composition == nil {
		return nil
	}
	closeWorkerCompositionAdmission(composition)
	if composition.resources == nil {
		return errWorkerResourcesInvalid
	}
	return composition.resources.Close(ctx)
}

func beginWorkerCompositionClose(composition *WorkerComposition, timeout time.Duration) {
	if composition == nil {
		return
	}
	closeWorkerCompositionAdmission(composition)
	if composition.resources == nil {
		return
	}
	composition.resources.beginClose(timeout)
}

func workerCompositionAdmissionGate(composition *WorkerComposition) *agentturn.AdmissionGate {
	if composition == nil || composition.runtimeScope == nil {
		return nil
	}
	return composition.runtimeScope.admissionGate()
}

// closeWorkerCompositionAdmission revokes new Claim/Reconcile/Effect authority
// before any owned dependency starts closing. A hard-quarantined process skips
// resource cleanup, but its lifecycle paths still call this helper: leaked
// goroutines may finish only work that crossed Acquire before the close.
func closeWorkerCompositionAdmission(composition *WorkerComposition) {
	if composition == nil {
		return
	}
	workerCompositionAdmissionGate(composition).Close()
	if composition.seal != nil && composition.seal.runtimeScope != nil {
		composition.seal.runtimeScope.admissionGate().Close()
	}
	if composition.ownershipTransfer != nil && composition.ownershipTransfer.runtimeScope != nil {
		composition.ownershipTransfer.runtimeScope.admissionGate().Close()
	}
}

// closeWorkerComposition deliberately removes cancellation and inherited
// deadlines from the serving context. Shutdown cleanup gets its own hard
// budget after SIGTERM has already canceled the Worker loops.
func closeWorkerComposition(parent context.Context, timeout time.Duration, composition *WorkerComposition) error {
	if composition == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultWorkerResourceCloseTimeout
	}
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	closeCtx, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	closeErr := composition.Close(closeCtx)
	if closeErr == nil {
		return nil
	}
	if errors.Is(closeErr, errWorkerResourceCloseTimedOut) {
		return errWorkerResourceCloseTimedOut
	}
	return errWorkerResourceCloseFailed
}
