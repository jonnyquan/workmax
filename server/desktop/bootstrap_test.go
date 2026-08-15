//go:build desktop

package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTrimmedBoot builds the minimum Boot that Shutdown can operate on. The
// production wiring is exercised end-to-end by the smoke scripts; what these
// tests pin down is the lifecycle contract, which is where a silent
// regression costs the most.
func newTrimmedBoot() (*Boot, *int) {
	ctx, cancel := context.WithCancel(context.Background())
	closes := 0
	b := &Boot{
		ctx:           ctx,
		cancel:        cancel,
		closeEmbedder: func() error { closes++; return nil },
	}
	return b, &closes
}

// Cancel runs first in every real shutdown path — a signal handler in
// --serve-only, a window-close hook under Wails. If Cancel and Shutdown share
// a sync.Once, Cancel consumes it and the teardown never runs: the ONNX
// Runtime environment is never released and the process aborts on exit with
// "mutex lock failed". That failure only reproduces with RAG enabled, so it
// survives any test run that lacks the native model.
func TestCancelDoesNotSuppressShutdownTeardown(t *testing.T) {
	b, closes := newTrimmedBoot()

	b.Cancel("signal terminated")
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after Cancel: %v", err)
	}

	if *closes != 1 {
		t.Fatalf("embedder closed %d times after Cancel+Shutdown, want exactly 1", *closes)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	b, closes := newTrimmedBoot()

	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}

	if *closes != 1 {
		t.Fatalf("embedder closed %d times across two Shutdowns, want exactly 1", *closes)
	}
}

func TestShutdownReportsEmbedderCloseFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	want := errors.New("onnxruntime went sideways")
	b := &Boot{ctx: ctx, cancel: cancel, closeEmbedder: func() error { return want }}

	err := b.Shutdown(context.Background())
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("Shutdown error = %v, want it to wrap %v", err, want)
	}
}

func TestCancelClosesDone(t *testing.T) {
	b, _ := newTrimmedBoot()
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	select {
	case <-b.Done():
		t.Fatal("Done closed before Cancel")
	default:
	}

	b.Cancel("test")
	select {
	case <-b.Done():
	default:
		t.Fatal("Done should close once Cancel has run")
	}
}

// The knowledge stack is torn down on a path the caller does not control:
// background indexing runs after a turn is answered, and shutdown can arrive
// while it is inside native code. These pin the two rules that keep that from
// crashing the process on exit — see lazyKnowledge in bootstrap_cgo.go.
func TestShutdownWaitsForCloseBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	closed := make(chan struct{})
	b := &Boot{ctx: ctx, cancel: cancel, closeEmbedder: func() error {
		<-release // stand in for draining an in-flight native call
		close(closed)
		return nil
	}}

	done := make(chan error, 1)
	go func() { done <- b.Shutdown(context.Background()) }()

	select {
	case <-done:
		t.Fatal("Shutdown returned before the embedder finished closing; a caller that exits on this would tear down native code mid-call")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("the embedder close never ran")
	}
}

// resolveAgentBinary is the seam packaged L2 runtimes are found through: env
// override, then <resources>/bin/<name>, then PATH. Each source must be able
// to win on its own, and a bad env value must fall through rather than
// silently disable the runtime the packaged binary would have served.
func TestResolveAgentBinaryPrefersEnvThenResourcesThenPath(t *testing.T) {
	dir := t.TempDir()

	// A real executable for the env override and the packaged layout.
	exe := filepath.Join(dir, "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKMAX_TEST_AGENT_BIN", exe)
	if got := resolveAgentBinary("WORKMAX_TEST_AGENT_BIN", dir, "claude"); got != exe {
		t.Fatalf("env override: got %q, want %q", got, exe)
	}

	// A directory (or any non-executable) under the env var falls through to
	// the packaged layout rather than disabling the runtime outright.
	binDir := filepath.Join(dir, "res", "bin")
	packaged := filepath.Join(binDir, "pi")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packaged, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKMAX_TEST_AGENT_BIN", filepath.Join(dir, "absent"))
	if got := resolveAgentBinary("WORKMAX_TEST_AGENT_BIN", binDir, "pi"); got != packaged {
		t.Fatalf("packaged fallback: got %q, want %q", got, packaged)
	}

	// A non-executable file in the packaged slot is not usable either; with
	// nothing on PATH under this name, the answer is "not present".
	t.Setenv("WORKMAX_TEST_AGENT_BIN", "")
	if err := os.Chmod(packaged, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveAgentBinary("WORKMAX_TEST_AGENT_BIN", binDir, "workmax-test-agent-no-such-name"); got != "" {
		t.Fatalf("nothing usable: got %q, want \"\"", got)
	}
}
