package agentturn

import (
	"errors"
	"fmt"
	"time"

	agentv1 "server/contracts/agent/v1"
)

var (
	// ErrWorkerPluginLimitsUnavailable means a worker with limit enforcement
	// enabled claimed a Plugin release for which it has no exact policy. The
	// worker deliberately neither executes nor terminally commits that epoch.
	ErrWorkerPluginLimitsUnavailable = errors.New("durable turn worker plugin execution limits are unavailable")
	// ErrWorkerExecutionTimeout and ErrWorkerProgressTimeout are cancellation
	// causes owned by the kernel. They are exposed through context.Cause to a
	// cooperative executor and through WorkerRunResult.LimitExceeded.
	ErrWorkerExecutionTimeout = errors.New("durable turn worker execution timeout exceeded")
	ErrWorkerProgressTimeout  = errors.New("durable turn worker progress timeout exceeded")
	// ErrWorkerRestartRequired means execution did not quiesce inside the
	// bounded stop grace after cancellation (the executor or an Emit remained
	// live). The Worker is permanently sealed against another claim so leaked
	// goroutines cannot accumulate.
	ErrWorkerRestartRequired = errors.New("durable turn worker restart is required")
)

const (
	MaxWorkerPluginLimits     = MaxClaimPluginScopes
	MaxWorkerExecutionTimeout = 24 * time.Hour
)

// PluginExecutionLimits binds both ceilings to one immutable Plugin release.
// Matching is exact and case-sensitive across ID, Version and ReleaseDigest.
// There is no ID-only or default policy fallback.
type PluginExecutionLimits struct {
	Plugin           agentv1.EventPluginRef
	ExecutionTimeout time.Duration
	ProgressTimeout  time.Duration
}

func (limits PluginExecutionLimits) Validate() error {
	if err := limits.Plugin.Validate(); err != nil {
		return fmt.Errorf("plugin: %w", err)
	}
	if err := validatePluginRef(limits.Plugin); err != nil {
		return fmt.Errorf("plugin: %w", err)
	}
	if limits.ExecutionTimeout <= 0 || limits.ExecutionTimeout > MaxWorkerExecutionTimeout {
		return fmt.Errorf("executionTimeout must be between 1ns and %s", MaxWorkerExecutionTimeout)
	}
	if limits.ProgressTimeout <= 0 || limits.ProgressTimeout > limits.ExecutionTimeout {
		return fmt.Errorf("progressTimeout must be between 1ns and executionTimeout")
	}
	return nil
}

// WorkerLimitKind classifies the ceiling that ended one execution epoch.
// Empty means that no kernel ceiling fired.
type WorkerLimitKind string

const (
	WorkerLimitExecutionTimeout WorkerLimitKind = "execution_timeout"
	WorkerLimitProgressTimeout  WorkerLimitKind = "progress_timeout"
)

func (kind WorkerLimitKind) Valid() bool {
	return kind == WorkerLimitExecutionTimeout || kind == WorkerLimitProgressTimeout
}

func (kind WorkerLimitKind) err() error {
	switch kind {
	case WorkerLimitExecutionTimeout:
		return ErrWorkerExecutionTimeout
	case WorkerLimitProgressTimeout:
		return ErrWorkerProgressTimeout
	default:
		return nil
	}
}

func normalizePluginExecutionLimits(input []PluginExecutionLimits) (
	[]PluginExecutionLimits,
	map[agentv1.EventPluginRef]PluginExecutionLimits,
	error,
) {
	if len(input) > MaxWorkerPluginLimits {
		return nil, nil, fmt.Errorf("pluginLimits must contain at most %d releases", MaxWorkerPluginLimits)
	}
	output := append([]PluginExecutionLimits(nil), input...)
	byPlugin := make(map[agentv1.EventPluginRef]PluginExecutionLimits, len(output))
	for index, limits := range output {
		if err := limits.Validate(); err != nil {
			return nil, nil, fmt.Errorf("pluginLimits[%d]: %w", index, err)
		}
		if _, duplicate := byPlugin[limits.Plugin]; duplicate {
			return nil, nil, fmt.Errorf("pluginLimits[%d] repeats a release", index)
		}
		byPlugin[limits.Plugin] = limits
	}
	return output, byPlugin, nil
}
