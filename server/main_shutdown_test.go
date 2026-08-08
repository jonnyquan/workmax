package main

import (
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRunGracefulShutdownWaitsForCronBeforeRemainingCleanupAndExit(t *testing.T) {
	signals := make(chan os.Signal, 1)
	cronEntered := make(chan struct{})
	releaseCron := make(chan struct{})
	exited := make(chan struct{})

	var mu sync.Mutex
	order := make([]string, 0, 3)
	record := func(value string) {
		mu.Lock()
		order = append(order, value)
		mu.Unlock()
	}
	// Mirrors a SIGTERM arriving while CronInitV2 is still building its graph:
	// signal.Notify has already buffered it, but the consumer is installed only
	// after the runtime handle is ready.
	signals <- syscall.SIGTERM
	done := runGracefulShutdown(signals, gracefulShutdownHooks{
		cleanup: []func(){
			func() {
				record("cron")
				close(cronEntered)
				<-releaseCron
			},
			func() { record("workers") },
		},
		exit: func(code int) {
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			record("exit")
			close(exited)
		},
	})

	select {
	case <-cronEntered:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not invoke cron Stop")
	}
	select {
	case <-exited:
		t.Fatal("shutdown exited before cron Stop completed")
	default:
	}

	close(releaseCron)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete after cron Stop returned")
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"cron", "workers", "exit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown order = %v, want %v", got, want)
	}
}
