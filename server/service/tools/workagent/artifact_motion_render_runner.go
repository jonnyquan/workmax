package workagent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"server/globals"
)

const (
	defaultArtifactMotionRenderInterval = 10 * time.Second
	defaultArtifactMotionRenderBatch    = 2
)

type ArtifactMotionRenderRunner struct {
	worker    *ArtifactMotionRenderWorker
	interval  time.Duration
	batch     int
	isRunning atomic.Bool
	stopChan  chan struct{}
	doneChan  chan struct{}
	stopOnce  sync.Once
}

func NewArtifactMotionRenderRunner(worker *ArtifactMotionRenderWorker, interval time.Duration, batch int) *ArtifactMotionRenderRunner {
	if interval <= 0 {
		interval = defaultArtifactMotionRenderInterval
	}
	if batch <= 0 {
		batch = defaultArtifactMotionRenderBatch
	}
	return &ArtifactMotionRenderRunner{
		worker:   worker,
		interval: interval,
		batch:    batch,
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
}

func (r *ArtifactMotionRenderRunner) Start() error {
	if r == nil || r.worker == nil {
		return fmt.Errorf("artifact motion render runner: worker is required")
	}
	if !r.isRunning.CompareAndSwap(false, true) {
		return nil
	}
	go r.run()
	return nil
}

func (r *ArtifactMotionRenderRunner) Stop() {
	if r == nil || !r.isRunning.CompareAndSwap(true, false) {
		return
	}
	r.stopOnce.Do(func() { close(r.stopChan) })
	<-r.doneChan
}

func (r *ArtifactMotionRenderRunner) run() {
	defer close(r.doneChan)
	globals.Info("[WorkAgent] artifact motion render runner started")
	defer globals.Info("[WorkAgent] artifact motion render runner stopped")

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.runDrainGuarded()

	for {
		select {
		case <-ticker.C:
			r.runDrainGuarded()
		case <-r.stopChan:
			return
		}
	}
}

func (r *ArtifactMotionRenderRunner) runDrainGuarded() {
	defer func() {
		if v := recover(); v != nil {
			globals.Error(fmt.Sprintf("[WorkAgent] artifact motion render runner panic recovered: %v", v))
			time.Sleep(time.Second)
		}
	}()
	r.drainOnce(context.Background())
}

func (r *ArtifactMotionRenderRunner) drainOnce(ctx context.Context) int {
	if r == nil || r.worker == nil {
		return 0
	}
	processed := 0
	for processed < r.batch {
		select {
		case <-ctx.Done():
			return processed
		case <-r.stopChan:
			return processed
		default:
		}
		result, err := r.worker.RunNext(ctx)
		if err != nil {
			globals.Error(fmt.Sprintf("[WorkAgent] artifact motion render job failed before status update: %v", err))
			return processed
		}
		if result == nil || !result.Claimed {
			return processed
		}
		processed++
		if result.Job != nil {
			globals.Info(fmt.Sprintf("[WorkAgent] artifact motion render job processed id=%d status=%s target=%s", result.Job.Id, result.Job.Status, result.Job.Target))
		}
	}
	return processed
}

var (
	defaultArtifactMotionRenderRunnerMu sync.Mutex
	defaultArtifactMotionRenderRunner   *ArtifactMotionRenderRunner
	defaultArtifactMotionRenderStatus   artifactRenderRunnerStatusStore
)

func StartDefaultArtifactMotionRenderRunner() error {
	defaultArtifactMotionRenderRunnerMu.Lock()
	defer defaultArtifactMotionRenderRunnerMu.Unlock()
	if defaultArtifactMotionRenderRunner != nil && defaultArtifactMotionRenderRunner.isRunning.Load() {
		return nil
	}
	renderer, err := NewDefaultBrowserCommandMotionRenderer()
	if err != nil {
		defaultArtifactMotionRenderStatus.recordDisabled(ArtifactMotionRenderWorkerName, err)
		return err
	}
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{Renderer: renderer})
	runner := NewArtifactMotionRenderRunner(worker, defaultArtifactMotionRenderInterval, defaultArtifactMotionRenderBatch)
	if err := runner.Start(); err != nil {
		defaultArtifactMotionRenderStatus.recordDisabled(ArtifactMotionRenderWorkerName, err)
		return err
	}
	defaultArtifactMotionRenderRunner = runner
	defaultArtifactMotionRenderStatus.recordRunning(ArtifactMotionRenderWorkerName)
	return nil
}

func ShutdownDefaultArtifactMotionRenderRunner() {
	defaultArtifactMotionRenderRunnerMu.Lock()
	runner := defaultArtifactMotionRenderRunner
	defaultArtifactMotionRenderRunner = nil
	defaultArtifactMotionRenderRunnerMu.Unlock()
	if runner != nil {
		runner.Stop()
	}
	defaultArtifactMotionRenderStatus.recordStopped(ArtifactMotionRenderWorkerName)
}

func DefaultArtifactMotionRenderRunnerStatus() ArtifactRenderRunnerStatus {
	status := defaultArtifactMotionRenderStatus.snapshot()
	if status.Worker == "" {
		status.Worker = ArtifactMotionRenderWorkerName
	}
	return status
}
