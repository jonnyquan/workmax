package workagent

import (
	"sync"
	"time"
)

type ArtifactRenderRunnerStatus struct {
	Worker         string    `json:"worker"`
	Running        bool      `json:"running"`
	DisabledReason string    `json:"disabledReason,omitempty"`
	LastError      string    `json:"lastError,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ArtifactRenderRunnerStatusSnapshot struct {
	Static ArtifactRenderRunnerStatus `json:"static"`
	Motion ArtifactRenderRunnerStatus `json:"motion"`
}

func DefaultArtifactRenderRunnerStatuses() ArtifactRenderRunnerStatusSnapshot {
	return ArtifactRenderRunnerStatusSnapshot{
		Static: DefaultArtifactStaticRenderRunnerStatus(),
		Motion: DefaultArtifactMotionRenderRunnerStatus(),
	}
}

type artifactRenderRunnerStatusStore struct {
	mu     sync.RWMutex
	status ArtifactRenderRunnerStatus
}

func (s *artifactRenderRunnerStatusStore) snapshot() ArtifactRenderRunnerStatus {
	if s == nil {
		return ArtifactRenderRunnerStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *artifactRenderRunnerStatusStore) recordRunning(worker string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = ArtifactRenderRunnerStatus{
		Worker:    worker,
		Running:   true,
		UpdatedAt: time.Now().UTC(),
	}
}

func (s *artifactRenderRunnerStatusStore) recordDisabled(worker string, err error) {
	if s == nil {
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = ArtifactRenderRunnerStatus{
		Worker:         worker,
		Running:        false,
		DisabledReason: reason,
		LastError:      reason,
		UpdatedAt:      time.Now().UTC(),
	}
}

func (s *artifactRenderRunnerStatusStore) recordStopped(worker string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = ArtifactRenderRunnerStatus{
		Worker:    worker,
		Running:   false,
		UpdatedAt: time.Now().UTC(),
	}
}
