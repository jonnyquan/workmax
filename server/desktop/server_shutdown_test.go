//go:build desktop

package desktop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

type shutdownLoginCoordinator struct {
	startCalls    atomic.Int32
	completeCalls atomic.Int32
	cancelCalls   atomic.Int32
	cancelEntered chan struct{}
	cancelRelease chan struct{}
}

func (c *shutdownLoginCoordinator) StartPassword(
	context.Context,
	string,
	string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	c.startCalls.Add(1)
	return cloudproxy.LoginTransactionCoordinatorSnapshot{
		State: cloudproxy.LoginTransactionCoordinatorStatePending,
	}, nil
}

func (c *shutdownLoginCoordinator) CompletePassword(
	context.Context,
	string,
	string,
	string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	c.completeCalls.Add(1)
	return cloudproxy.LoginTransactionCoordinatorSnapshot{
		State: cloudproxy.LoginTransactionCoordinatorStateComplete,
	}, nil
}

func (c *shutdownLoginCoordinator) Snapshot() cloudproxy.LoginTransactionCoordinatorSnapshot {
	return cloudproxy.LoginTransactionCoordinatorSnapshot{
		State: cloudproxy.LoginTransactionCoordinatorStateIdle,
	}
}

func (c *shutdownLoginCoordinator) CancelFlow(
	string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	return c.Snapshot(), nil
}

func (c *shutdownLoginCoordinator) Cancel() {
	c.cancelCalls.Add(1)
	if c.cancelEntered != nil {
		select {
		case <-c.cancelEntered:
		default:
			close(c.cancelEntered)
		}
	}
	if c.cancelRelease != nil {
		<-c.cancelRelease
	}
}

func TestAuthOperationContextSidecarCancellationIsSynchronous(t *testing.T) {
	authContext, authCancel := context.WithCancel(context.Background())
	s := &Server{authContext: authContext, authCancel: authCancel}
	requestContext, requestCancel := context.WithCancel(context.Background())
	operationContext, cleanup := s.authOperationContext(requestContext)
	defer cleanup()

	authCancel()
	if !errors.Is(operationContext.Err(), context.Canceled) {
		t.Fatalf("operation context error = %v, want synchronous context.Canceled", operationContext.Err())
	}

	// The secondary request source must remain connected as well.
	authContext2, authCancel2 := context.WithCancel(context.Background())
	defer authCancel2()
	s.authContext = authContext2
	operationContext2, cleanup2 := s.authOperationContext(requestContext)
	defer cleanup2()
	requestCancel()
	select {
	case <-operationContext2.Done():
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not reach auth operation context")
	}
}

func TestShutdownHonorsDeadlineWhileAuthCleanupIsBlocked(t *testing.T) {
	coordinator := &shutdownLoginCoordinator{
		cancelEntered: make(chan struct{}),
		cancelRelease: make(chan struct{}),
	}
	defer close(coordinator.cancelRelease)
	authContext, authCancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:         ServerConfig{LoginCoordinator: coordinator},
		httpServer:  &http.Server{},
		authContext: authContext,
		authCancel:  authCancel,
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := s.Shutdown(shutdownContext)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Shutdown exceeded caller deadline by too much: %s", elapsed)
	}
	select {
	case <-coordinator.cancelEntered:
	default:
		t.Fatal("Shutdown did not start auth cleanup")
	}
	if !s.authClosing.Load() || !errors.Is(authContext.Err(), context.Canceled) {
		t.Fatal("Shutdown did not close auth admission and cancel its lifetime")
	}
}

func TestAuthMutationHandlersRejectAfterShutdownAdmissionCloses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	coordinator := &shutdownLoginCoordinator{}
	s := &Server{cfg: ServerConfig{LoginCoordinator: coordinator}}
	s.authClosing.Store(true)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		handle func(*gin.Context)
	}{
		{
			name:   "begin",
			method: http.MethodPost,
			path:   "/auth/login-transaction",
			handle: s.handleLoginTransactionBegin,
		},
		{
			name:   "password",
			method: http.MethodPost,
			path:   "/auth/login-transaction/password",
			body:   `{"email":"person@example.com","password":"must-not-be-processed"}`,
			handle: s.handleLoginTransactionPassword,
		},
		{
			name:   "cancel",
			method: http.MethodDelete,
			path:   "/auth/login-transaction",
			handle: s.handleLoginTransactionCancel,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			test.handle(ctx)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"error":"unavailable"`) {
				t.Fatalf("closed response = %s", recorder.Body.String())
			}
		})
	}
	if coordinator.startCalls.Load() != 0 || coordinator.completeCalls.Load() != 0 {
		t.Fatalf(
			"closed handlers reached coordinator: start=%d complete=%d",
			coordinator.startCalls.Load(),
			coordinator.completeCalls.Load(),
		)
	}
}
