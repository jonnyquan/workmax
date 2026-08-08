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
	defaultArtifactStaticRenderInterval = 10 * time.Second
	defaultArtifactStaticRenderBatch    = 5
)

type ArtifactStaticRenderRunner struct {
	worker    *ArtifactStaticRenderWorker
	interval  time.Duration
	batch     int
	isRunning atomic.Bool
	stopChan  chan struct{}
	doneChan  chan struct{}
	stopOnce  sync.Once
}

func NewArtifactStaticRenderRunner(worker *ArtifactStaticRenderWorker, interval time.Duration, batch int) *ArtifactStaticRenderRunner {
	if interval <= 0 {
		interval = defaultArtifactStaticRenderInterval
	}
	if batch <= 0 {
		batch = defaultArtifactStaticRenderBatch
	}
	return &ArtifactStaticRenderRunner{
		worker:   worker,
		interval: interval,
		batch:    batch,
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
}

func (r *ArtifactStaticRenderRunner) Start() error {
	if r == nil || r.worker == nil {
		return fmt.Errorf("artifact static render runner: worker is required")
	}
	if !r.isRunning.CompareAndSwap(false, true) {
		return nil
	}
	go r.run()
	return nil
}

func (r *ArtifactStaticRenderRunner) Stop() {
	if r == nil || !r.isRunning.CompareAndSwap(true, false) {
		return
	}
	r.stopOnce.Do(func() { close(r.stopChan) })
	<-r.doneChan
}

func (r *ArtifactStaticRenderRunner) run() {
	defer close(r.doneChan)
	globals.Info("[WorkAgent] artifact static render runner started")
	defer globals.Info("[WorkAgent] artifact static render runner stopped")

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

func (r *ArtifactStaticRenderRunner) runDrainGuarded() {
	defer func() {
		if v := recover(); v != nil {
			globals.Error(fmt.Sprintf("[WorkAgent] artifact static render runner panic recovered: %v", v))
			time.Sleep(time.Second)
		}
	}()
	r.drainOnce(context.Background())
}

func (r *ArtifactStaticRenderRunner) drainOnce(ctx context.Context) int {
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
			globals.Error(fmt.Sprintf("[WorkAgent] artifact static render job failed before status update: %v", err))
			return processed
		}
		if result == nil || !result.Claimed {
			return processed
		}
		processed++
		if result.Job != nil {
			globals.Info(fmt.Sprintf("[WorkAgent] artifact static render job processed id=%d status=%s target=%s", result.Job.Id, result.Job.Status, result.Job.Target))
		}
	}
	return processed
}

var (
	defaultArtifactStaticRenderRunnerMu sync.Mutex
	defaultArtifactStaticRenderRunner   *ArtifactStaticRenderRunner
	defaultArtifactStaticRenderStatus   artifactRenderRunnerStatusStore
)

func StartDefaultArtifactStaticRenderRunner() error {
	defaultArtifactStaticRenderRunnerMu.Lock()
	defer defaultArtifactStaticRenderRunnerMu.Unlock()
	if defaultArtifactStaticRenderRunner != nil && defaultArtifactStaticRenderRunner.isRunning.Load() {
		return nil
	}
	renderer, err := NewDefaultBrowserCommandStaticRenderer()
	if err != nil {
		defaultArtifactStaticRenderStatus.recordDisabled(ArtifactStaticRenderWorkerName, err)
		return err
	}
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{Renderer: renderer})
	runner := NewArtifactStaticRenderRunner(worker, defaultArtifactStaticRenderInterval, defaultArtifactStaticRenderBatch)
	if err := runner.Start(); err != nil {
		defaultArtifactStaticRenderStatus.recordDisabled(ArtifactStaticRenderWorkerName, err)
		return err
	}
	defaultArtifactStaticRenderRunner = runner
	defaultArtifactStaticRenderStatus.recordRunning(ArtifactStaticRenderWorkerName)
	return nil
}

func ShutdownDefaultArtifactStaticRenderRunner() {
	defaultArtifactStaticRenderRunnerMu.Lock()
	runner := defaultArtifactStaticRenderRunner
	defaultArtifactStaticRenderRunner = nil
	defaultArtifactStaticRenderRunnerMu.Unlock()
	if runner != nil {
		runner.Stop()
	}
	defaultArtifactStaticRenderStatus.recordStopped(ArtifactStaticRenderWorkerName)
}

func DefaultArtifactStaticRenderRunnerStatus() ArtifactRenderRunnerStatus {
	status := defaultArtifactStaticRenderStatus.snapshot()
	if status.Worker == "" {
		status.Worker = ArtifactStaticRenderWorkerName
	}
	return status
}
