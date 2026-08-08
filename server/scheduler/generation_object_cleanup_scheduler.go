package scheduler

import (
	"context"
	"fmt"
	"time"

	"server/globals"
	toolsService "server/service/tools"
)

const (
	defaultGenerationObjectCleanupInterval  = 1 * time.Hour
	defaultGenerationObjectCleanupOlderThan = 24 * time.Hour
	defaultGenerationObjectCleanupLimit     = 100
)

// GenerationObjectCleanupScheduler cleans orphaned generated objects from object storage.
type GenerationObjectCleanupScheduler struct {
	isRunning bool
	stopChan  chan struct{}
}

func NewGenerationObjectCleanupScheduler() *GenerationObjectCleanupScheduler {
	return &GenerationObjectCleanupScheduler{
		stopChan: make(chan struct{}),
	}
}

func (s *GenerationObjectCleanupScheduler) Start() {
	if s.isRunning {
		return
	}

	s.isRunning = true
	go s.run()
}

func (s *GenerationObjectCleanupScheduler) Stop() {
	if !s.isRunning {
		return
	}

	s.isRunning = false
	s.stopChan <- struct{}{}
}

func (s *GenerationObjectCleanupScheduler) run() {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("Generation object cleanup scheduler panic recovered: %v", r))
			time.Sleep(5 * time.Second)
			if s.isRunning {
				go s.run()
			}
		}
	}()

	globals.Info("Generation object cleanup scheduler started")

	ticker := time.NewTicker(defaultGenerationObjectCleanupInterval)
	defer ticker.Stop()

	s.cleanupOrphanObjects()

	for {
		select {
		case <-ticker.C:
			s.cleanupOrphanObjects()
		case <-s.stopChan:
			globals.Info("Generation object cleanup scheduler stopped")
			return
		}
	}
}

func (s *GenerationObjectCleanupScheduler) cleanupOrphanObjects() {
	service := &toolsService.GenerationObjectService{}
	result, err := service.CleanupOrphanGenerationObjects(
		context.Background(),
		defaultGenerationObjectCleanupOlderThan,
		defaultGenerationObjectCleanupLimit,
	)
	if err != nil {
		globals.Error("Generation object cleanup failed:", err)
		return
	}
	if result == nil {
		return
	}
	if result.Scanned == 0 {
		globals.Debug("Generation object cleanup: no orphan objects due")
		return
	}

	globals.Info(fmt.Sprintf(
		"Generation object cleanup finished: scanned=%d deleted=%d skipped=%d",
		result.Scanned,
		result.Deleted,
		result.Skipped,
	))
}
