package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type acquisitionTestDependency struct{ name string }

func validAcquisitionTestDependency(value *acquisitionTestDependency) bool {
	return value != nil && value.name != ""
}

func TestWorkerAcquisitionCancellationReapsARegisteredResourceBeforeFactoryReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	registered := make(chan struct{})
	closed := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	var closeCalls atomic.Int32
	go func() {
		_, acquireErr := acquireWorkerDependency(guard, workerOwnsResource(),
			func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
				if ownErr := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
					if closeCalls.Add(1) == 1 {
						close(closed)
					}
					return nil
				})); ownErr != nil {
					return nil, ownErr
				}
				close(registered)
				<-release // deliberately ignores ctx after registering ownership
				return &acquisitionTestDependency{name: "database"}, nil
			}, validAcquisitionTestDependency)
		result <- acquireErr
	}()
	<-registered
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reap a partially acquired resource")
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("close calls before Factory return = %d, want 1", closeCalls.Load())
	}
	close(release)
	if acquireErr := <-result; acquireErr == nil {
		t.Fatal("canceled acquisition succeeded after its Factory returned")
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("close calls after late Factory return = %d, want 1", closeCalls.Load())
	}
}

func TestWorkerAcquisitionSanitizesFactoryFailurePanicAndTypedNil(t *testing.T) {
	for name, invoke := range map[string]func(workerResourceRegistrar) (*acquisitionTestDependency, error){
		"error": func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			_ = registrar.Own(WorkerResourceCloseFunc(func(context.Context) error { return nil }))
			return &acquisitionTestDependency{name: "provider"}, errors.New("SECRET_PROVIDER_BODY")
		},
		"panic": func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			_ = registrar.Own(WorkerResourceCloseFunc(func(context.Context) error { return nil }))
			panic("SECRET_PROVIDER_PANIC")
		},
		"typed nil": func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			_ = registrar.Own(WorkerResourceCloseFunc(func(context.Context) error { return nil }))
			var result *acquisitionTestDependency
			return result, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			wrapped := func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
				return invoke(countingRegistrar{delegate: registrar, calls: &calls})
			}
			_, acquireErr := acquireWorkerDependency(
				guard, workerOwnsResource(), wrapped, validAcquisitionTestDependency,
			)
			if !errors.Is(acquireErr, errWorkerAcquisitionFailed) {
				t.Fatalf("acquisition error = %v, want stable failure", acquireErr)
			}
			if strings.Contains(acquireErr.Error(), "SECRET_") {
				t.Fatalf("acquisition exposed Factory detail: %q", acquireErr)
			}
			if calls.Load() != 1 {
				t.Fatalf("Own calls = %d, want 1", calls.Load())
			}
		})
	}
}

type countingRegistrar struct {
	delegate workerResourceRegistrar
	calls    *atomic.Int32
}

func (registrar countingRegistrar) Own(resource WorkerResourceCloser) error {
	registrar.calls.Add(1)
	return registrar.delegate.Own(resource)
}

func TestWorkerAcquisitionRequiresOwnedOrTrustedBorrowedLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkerDependency(guard, workerOwnsResource(),
		func(workerResourceRegistrar) (*acquisitionTestDependency, error) {
			return &acquisitionTestDependency{name: "unowned database"}, nil
		}, validAcquisitionTestDependency); !errors.Is(err, errWorkerOwnershipRequired) {
		t.Fatalf("unowned root error = %v, want ownership required", err)
	}

	guard, err = newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	root, err := acquireWorkerDependency(guard, workerOwnsResource(),
		func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			if ownErr := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error { return nil })); ownErr != nil {
				return nil, ownErr
			}
			return &acquisitionTestDependency{name: "database"}, nil
		}, validAcquisitionTestDependency)
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := acquireWorkerDependency(guard, workerBorrowsFrom(root.ownership),
		func(workerResourceRegistrar) (*acquisitionTestDependency, error) {
			return &acquisitionTestDependency{name: "sql store"}, nil
		}, validAcquisitionTestDependency)
	if err != nil || borrowed.value.name != "sql store" {
		t.Fatalf("borrowed dependency = %+v, %v", borrowed.value, err)
	}
	guard.abort()

	other, err := newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkerDependency(other, workerBorrowsFrom(root.ownership),
		func(workerResourceRegistrar) (*acquisitionTestDependency, error) {
			return &acquisitionTestDependency{name: "forged borrow"}, nil
		}, validAcquisitionTestDependency); !errors.Is(err, errWorkerOwnershipUntrusted) {
		t.Fatalf("cross-Guard receipt error = %v, want invalid receipt", err)
	}
}

func TestWorkerFactoryAcquisitionRejectsRegisteredDeclarationWithoutOwn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	_, err = acquireWorkerFactoryDependency(
		guard,
		nil,
		func(workerResourceRegistrar) (*acquisitionTestDependency, workerFactoryOwnership, error) {
			return &acquisitionTestDependency{name: "unregistered root"}, workerFactoryRegisteredResources, nil
		},
		validAcquisitionTestDependency,
	)
	if !errors.Is(err, errWorkerOwnershipRequired) {
		t.Fatalf("registered-without-Own error = %v, want ownership required", err)
	}
}

func TestWorkerFactoryAcquisitionRejectsBorrowedDeclarationThatCallsOwnAndReapsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var rootCloses, unexpectedCloses atomic.Int32
	root, err := acquireWorkerDependency(
		guard,
		workerOwnsResource(),
		func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			if ownErr := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
				rootCloses.Add(1)
				return nil
			})); ownErr != nil {
				return nil, ownErr
			}
			return &acquisitionTestDependency{name: "root"}, nil
		},
		validAcquisitionTestDependency,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = acquireWorkerFactoryDependency(
		guard,
		[]workerOwnershipReceipt{root.ownership},
		func(registrar workerResourceRegistrar) (*acquisitionTestDependency, workerFactoryOwnership, error) {
			if ownErr := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
				unexpectedCloses.Add(1)
				return nil
			})); ownErr != nil {
				return nil, workerFactoryBorrowedOnly, ownErr
			}
			return &acquisitionTestDependency{name: "dishonest borrower"}, workerFactoryBorrowedOnly, nil
		},
		validAcquisitionTestDependency,
	)
	if !errors.Is(err, errWorkerOwnershipRequired) {
		t.Fatalf("borrowed-with-Own error = %v, want ownership required", err)
	}
	waitForAtomicValue(t, &rootCloses, 1)
	waitForAtomicValue(t, &unexpectedCloses, 1)
}

func TestWorkerFactoryAcquisitionRejectsUnknownOwnershipDeclaration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var rootCloses atomic.Int32
	root, err := acquireWorkerDependency(
		guard,
		workerOwnsResource(),
		func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			if ownErr := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
				rootCloses.Add(1)
				return nil
			})); ownErr != nil {
				return nil, ownErr
			}
			return &acquisitionTestDependency{name: "root"}, nil
		},
		validAcquisitionTestDependency,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = acquireWorkerFactoryDependency(
		guard,
		[]workerOwnershipReceipt{root.ownership},
		func(workerResourceRegistrar) (*acquisitionTestDependency, workerFactoryOwnership, error) {
			return &acquisitionTestDependency{name: "unknown"}, workerFactoryOwnership(255), nil
		},
		validAcquisitionTestDependency,
	)
	if !errors.Is(err, errWorkerAcquisitionFailed) {
		t.Fatalf("unknown declaration error = %v, want stable acquisition failure", err)
	}
	waitForAtomicValue(t, &rootCloses, 1)
}

func TestWorkerAcquisitionLateOwnIsClosedAndCannotChangeSealedOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var captured workerResourceRegistrar
	var originalCalls, lateCalls atomic.Int32
	_, err = acquireWorkerDependency(guard, workerOwnsResource(),
		func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			captured = registrar
			if ownErr := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
				originalCalls.Add(1)
				return nil
			})); ownErr != nil {
				return nil, ownErr
			}
			return &acquisitionTestDependency{name: "database"}, nil
		}, validAcquisitionTestDependency)
	if err != nil {
		t.Fatal(err)
	}
	if ownErr := captured.Own(WorkerResourceCloseFunc(func(context.Context) error {
		lateCalls.Add(1)
		return nil
	})); !errors.Is(ownErr, errWorkerAcquisitionClosed) {
		t.Fatalf("late Own error = %v, want closed", ownErr)
	}
	waitForAtomicValue(t, &lateCalls, 1)
	if originalCalls.Load() != 0 {
		t.Fatal("late Own closed the accepted resource")
	}
	owner, err := guard.seal()
	if err != nil {
		t.Fatal(err)
	}
	guard.abort()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if closeErr := owner.Close(closeCtx); closeErr != nil {
		t.Fatal(closeErr)
	}
	if originalCalls.Load() != 1 || lateCalls.Load() != 1 {
		t.Fatalf("close calls original=%d late=%d, want 1/1", originalCalls.Load(), lateCalls.Load())
	}
}

func TestWorkerAcquisitionResourceLimitReapsEveryAcceptedAndOverflowResource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newWorkerAcquisitionGuard(ctx, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var closes atomic.Int32
	_, acquireErr := acquireWorkerDependency(guard, workerOwnsResource(),
		func(registrar workerResourceRegistrar) (*acquisitionTestDependency, error) {
			for range maxWorkerCompositionResources + 1 {
				_ = registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
					closes.Add(1)
					return nil
				}))
			}
			return &acquisitionTestDependency{name: "overflow"}, nil
		}, validAcquisitionTestDependency)
	if acquireErr == nil {
		t.Fatal("overflow acquisition succeeded")
	}
	waitForAtomicValue(t, &closes, maxWorkerCompositionResources+1)
}

func TestWorkerAcquisitionInitiallyCanceledInvokesNoFactory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if guard, err := newWorkerAcquisitionGuard(ctx, time.Second); guard != nil ||
		!errors.Is(err, errWorkerAcquisitionInvalid) {
		t.Fatalf("new guard = %p, %v; want canceled rejection", guard, err)
	}
}

func waitForAtomicValue(t *testing.T, value *atomic.Int32, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for value.Load() != int32(want) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := value.Load(); got != int32(want) {
		t.Fatalf("atomic value = %d, want %d", got, want)
	}
}

func TestWorkerAcquisitionOwnCancelRaceClosesExactlyOnce(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		guard, err := newWorkerAcquisitionGuard(ctx, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		registrar, err := guard.beginStep()
		if err != nil {
			t.Fatal(err)
		}
		var closes atomic.Int32
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_ = registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
				closes.Add(1)
				return nil
			}))
		}()
		go func() {
			defer wait.Done()
			<-start
			cancel()
		}()
		close(start)
		wait.Wait()
		guard.abort()
		waitForAtomicValue(t, &closes, 1)
	}
}
