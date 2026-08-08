package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	errWorkerAcquisitionInvalid  = errors.New("agent-worker dependency acquisition is invalid")
	errWorkerAcquisitionClosed   = errors.New("agent-worker dependency acquisition is closed")
	errWorkerAcquisitionFailed   = errors.New("agent-worker dependency acquisition failed")
	errWorkerOwnershipRequired   = errors.New("agent-worker dependency ownership declaration is required")
	errWorkerOwnershipUntrusted  = errors.New("agent-worker dependency ownership receipt is invalid")
	errWorkerAcquisitionTransfer = errors.New("agent-worker dependency ownership transfer failed")
)

type workerAcquisitionState uint8

const (
	workerAcquisitionCollecting workerAcquisitionState = iota + 1
	workerAcquisitionSealed
	workerAcquisitionTransferred
	workerAcquisitionAborted
)

// workerAcquisitionGuard owns every resource from the instant a Factory calls
// Own until the exact same immutable stack is transferred into a successfully
// sealed composition. Context cancellation aborts partial acquisition even if
// the currently executing Factory never returns.
type workerAcquisitionGuard struct {
	mu           sync.Mutex
	ctx          context.Context
	closeTimeout time.Duration
	state        workerAcquisitionState
	resources    []WorkerResourceCloser
	owner        *workerResourceStack
	transfer     *workerOwnershipTransferSeal
	activeStep   uint64
	nextStep     uint64
	stepStart    int
	stepPoisoned bool
	receipts     map[uint64]*workerOwnershipToken
	stopAbort    func() bool
}

type workerOwnershipToken struct{ marker byte }

type workerOwnershipReceipt struct {
	guard *workerAcquisitionGuard
	step  uint64
	token *workerOwnershipToken
}

type workerStepRegistrar struct {
	guard *workerAcquisitionGuard
	step  uint64
}

type workerOwnershipPolicy struct {
	requireOwned bool
	forbidOwned  bool
	parents      []workerOwnershipReceipt
}

func workerOwnsResource() workerOwnershipPolicy {
	return workerOwnershipPolicy{requireOwned: true}
}

func workerBorrowsFrom(parents ...workerOwnershipReceipt) workerOwnershipPolicy {
	return workerOwnershipPolicy{
		forbidOwned: true, parents: append([]workerOwnershipReceipt(nil), parents...),
	}
}

func workerOwnsWithParents(parents ...workerOwnershipReceipt) workerOwnershipPolicy {
	return workerOwnershipPolicy{
		requireOwned: true, parents: append([]workerOwnershipReceipt(nil), parents...),
	}
}

// workerAcquired prevents a raw Factory result from crossing into production
// composition without an ownership receipt. The receipt names either resources
// registered by this step or explicit parent lifetimes that the result borrows.
type workerAcquired[T any] struct {
	value     T
	ownership workerOwnershipReceipt
}

func newWorkerAcquisitionGuard(
	ctx context.Context,
	closeTimeout time.Duration,
) (*workerAcquisitionGuard, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errWorkerAcquisitionInvalid
	}
	if closeTimeout <= 0 {
		closeTimeout = defaultWorkerResourceCloseTimeout
	}
	guard := &workerAcquisitionGuard{
		ctx: ctx, closeTimeout: closeTimeout, state: workerAcquisitionCollecting,
		receipts: make(map[uint64]*workerOwnershipToken),
	}
	stopAbort := context.AfterFunc(ctx, guard.abort)
	// AfterFunc may start immediately if cancellation races construction. Both
	// publishing and consuming its stop handle therefore use the Guard mutex.
	guard.mu.Lock()
	if guard.state == workerAcquisitionCollecting {
		guard.stopAbort = stopAbort
	} else {
		stopAbort = nil
	}
	guard.mu.Unlock()
	if stopAbort == nil {
		// The callback already won and moved the Guard to aborted.
		return guard, nil
	}
	return guard, nil
}

func (guard *workerAcquisitionGuard) beginStep() (*workerStepRegistrar, error) {
	if guard == nil {
		return nil, errWorkerAcquisitionInvalid
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.ctx.Err() != nil || guard.state != workerAcquisitionCollecting || guard.activeStep != 0 {
		return nil, errWorkerAcquisitionClosed
	}
	guard.nextStep++
	guard.activeStep = guard.nextStep
	guard.stepStart = len(guard.resources)
	guard.stepPoisoned = false
	return &workerStepRegistrar{guard: guard, step: guard.activeStep}, nil
}

// Own consumes ownership at method entry. If the Registrar is late, closed or
// over capacity, the resource is immediately handed to its own bounded reaper;
// returning an error never hands ownership back to the Factory.
func (registrar *workerStepRegistrar) Own(resource WorkerResourceCloser) error {
	if registrar == nil || registrar.guard == nil {
		beginLateWorkerResourceClose(resource, defaultWorkerResourceCloseTimeout)
		return errWorkerAcquisitionClosed
	}
	guard := registrar.guard
	if dependencyMissing(resource) {
		guard.poisonStep(registrar.step)
		return errWorkerResourcesInvalid
	}

	guard.mu.Lock()
	if guard.state != workerAcquisitionCollecting || guard.activeStep != registrar.step ||
		guard.ctx.Err() != nil {
		guard.mu.Unlock()
		beginLateWorkerResourceClose(resource, guard.closeTimeout)
		return errWorkerAcquisitionClosed
	}
	if len(guard.resources) >= maxWorkerCompositionResources {
		guard.stepPoisoned = true
		guard.mu.Unlock()
		beginLateWorkerResourceClose(resource, guard.closeTimeout)
		return errWorkerResourcesInvalid
	}
	guard.resources = append(guard.resources, resource)
	guard.mu.Unlock()
	return nil
}

func (guard *workerAcquisitionGuard) poisonStep(step uint64) {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	if guard.state == workerAcquisitionCollecting && guard.activeStep == step {
		guard.stepPoisoned = true
	}
	guard.mu.Unlock()
}

func (guard *workerAcquisitionGuard) completeStep(
	registrar *workerStepRegistrar,
	policy workerOwnershipPolicy,
) (workerOwnershipReceipt, error) {
	if guard == nil || registrar == nil || registrar.guard != guard {
		return workerOwnershipReceipt{}, errWorkerOwnershipUntrusted
	}
	guard.mu.Lock()
	if guard.state != workerAcquisitionCollecting || guard.ctx.Err() != nil ||
		guard.activeStep != registrar.step || guard.stepPoisoned {
		guard.mu.Unlock()
		guard.abort()
		return workerOwnershipReceipt{}, errWorkerAcquisitionClosed
	}
	owned := len(guard.resources) - guard.stepStart
	if (policy.requireOwned && owned == 0) || (policy.forbidOwned && owned != 0) ||
		(!policy.requireOwned && owned == 0 && len(policy.parents) == 0) {
		guard.mu.Unlock()
		guard.abort()
		return workerOwnershipReceipt{}, errWorkerOwnershipRequired
	}
	for _, parent := range policy.parents {
		if !guard.validReceiptLocked(parent) {
			guard.mu.Unlock()
			guard.abort()
			return workerOwnershipReceipt{}, errWorkerOwnershipUntrusted
		}
	}
	token := &workerOwnershipToken{marker: 1}
	guard.receipts[registrar.step] = token
	guard.activeStep = 0
	guard.stepStart = 0
	guard.stepPoisoned = false
	receipt := workerOwnershipReceipt{guard: guard, step: registrar.step, token: token}
	guard.mu.Unlock()
	return receipt, nil
}

func (guard *workerAcquisitionGuard) validReceiptLocked(receipt workerOwnershipReceipt) bool {
	return receipt.guard == guard && receipt.step != 0 && receipt.token != nil && receipt.token.marker == 1 &&
		guard.receipts[receipt.step] == receipt.token
}

// acquireWorkerDependency is the only supported Factory call wrapper. It
// contains panic/error/typed-nil validation and returns only a value paired
// with a Guard-issued ownership receipt. Factory error text never crosses this
// boundary because it may contain credentials, topology or provider payloads.
func acquireWorkerDependency[T any](
	guard *workerAcquisitionGuard,
	policy workerOwnershipPolicy,
	invoke func(workerResourceRegistrar) (T, error),
	validate func(T) bool,
) (workerAcquired[T], error) {
	if invoke == nil {
		return workerAcquired[T]{}, errWorkerAcquisitionInvalid
	}
	return acquireWorkerDependencyStep(
		guard,
		func(registrar workerResourceRegistrar) (T, workerOwnershipPolicy, error) {
			value, err := invoke(registrar)
			return value, policy, err
		},
		validate,
	)
}

func acquireWorkerFactoryDependency[T any](
	guard *workerAcquisitionGuard,
	parents []workerOwnershipReceipt,
	invoke func(workerResourceRegistrar) (T, workerFactoryOwnership, error),
	validate func(T) bool,
) (workerAcquired[T], error) {
	if invoke == nil {
		return workerAcquired[T]{}, errWorkerAcquisitionInvalid
	}
	return acquireWorkerDependencyStep(
		guard,
		func(registrar workerResourceRegistrar) (T, workerOwnershipPolicy, error) {
			value, declaration, err := invoke(registrar)
			if err != nil {
				return value, workerOwnershipPolicy{}, err
			}
			policy, ok := workerFactoryOwnershipPolicy(declaration, parents)
			if !ok {
				return value, workerOwnershipPolicy{}, errWorkerOwnershipRequired
			}
			return value, policy, nil
		},
		validate,
	)
}

func workerFactoryOwnershipPolicy(
	declaration workerFactoryOwnership,
	parents []workerOwnershipReceipt,
) (workerOwnershipPolicy, bool) {
	switch declaration {
	case workerFactoryRegisteredResources:
		return workerOwnsWithParents(parents...), true
	case workerFactoryBorrowedOnly:
		if len(parents) == 0 {
			return workerOwnershipPolicy{}, false
		}
		return workerBorrowsFrom(parents...), true
	default:
		return workerOwnershipPolicy{}, false
	}
}

func acquireWorkerDependencyStep[T any](
	guard *workerAcquisitionGuard,
	invoke func(workerResourceRegistrar) (T, workerOwnershipPolicy, error),
	validate func(T) bool,
) (acquired workerAcquired[T], resultErr error) {
	if guard == nil || invoke == nil || validate == nil {
		return workerAcquired[T]{}, errWorkerAcquisitionInvalid
	}
	registrar, err := guard.beginStep()
	if err != nil {
		return workerAcquired[T]{}, err
	}
	validResult := false
	defer func() {
		if recover() != nil || resultErr != nil || !validResult {
			guard.abort()
			acquired = workerAcquired[T]{}
			if resultErr == nil {
				resultErr = errWorkerAcquisitionFailed
			}
		}
	}()

	value, policy, factoryErr := invoke(registrar)
	if factoryErr != nil || dependencyMissing(any(value)) || !validate(value) || guard.ctx.Err() != nil {
		return workerAcquired[T]{}, errWorkerAcquisitionFailed
	}
	receipt, err := guard.completeStep(registrar, policy)
	if err != nil {
		return workerAcquired[T]{}, err
	}
	validResult = true
	return workerAcquired[T]{value: value, ownership: receipt}, nil
}

func (guard *workerAcquisitionGuard) seal() (*workerResourceStack, error) {
	if guard == nil {
		return nil, errWorkerAcquisitionInvalid
	}
	guard.mu.Lock()
	if guard.state != workerAcquisitionCollecting || guard.activeStep != 0 ||
		guard.ctx.Err() != nil || len(guard.receipts) == 0 {
		guard.mu.Unlock()
		guard.abort()
		return nil, errWorkerAcquisitionClosed
	}
	owner, err := newWorkerResourceStack(guard.resources)
	if err != nil {
		guard.mu.Unlock()
		guard.abort()
		return nil, errWorkerAcquisitionFailed
	}
	guard.resources = nil
	guard.owner = owner
	guard.state = workerAcquisitionSealed
	guard.mu.Unlock()
	return owner, nil
}

// commit is the Builder's final operation. It proves exact Compose retained
// the same owner and structural/runtime seals produced under this Guard, then
// atomically publishes the private ownership-transfer proof and transfers
// cleanup responsibility once. No Compose entry point can mint this proof.
func (guard *workerAcquisitionGuard) commit(
	composition *WorkerComposition,
) (*WorkerComposition, error) {
	if guard == nil {
		return nil, errWorkerAcquisitionTransfer
	}
	guard.mu.Lock()
	if guard.state != workerAcquisitionSealed || guard.ctx.Err() != nil ||
		guard.owner == nil || !guard.owner.isOpen() || guard.transfer != nil ||
		guard.stopAbort == nil ||
		composition == nil || composition.resources != guard.owner ||
		composition.seal == nil || composition.probe == nil || composition.runtimeScope == nil ||
		composition.ownershipTransfer != nil || composition.seal.ownershipTransfer != nil ||
		!composition.seal.matches(composition) ||
		!composition.runtimeScope.intact(
			composition.store, composition.worker, composition.reconciler, composition.dispatcher,
		) {
		if guard.state != workerAcquisitionTransferred {
			closeWorkerCompositionAdmission(composition)
		}
		guard.mu.Unlock()
		guard.abort()
		return nil, errWorkerAcquisitionTransfer
	}
	compositeProbe, compositeOK := composition.probe.delegate.(*workerCompositeRuntimeProbe)
	expectedProbeCount := workerCoreProbeCount + len(composition.runtimeScope.plugins) + len(composition.runtimeScope.topics)
	if !compositeOK || !compositeProbe.intact(expectedProbeCount) {
		guard.mu.Unlock()
		guard.abort()
		return nil, errWorkerAcquisitionTransfer
	}
	// Stopping the cancellation callback is the transfer's linearization point.
	// A false result means cancellation already started and its callback may be
	// waiting on this mutex. A second context check is still required because
	// cancellation can win immediately before a successful Stop without the
	// callback having started. If cancellation happens after that check, commit
	// won the handoff and the returned Composition owns normal shutdown.
	stopAbort := guard.stopAbort
	stopped := stopAbort()
	guard.stopAbort = nil
	if !stopped || guard.ctx.Err() != nil {
		guard.mu.Unlock()
		guard.abort()
		return nil, errWorkerAcquisitionTransfer
	}
	transfer := &workerOwnershipTransferSeal{
		marker: 1, guard: guard, composition: composition, compositionSeal: composition.seal,
		runtimeScope: composition.runtimeScope, compositeProbe: compositeProbe,
		compositeProbeCount: expectedProbeCount, resources: guard.owner,
	}
	// Bind both objects before publishing the transferred state. A readiness
	// reader can only see either an uncommitted candidate or a mutually linked
	// transfer proof; a partially linked token cannot pass either seal check.
	composition.ownershipTransfer = transfer
	composition.seal.ownershipTransfer = transfer
	guard.transfer = transfer
	guard.state = workerAcquisitionTransferred
	guard.mu.Unlock()
	return composition, nil
}

func (guard *workerAcquisitionGuard) abort() {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	if guard.state == workerAcquisitionTransferred || guard.state == workerAcquisitionAborted {
		guard.mu.Unlock()
		return
	}
	var owner *workerResourceStack
	if guard.owner != nil {
		owner = guard.owner
	} else {
		owner, _ = newWorkerResourceStack(guard.resources)
	}
	guard.resources = nil
	guard.owner = owner
	guard.activeStep = 0
	guard.state = workerAcquisitionAborted
	stopAbort := guard.stopAbort
	guard.stopAbort = nil
	guard.mu.Unlock()
	if stopAbort != nil {
		_ = stopAbort()
	}
	if owner != nil {
		owner.beginClose(guard.closeTimeout)
	}
}

func beginLateWorkerResourceClose(resource WorkerResourceCloser, timeout time.Duration) {
	if dependencyMissing(resource) {
		return
	}
	owner, _ := newWorkerResourceStack([]WorkerResourceCloser{resource})
	if owner != nil {
		owner.beginClose(timeout)
	}
}

var _ workerResourceRegistrar = (*workerStepRegistrar)(nil)
