package agentbilling

import (
	"errors"
	"reflect"

	"gorm.io/gorm"

	"server/service/agentturn"
)

var ErrProviderUsageMeterUnavailable = errors.New("agent provider usage meter is unavailable")

// ProviderUsageMeter is the narrow transaction-local metering capability
// needed by a provider-aware Review. The kernel, not an API caller, assembles
// the command from a locked Review, immutable Meter Release and durable
// Provider receipts. Implementations must not perform network I/O.
type ProviderUsageMeter interface {
	MeasureProviderUsage(
		tx *gorm.DB,
		command agentturn.MeasureSettlementReviewProviderUsageCommand,
	) (agentturn.SettlementReviewProviderUsageAuthorityReceipt, error)
}

// ProviderUsageCreditAuthority is the single object sealed into an
// agentturn.SQLStore for provider-aware Reviews. Metering is deliberately
// separate from Credits mutation: the trusted meter can return only a
// measurement digest and positive units, while every Reservation lookup,
// hold, settlement and execution decision remains owned by the immutable
// Turn-to-Reservation ledger.
type ProviderUsageCreditAuthority struct {
	credits *CreditSettlementAuthority
	meter   ProviderUsageMeter
}

func NewProviderUsageCreditAuthority(
	credits *CreditSettlementAuthority,
	meter ProviderUsageMeter,
) (*ProviderUsageCreditAuthority, error) {
	if !creditSettlementAuthorityReady(credits) {
		return nil, ErrLedgerUnavailable
	}
	if providerUsageMeterMissing(meter) {
		return nil, ErrProviderUsageMeterUnavailable
	}
	return &ProviderUsageCreditAuthority{credits: credits, meter: meter}, nil
}

func (authority *ProviderUsageCreditAuthority) Settle(
	tx *gorm.DB,
	command agentturn.SettlementCommand,
) error {
	credits, err := authority.creditAuthority()
	if err != nil {
		return err
	}
	return credits.Settle(tx, command)
}

func (authority *ProviderUsageCreditAuthority) HoldForReview(
	tx *gorm.DB,
	command agentturn.SettlementReviewHoldCommand,
) error {
	credits, err := authority.creditAuthority()
	if err != nil {
		return err
	}
	return credits.HoldForReview(tx, command)
}

func (authority *ProviderUsageCreditAuthority) ResolveReview(
	tx *gorm.DB,
	command agentturn.SettlementReviewResolutionAuthorityCommand,
) (agentturn.SettlementReviewResolutionAuthorityReceipt, error) {
	credits, err := authority.creditAuthority()
	if err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	return credits.ResolveReview(tx, command)
}

func (authority *ProviderUsageCreditAuthority) AuthorizeTurnExecution(
	tx *gorm.DB,
	turn agentturn.Turn,
) error {
	credits, err := authority.creditAuthority()
	if err != nil {
		return err
	}
	return credits.AuthorizeTurnExecution(tx, turn)
}

func (authority *ProviderUsageCreditAuthority) VerifyExpiredTurnReservation(
	tx *gorm.DB,
	turn agentturn.Turn,
) error {
	credits, err := authority.creditAuthority()
	if err != nil {
		return err
	}
	return credits.VerifyExpiredTurnReservation(tx, turn)
}

func (authority *ProviderUsageCreditAuthority) MeasureProviderUsage(
	tx *gorm.DB,
	command agentturn.MeasureSettlementReviewProviderUsageCommand,
) (agentturn.SettlementReviewProviderUsageAuthorityReceipt, error) {
	if authority == nil || !creditSettlementAuthorityReady(authority.credits) ||
		providerUsageMeterMissing(authority.meter) {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, ErrProviderUsageMeterUnavailable
	}
	if tx == nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, ErrProviderUsageMeterUnavailable
	}
	if err := command.Validate(); err != nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	receipt, err := authority.meter.MeasureProviderUsage(tx, command)
	if err != nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	if err := receipt.Validate(command); err != nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	return receipt, nil
}

func (authority *ProviderUsageCreditAuthority) creditAuthority() (*CreditSettlementAuthority, error) {
	if authority == nil || !creditSettlementAuthorityReady(authority.credits) {
		return nil, ErrLedgerUnavailable
	}
	return authority.credits, nil
}

func creditSettlementAuthorityReady(authority *CreditSettlementAuthority) bool {
	return authority != nil && authority.db != nil && authority.db.Config != nil &&
		authority.db.Dialector != nil && authority.reservations != nil
}

func providerUsageMeterMissing(meter ProviderUsageMeter) bool {
	if meter == nil {
		return true
	}
	value := reflect.ValueOf(meter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var (
	_ agentturn.SettlementReviewProviderUsageAuthority = (*ProviderUsageCreditAuthority)(nil)
	_ agentturn.TurnReservationExecutionAuthority      = (*ProviderUsageCreditAuthority)(nil)
)
