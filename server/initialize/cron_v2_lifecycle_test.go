package initialize

import (
	"sync"
	"testing"
	"time"

	"server/globals"
)

func TestCronRuntimeStopIsConcurrentIdempotentAndWaits(t *testing.T) {
	runtime := newCronRuntime()
	stopObserved := make(chan struct{})
	releaseShutdown := make(chan struct{})
	go func() {
		<-runtime.stopChan
		close(stopObserved)
		<-releaseShutdown
		close(runtime.doneChan)
	}()

	const callers = 16
	var stops sync.WaitGroup
	returned := make(chan struct{}, callers)
	for index := 0; index < callers; index++ {
		stops.Add(1)
		go func() {
			defer stops.Done()
			runtime.Stop()
			returned <- struct{}{}
		}()
	}

	select {
	case <-stopObserved:
	case <-time.After(time.Second):
		t.Fatal("runtime Stop did not close its stop signal")
	}
	select {
	case <-returned:
		t.Fatal("runtime Stop returned before scheduler shutdown completed")
	default:
	}

	close(releaseShutdown)
	allReturned := make(chan struct{})
	go func() {
		stops.Wait()
		close(allReturned)
	}()
	select {
	case <-allReturned:
	case <-time.After(time.Second):
		t.Fatal("concurrent runtime Stop calls deadlocked")
	}

	stopReturned := make(chan struct{})
	go func() {
		runtime.Stop()
		close(stopReturned)
	}()
	select {
	case <-stopReturned:
	case <-time.After(time.Second):
		t.Fatal("repeated runtime Stop did not return")
	}
}

func TestCronInitV2DisabledReturnsCompletedRuntime(t *testing.T) {
	previous := globals.GraConf.System.Cron.Enable
	globals.GraConf.System.Cron.Enable = false
	t.Cleanup(func() { globals.GraConf.System.Cron.Enable = previous })

	runtime := CronInitV2()
	stopReturned := make(chan struct{})
	go func() {
		runtime.Stop()
		close(stopReturned)
	}()
	select {
	case <-stopReturned:
	case <-time.After(time.Second):
		t.Fatal("disabled cron runtime Stop blocked")
	}
}
