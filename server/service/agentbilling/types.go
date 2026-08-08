// Package agentbilling adapts the durable Agent Turn kernel to the Credits
// Reservation state machine. It is deliberately outside both agentturn and
// account so the execution kernel does not import billing models and the
// Credits service never calls back into the Turn kernel.
package agentbilling

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

const (
	BindingTable = "w_agent_turn_reservation_binding"
	OutcomeTable = "w_agent_turn_settlement_outcome"

	maxBindingIDBytes       = 64
	maxOutcomeIDBytes       = 64
	maxReservationToolBytes = 64
	maxDigestBytes          = 128
)

var (
	ErrLedgerUnavailable       = errors.New("agent billing ledger is unavailable")
	ErrBindingNotFound         = errors.New("agent turn reservation binding was not found")
	ErrBindingConflict         = errors.New("agent turn reservation binding conflicts with durable identity")
	ErrOutcomeNotFound         = errors.New("agent settlement outcome was not found")
	ErrOutcomeConflict         = errors.New("agent settlement outcome conflicts with durable request")
	ErrReservationIneligible   = errors.New("agent credit reservation is not eligible for execution or settlement")
	ErrReservationStateDrift   = errors.New("agent credit reservation state conflicts with settlement ledger")
	ErrPricingSnapshotConflict = errors.New("agent pricing snapshot conflicts with reservation binding")
)

type OutcomeStatus string

const (
	OutcomeStatusReviewHeld    OutcomeStatus = "review_held"
	OutcomeStatusRefundPending OutcomeStatus = "refund_pending"
	OutcomeStatusFinalized     OutcomeStatus = "finalized"
	OutcomeStatusReleased      OutcomeStatus = "released"
)

func (status OutcomeStatus) Valid() bool {
	return status == OutcomeStatusReviewHeld || status == OutcomeStatusRefundPending ||
		status == OutcomeStatusFinalized || status == OutcomeStatusReleased
}

type RequestedIntent string

const (
	RequestedIntentFinalize RequestedIntent = "finalize"
	RequestedIntentRelease  RequestedIntent = "release"
	RequestedIntentReview   RequestedIntent = "review"
)

func (intent RequestedIntent) Valid() bool {
	return intent == RequestedIntentFinalize || intent == RequestedIntentRelease || intent == RequestedIntentReview
}

type AuthorizationKind string

const (
	AuthorizationKindOperation AuthorizationKind = "operation"
	AuthorizationKindReconcile AuthorizationKind = "reconcile"
)

func (kind AuthorizationKind) Valid() bool {
	return kind == AuthorizationKindOperation || kind == AuthorizationKindReconcile
}

// BindingRecord is an immutable one-to-one Turn -> Reservation receipt. The
// request/pricing digests are copied from trusted server-side admission and
// the newly-created Reservation; no worker input participates in resolution.
type BindingRecord struct {
	BindingID                string
	TurnID                   agentv1.TurnID
	PrincipalID              agentturn.PrincipalID
	TurnCommandDigest        string
	ReservationID            uint64
	ReservationUID           int
	ReservationRequestDigest string
	ReservationTool          string
	ReservedUnits            int64
	ProjectID                uint64
	PricingSnapshotDigest    string
	BindingDigest            string
	CreatedAt                time.Time
}

func (binding BindingRecord) Validate() error {
	if err := validateASCII("bindingId", binding.BindingID, maxBindingIDBytes); err != nil {
		return err
	}
	if strings.TrimSpace(string(binding.TurnID)) == "" || len(binding.TurnID) > agentturn.MaxTurnIDBytes {
		return ErrBindingConflict
	}
	if strings.TrimSpace(string(binding.PrincipalID)) == "" || len(binding.PrincipalID) > agentturn.MaxPrincipalIDBytes {
		return ErrBindingConflict
	}
	if err := validateASCII("turnCommandDigest", binding.TurnCommandDigest, agentturn.MaxCommandDigestBytes); err != nil {
		return ErrBindingConflict
	}
	if binding.ReservationID == 0 || binding.ReservationUID <= 0 ||
		int64(binding.ReservationUID) > int64(^uint32(0)>>1) || binding.ProjectID > uint64(^uint32(0)>>1) ||
		binding.ReservedUnits < 0 || binding.ReservedUnits > int64(^uint32(0)>>1) {
		return ErrBindingConflict
	}
	if !lowerHexDigest(binding.ReservationRequestDigest) {
		return ErrBindingConflict
	}
	if err := validateASCII("reservationTool", binding.ReservationTool, maxReservationToolBytes); err != nil {
		return err
	}
	if !sha256Digest(binding.PricingSnapshotDigest) || !sha256Digest(binding.BindingDigest) {
		return ErrBindingConflict
	}
	if binding.BindingID != bindingID(binding.TurnID) || binding.BindingDigest != bindingRecordDigest(binding) || binding.CreatedAt.IsZero() {
		return ErrBindingConflict
	}
	return nil
}

// OutcomeRecord is the current monotonic projection for one SettlementKey.
// The Agent Review/Evidence/Resolution tables remain the append-only decision
// history; this row answers recovery with the exact current Credits result.
type OutcomeRecord struct {
	OutcomeID               string
	BindingID               string
	TurnID                  agentv1.TurnID
	ReservationID           uint64
	BindingDigest           string
	SettlementKey           string
	LedgerRequestDigest     string
	AuthorizationKind       AuthorizationKind
	AttemptID               *string
	FencingToken            agentv1.Sequence
	OperationID             *string
	TerminalStatus          agentv1.TurnStatus
	RequestedIntent         RequestedIntent
	UsedUnits               *int64
	ReservedUnits           int64
	Status                  OutcomeStatus
	RefundTarget            *string
	RefundDue               int64
	ReservationStateVersion uint64
	ReviewID                *string
	ReviewRequestDigest     *string
	ResolutionID            *string
	ResolutionRequestDigest *string
	OutcomeDigest           string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (outcome OutcomeRecord) Validate() error {
	if err := validateASCII("outcomeId", outcome.OutcomeID, maxOutcomeIDBytes); err != nil {
		return err
	}
	if err := validateASCII("bindingId", outcome.BindingID, maxBindingIDBytes); err != nil {
		return err
	}
	if strings.TrimSpace(string(outcome.TurnID)) == "" || len(outcome.TurnID) > agentturn.MaxTurnIDBytes ||
		outcome.ReservationID == 0 || !sha256Digest(outcome.BindingDigest) {
		return ErrOutcomeConflict
	}
	if err := validateASCII("settlementKey", outcome.SettlementKey, agentturn.MaxSettlementKeyBytes); err != nil {
		return err
	}
	if !sha256Digest(outcome.LedgerRequestDigest) || !outcome.AuthorizationKind.Valid() ||
		outcome.FencingToken < 1 || outcome.FencingToken > agentturn.MaxDurableSequence ||
		!outcome.TerminalStatus.Valid() ||
		!outcome.TerminalStatus.Terminal() || !outcome.RequestedIntent.Valid() || !outcome.Status.Valid() ||
		outcome.ReservedUnits < 0 || outcome.ReservationStateVersion == 0 {
		return ErrOutcomeConflict
	}
	if outcome.UsedUnits != nil && (*outcome.UsedUnits < 0 || *outcome.UsedUnits > outcome.ReservedUnits) {
		return ErrOutcomeConflict
	}
	if outcome.RefundDue < 0 || outcome.RefundDue > outcome.ReservedUnits {
		return ErrOutcomeConflict
	}
	reviewTuple := outcome.ReviewID != nil || outcome.ReviewRequestDigest != nil
	resolutionTuple := outcome.ResolutionID != nil || outcome.ResolutionRequestDigest != nil
	switch outcome.AuthorizationKind {
	case AuthorizationKindOperation:
		if outcome.AttemptID == nil || outcome.OperationID == nil ||
			validateASCII("attemptId", *outcome.AttemptID, agentturn.MaxAttemptIDBytes) != nil ||
			validateASCII("operationId", *outcome.OperationID, agentturn.MaxOperationIDBytes) != nil ||
			outcome.SettlementKey != settlementAuthorizationKey(
				"operation", outcome.TurnID, *outcome.OperationID,
			) {
			return ErrOutcomeConflict
		}
	case AuthorizationKindReconcile:
		if outcome.AttemptID != nil || outcome.OperationID != nil ||
			outcome.SettlementKey != settlementAuthorizationKey(
				"reconcile", outcome.TurnID, strconv.FormatInt(int64(outcome.FencingToken), 10),
			) {
			return ErrOutcomeConflict
		}
	}
	if outcome.RequestedIntent == RequestedIntentReview {
		if !reviewTuple || outcome.ReviewID == nil || outcome.ReviewRequestDigest == nil ||
			validateASCII("reviewId", *outcome.ReviewID, agentturn.MaxSettlementReviewIDBytes) != nil ||
			!lowerHexDigest(*outcome.ReviewID) || !sha256Digest(*outcome.ReviewRequestDigest) {
			return ErrOutcomeConflict
		}
	} else if reviewTuple || resolutionTuple {
		return ErrOutcomeConflict
	}
	if resolutionTuple && (outcome.ResolutionID == nil || outcome.ResolutionRequestDigest == nil ||
		validateASCII("resolutionId", *outcome.ResolutionID, agentturn.MaxSettlementReviewIDBytes) != nil ||
		!lowerHexDigest(*outcome.ResolutionID) || !sha256Digest(*outcome.ResolutionRequestDigest)) {
		return ErrOutcomeConflict
	}
	switch outcome.Status {
	case OutcomeStatusReviewHeld:
		if outcome.RequestedIntent != RequestedIntentReview || outcome.UsedUnits != nil || resolutionTuple ||
			outcome.RefundTarget != nil || outcome.RefundDue != 0 {
			return ErrOutcomeConflict
		}
	case OutcomeStatusRefundPending:
		if outcome.UsedUnits == nil || outcome.RefundTarget == nil ||
			(*outcome.RefundTarget != "finalized" && *outcome.RefundTarget != "released") ||
			outcome.RefundDue <= 0 || outcome.RefundDue != outcome.ReservedUnits-*outcome.UsedUnits ||
			(*outcome.RefundTarget == "released" && *outcome.UsedUnits != 0) {
			return ErrOutcomeConflict
		}
	case OutcomeStatusFinalized:
		if outcome.UsedUnits == nil || outcome.RefundTarget != nil || outcome.RefundDue != 0 {
			return ErrOutcomeConflict
		}
	case OutcomeStatusReleased:
		if outcome.UsedUnits == nil || *outcome.UsedUnits != 0 || outcome.RefundTarget != nil || outcome.RefundDue != 0 {
			return ErrOutcomeConflict
		}
	}
	if outcome.RequestedIntent == RequestedIntentFinalize && (outcome.UsedUnits == nil || *outcome.UsedUnits <= 0) {
		return ErrOutcomeConflict
	}
	if outcome.RequestedIntent == RequestedIntentRelease && (outcome.UsedUnits == nil || *outcome.UsedUnits != 0) {
		return ErrOutcomeConflict
	}
	if outcome.RequestedIntent == RequestedIntentReview && outcome.Status != OutcomeStatusReviewHeld && !resolutionTuple {
		return ErrOutcomeConflict
	}
	if !sha256Digest(outcome.OutcomeDigest) || outcome.OutcomeID != outcomeID(outcome.SettlementKey) ||
		outcome.OutcomeDigest != outcomeRecordDigest(outcome) || outcome.CreatedAt.IsZero() ||
		outcome.UpdatedAt.Before(outcome.CreatedAt) {
		return ErrOutcomeConflict
	}
	return nil
}

func validateASCII(name, value string, max int) error {
	if strings.TrimSpace(value) == "" || len(value) > max {
		return fmt.Errorf("%s is required and must not exceed %d bytes", name, max)
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return fmt.Errorf("%s must contain printable ASCII", name)
		}
	}
	return nil
}

func lowerHexDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256Digest(value string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(value, prefix) && lowerHexDigest(strings.TrimPrefix(value, prefix))
}

func digest(domain string, values ...string) string {
	hash := sha256.New()
	parts := append([]string{domain}, values...)
	for _, part := range parts {
		fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func shortDigest(domain string, values ...string) string {
	return strings.TrimPrefix(digest(domain, values...), "sha256:")
}

func bindingID(turnID agentv1.TurnID) string {
	return shortDigest("agent-turn-reservation-binding-id-v1", string(turnID))
}

func bindingRecordDigest(binding BindingRecord) string {
	return digest(
		"agent-turn-reservation-binding-v1",
		binding.BindingID,
		string(binding.TurnID),
		string(binding.PrincipalID),
		binding.TurnCommandDigest,
		strconv.FormatUint(binding.ReservationID, 10),
		strconv.Itoa(binding.ReservationUID),
		binding.ReservationRequestDigest,
		binding.ReservationTool,
		strconv.FormatInt(binding.ReservedUnits, 10),
		strconv.FormatUint(binding.ProjectID, 10),
		binding.PricingSnapshotDigest,
	)
}

func outcomeID(settlementKey string) string {
	return shortDigest("agent-turn-settlement-outcome-id-v1", settlementKey)
}

func settlementAuthorizationKey(domain string, turnID agentv1.TurnID, receipt string) string {
	return "wm:turn-settlement:v1:" + shortDigest(domain, string(turnID), receipt)
}

func outcomeRecordDigest(outcome OutcomeRecord) string {
	return digest(
		"agent-turn-settlement-outcome-v1",
		outcome.OutcomeID,
		outcome.BindingID,
		string(outcome.TurnID),
		strconv.FormatUint(outcome.ReservationID, 10),
		outcome.BindingDigest,
		outcome.SettlementKey,
		outcome.LedgerRequestDigest,
		string(outcome.AuthorizationKind),
		optionalString(outcome.AttemptID),
		strconv.FormatInt(int64(outcome.FencingToken), 10),
		optionalString(outcome.OperationID),
		string(outcome.TerminalStatus),
		string(outcome.RequestedIntent),
		optionalInt64(outcome.UsedUnits),
		strconv.FormatInt(outcome.ReservedUnits, 10),
		string(outcome.Status),
		optionalString(outcome.RefundTarget),
		strconv.FormatInt(outcome.RefundDue, 10),
		strconv.FormatUint(outcome.ReservationStateVersion, 10),
		optionalString(outcome.ReviewID),
		optionalString(outcome.ReviewRequestDigest),
		optionalString(outcome.ResolutionID),
		optionalString(outcome.ResolutionRequestDigest),
	)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalInt64(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

func int64Pointer(value int64) *int64 {
	copy := value
	return &copy
}
