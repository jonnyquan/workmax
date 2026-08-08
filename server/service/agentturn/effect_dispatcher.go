package agentturn

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// EffectDispatcher drains the Agent Effect Outbox.
//
// It is the handoff the transactional outbox exists for: an Operation commits
// its effects atomically with the Turn's state, and this loop delivers them
// afterwards. Delivery is at-least-once — the provider's own idempotency on
// DedupeKey is what makes it safe, which is why an ambiguous outcome is never
// resolved by guessing here.
//
// It is a candidate runtime, not a process: it opens no port, reads no
// configuration and is composed into nothing.
type EffectDispatcher struct {
	store     EffectOutboxStore
	deliverer Deliverer
	options   EffectDispatcherOptions
	admission *AdmissionGate
}

type EffectDispatcherOptions struct {
	// LeaseOwnerID identifies this dispatcher in the lease it takes.
	LeaseOwnerID string
	// AdmissionGate is the shared, one-way authority boundary for starting a
	// ClaimEffects call. Nil preserves legacy candidate behaviour.
	AdmissionGate *AdmissionGate
	// BatchLimit bounds one claim. Zero selects DefaultEffectClaimBatch.
	BatchLimit int
	// Topics optionally partitions a dispatcher fleet by provider.
	Topics []string
	// IdleBackoff is the pause after an empty outbox. Zero selects
	// DefaultWorkerIdleBackoff.
	IdleBackoff time.Duration
	// DeliveryTimeout bounds one Deliver call so a hung provider cannot hold a
	// lease until it lapses. Zero selects DefaultEffectDeliveryTimeout.
	DeliveryTimeout time.Duration
}

const DefaultEffectDeliveryTimeout = 15 * time.Second

func (options *EffectDispatcherOptions) applyDefaults() {
	if options.IdleBackoff <= 0 {
		options.IdleBackoff = DefaultWorkerIdleBackoff
	}
	if options.DeliveryTimeout <= 0 {
		options.DeliveryTimeout = DefaultEffectDeliveryTimeout
	}
}

func (options EffectDispatcherOptions) Validate() error {
	if err := (ClaimEffectsCommand{
		LeaseOwnerID: options.LeaseOwnerID,
		Limit:        options.BatchLimit,
		Topics:       options.Topics,
	}).Validate(); err != nil {
		return err
	}
	if options.IdleBackoff <= 0 || options.IdleBackoff > MaxWorkerIdleBackoff {
		return fmt.Errorf("idleBackoff must be between 1ns and %s", MaxWorkerIdleBackoff)
	}
	// A delivery allowed to outlast its lease would let a superseded
	// dispatcher and its replacement both be in flight at the provider.
	if options.DeliveryTimeout <= 0 || options.DeliveryTimeout >= DefaultEffectLeaseTTL {
		return fmt.Errorf("deliveryTimeout must be between 1ns and %s", DefaultEffectLeaseTTL)
	}
	return nil
}

func NewEffectDispatcher(store EffectOutboxStore, deliverer Deliverer, options EffectDispatcherOptions) (*EffectDispatcher, error) {
	if store == nil || deliverer == nil {
		return nil, fmt.Errorf("effect dispatcher requires an outbox store and a deliverer")
	}
	options.applyDefaults()
	if err := options.Validate(); err != nil {
		return nil, err
	}
	// Topics define the dispatcher's authority boundary. Keep an owned copy so
	// a caller cannot widen, narrow, or replace that boundary after startup by
	// mutating the slice it passed to the constructor.
	options.Topics = append([]string(nil), options.Topics...)
	return &EffectDispatcher{
		store: store, deliverer: deliverer, options: options,
		admission: options.AdmissionGate,
	}, nil
}

// MatchesAdmissionGate proves the exact gate identity captured at
// construction.
func (dispatcher *EffectDispatcher) MatchesAdmissionGate(expected *AdmissionGate) bool {
	return dispatcher != nil && dispatcher.admission == expected
}

type EffectDispatchReport struct {
	Claimed      int
	Delivered    int
	Retried      int
	DeadLettered int
	Failures     []EffectDispatchFailure
}

type EffectDispatchFailure struct {
	OutboxID string
	Topic    string
	Err      error
}

// DispatchOnce claims one batch and delivers it.
//
// It returns ErrNoClaimableEffects when the outbox is empty. A provider
// failure is not an error here: it is classified into a retry or a dead
// letter, which is the dispatcher's job.
func (dispatcher *EffectDispatcher) DispatchOnce(ctx context.Context) (EffectDispatchReport, error) {
	if err := contextError(ctx); err != nil {
		return EffectDispatchReport{}, err
	}
	if err := dispatcher.admission.Acquire(); err != nil {
		return EffectDispatchReport{}, err
	}
	deliveries, err := dispatcher.store.ClaimEffects(ctx, ClaimEffectsCommand{
		LeaseOwnerID: dispatcher.options.LeaseOwnerID,
		Limit:        dispatcher.options.BatchLimit,
		Topics:       dispatcher.options.Topics,
	})
	if err != nil {
		return EffectDispatchReport{}, err
	}

	report := EffectDispatchReport{Claimed: len(deliveries)}
	for _, delivery := range deliveries {
		if err := contextError(ctx); err != nil {
			// Remaining leases lapse and are reclaimed. Abandoning is correct:
			// completing them after the deadline would report an outcome for a
			// delivery that never ran.
			return report, err
		}
		result, err := dispatcher.dispatch(ctx, delivery)
		if err != nil {
			report.Failures = append(report.Failures, EffectDispatchFailure{
				OutboxID: delivery.OutboxID, Topic: delivery.Topic, Err: err,
			})
			continue
		}
		switch result.Status {
		case EffectStatusDelivered:
			report.Delivered++
		case EffectStatusPending:
			report.Retried++
		case EffectStatusDeadLetter:
			report.DeadLettered++
		}
	}
	return report, nil
}

func (dispatcher *EffectDispatcher) dispatch(ctx context.Context, delivery EffectDelivery) (CompleteEffectResult, error) {
	deliverCtx, cancel := context.WithTimeout(ctx, dispatcher.options.DeliveryTimeout)
	report, deliverErr := dispatcher.deliverer.Deliver(deliverCtx, delivery)
	cancel()

	if deliverErr != nil {
		// A Deliverer that returns an error told us nothing about whether the
		// provider applied the effect, so this is an unknown outcome, not a
		// retryable one. It is only re-sent if the report says that is safe.
		report = DeliveryReport{
			Outcome:             DeliveryOutcomeUnknown,
			ErrorCode:           "deliverer_error",
			IdempotentRetrySafe: report.IdempotentRetrySafe,
		}
	}
	if err := report.Validate(); err != nil {
		report = DeliveryReport{Outcome: DeliveryOutcomeUnknown, ErrorCode: "invalid_report"}
	}

	// Completion uses the parent context: the delivery deadline bounds the
	// provider call, not the durable write that records its outcome.
	return dispatcher.store.CompleteEffect(ctx, CompleteEffectCommand{
		Fence:  delivery.Fence,
		Report: report,
	})
}

// Run loops DispatchOnce until ctx is cancelled. A batch that filled its limit
// is followed immediately by another so a backlog drains at claim speed.
func (dispatcher *EffectDispatcher) Run(ctx context.Context, observe func(EffectDispatchReport, error)) error {
	return dispatcher.RunWithPulse(ctx, observe, nil)
}

// RunWithPulse is Run plus a scheduler-progress pulse. Empty outbox scans
// pulse too, so readiness never depends on Effect traffic being present.
func (dispatcher *EffectDispatcher) RunWithPulse(ctx context.Context, observe func(EffectDispatchReport, error), pulse func()) error {
	limit := ClaimEffectsCommand{Limit: dispatcher.options.BatchLimit}.limit()
	for {
		if err := contextError(ctx); err != nil {
			return err
		}
		observeLoopPulse(pulse)
		report, err := dispatcher.DispatchOnce(ctx)
		if observe != nil && !errors.Is(err, ErrNoClaimableEffects) &&
			!errors.Is(err, ErrAdmissionClosed) {
			observe(report, err)
		}
		switch {
		case err == nil && report.Claimed >= limit:
			continue
		case err == nil:
			if sleepErr := sleepContext(ctx, dispatcher.options.IdleBackoff); sleepErr != nil {
				return sleepErr
			}
		case errors.Is(err, ErrNoClaimableEffects):
			if sleepErr := sleepContext(ctx, dispatcher.options.IdleBackoff); sleepErr != nil {
				return sleepErr
			}
		case errors.Is(err, ErrAdmissionClosed):
			return waitForAdmissionShutdown(ctx)
		default:
			if ctxErr := contextError(ctx); ctxErr != nil {
				return ctxErr
			}
			if sleepErr := sleepContext(ctx, dispatcher.options.IdleBackoff); sleepErr != nil {
				return sleepErr
			}
		}
	}
}
