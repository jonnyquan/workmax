package agentbilling

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

type bindingRow struct {
	ID                       uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	BindingID                string    `gorm:"column:binding_id"`
	TurnID                   string    `gorm:"column:turn_id"`
	PrincipalID              string    `gorm:"column:principal_id"`
	TurnCommandDigest        string    `gorm:"column:turn_command_digest"`
	ReservationID            uint64    `gorm:"column:reservation_id"`
	ReservationUID           int       `gorm:"column:reservation_uid"`
	ReservationRequestDigest string    `gorm:"column:reservation_request_digest"`
	ReservationTool          string    `gorm:"column:reservation_tool"`
	ReservedUnits            int64     `gorm:"column:reserved_units"`
	ProjectID                uint64    `gorm:"column:project_id"`
	PricingSnapshotDigest    string    `gorm:"column:pricing_snapshot_digest"`
	BindingDigest            string    `gorm:"column:binding_digest"`
	CreatedAt                time.Time `gorm:"column:created_at"`
}

func (bindingRow) TableName() string { return BindingTable }

func (row bindingRow) record() (BindingRecord, error) {
	record := BindingRecord{
		BindingID: row.BindingID, TurnID: agentv1.TurnID(row.TurnID),
		PrincipalID: agentturn.PrincipalID(row.PrincipalID), TurnCommandDigest: row.TurnCommandDigest,
		ReservationID: row.ReservationID, ReservationUID: row.ReservationUID,
		ReservationRequestDigest: row.ReservationRequestDigest, ReservationTool: row.ReservationTool,
		ReservedUnits: row.ReservedUnits, ProjectID: row.ProjectID,
		PricingSnapshotDigest: row.PricingSnapshotDigest, BindingDigest: row.BindingDigest,
		CreatedAt: row.CreatedAt.UTC(),
	}
	if err := record.Validate(); err != nil {
		return BindingRecord{}, err
	}
	return record, nil
}

func bindingSQLRow(record BindingRecord) (bindingRow, error) {
	if err := record.Validate(); err != nil {
		return bindingRow{}, err
	}
	return bindingRow{
		BindingID: record.BindingID, TurnID: string(record.TurnID), PrincipalID: string(record.PrincipalID),
		TurnCommandDigest: record.TurnCommandDigest, ReservationID: record.ReservationID,
		ReservationUID: record.ReservationUID, ReservationRequestDigest: record.ReservationRequestDigest,
		ReservationTool: record.ReservationTool, ReservedUnits: record.ReservedUnits,
		ProjectID: record.ProjectID, PricingSnapshotDigest: record.PricingSnapshotDigest,
		BindingDigest: record.BindingDigest, CreatedAt: record.CreatedAt.UTC(),
	}, nil
}

type outcomeRow struct {
	ID                      uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	OutcomeID               string    `gorm:"column:outcome_id"`
	BindingID               string    `gorm:"column:binding_id"`
	TurnID                  string    `gorm:"column:turn_id"`
	ReservationID           uint64    `gorm:"column:reservation_id"`
	BindingDigest           string    `gorm:"column:binding_digest"`
	SettlementKey           string    `gorm:"column:settlement_key"`
	LedgerRequestDigest     string    `gorm:"column:ledger_request_digest"`
	AuthorizationKind       string    `gorm:"column:authorization_kind"`
	AttemptID               *string   `gorm:"column:attempt_id"`
	FencingToken            int64     `gorm:"column:fencing_token"`
	OperationID             *string   `gorm:"column:operation_id"`
	TerminalStatus          string    `gorm:"column:terminal_status"`
	RequestedIntent         string    `gorm:"column:requested_intent"`
	UsedUnits               *int64    `gorm:"column:used_units"`
	ReservedUnits           int64     `gorm:"column:reserved_units"`
	Status                  string    `gorm:"column:status"`
	RefundTarget            *string   `gorm:"column:refund_target"`
	RefundDue               int64     `gorm:"column:refund_due"`
	ReservationStateVersion uint64    `gorm:"column:reservation_state_version"`
	ReviewID                *string   `gorm:"column:review_id"`
	ReviewRequestDigest     *string   `gorm:"column:review_request_digest"`
	ResolutionID            *string   `gorm:"column:resolution_id"`
	ResolutionRequestDigest *string   `gorm:"column:resolution_request_digest"`
	OutcomeDigest           string    `gorm:"column:outcome_digest"`
	CreatedAt               time.Time `gorm:"column:created_at"`
	UpdatedAt               time.Time `gorm:"column:updated_at"`
}

func (outcomeRow) TableName() string { return OutcomeTable }

func (row outcomeRow) record() (OutcomeRecord, error) {
	record := OutcomeRecord{
		OutcomeID: row.OutcomeID, BindingID: row.BindingID, TurnID: agentv1.TurnID(row.TurnID),
		ReservationID: row.ReservationID, BindingDigest: row.BindingDigest,
		SettlementKey: row.SettlementKey, LedgerRequestDigest: row.LedgerRequestDigest,
		AuthorizationKind: AuthorizationKind(row.AuthorizationKind), AttemptID: cloneString(row.AttemptID),
		FencingToken: agentv1.Sequence(row.FencingToken), OperationID: cloneString(row.OperationID),
		TerminalStatus: agentv1.TurnStatus(row.TerminalStatus), RequestedIntent: RequestedIntent(row.RequestedIntent),
		UsedUnits: cloneInt64(row.UsedUnits), ReservedUnits: row.ReservedUnits, Status: OutcomeStatus(row.Status),
		RefundTarget: cloneString(row.RefundTarget), RefundDue: row.RefundDue,
		ReservationStateVersion: row.ReservationStateVersion,
		ReviewID:                cloneString(row.ReviewID), ReviewRequestDigest: cloneString(row.ReviewRequestDigest),
		ResolutionID: cloneString(row.ResolutionID), ResolutionRequestDigest: cloneString(row.ResolutionRequestDigest),
		OutcomeDigest: row.OutcomeDigest, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if err := record.Validate(); err != nil {
		return OutcomeRecord{}, err
	}
	return record, nil
}

func outcomeSQLRow(record OutcomeRecord) (outcomeRow, error) {
	if err := record.Validate(); err != nil {
		return outcomeRow{}, err
	}
	return outcomeRow{
		OutcomeID: record.OutcomeID, BindingID: record.BindingID, TurnID: string(record.TurnID),
		ReservationID: record.ReservationID, BindingDigest: record.BindingDigest,
		SettlementKey: record.SettlementKey, LedgerRequestDigest: record.LedgerRequestDigest,
		AuthorizationKind: string(record.AuthorizationKind), AttemptID: cloneString(record.AttemptID),
		FencingToken: int64(record.FencingToken), OperationID: cloneString(record.OperationID),
		TerminalStatus: string(record.TerminalStatus), RequestedIntent: string(record.RequestedIntent),
		UsedUnits: cloneInt64(record.UsedUnits), ReservedUnits: record.ReservedUnits, Status: string(record.Status),
		RefundTarget: cloneString(record.RefundTarget), RefundDue: record.RefundDue,
		ReservationStateVersion: record.ReservationStateVersion,
		ReviewID:                cloneString(record.ReviewID), ReviewRequestDigest: cloneString(record.ReviewRequestDigest),
		ResolutionID: cloneString(record.ResolutionID), ResolutionRequestDigest: cloneString(record.ResolutionRequestDigest),
		OutcomeDigest: record.OutcomeDigest, CreatedAt: record.CreatedAt.UTC(), UpdatedAt: record.UpdatedAt.UTC(),
	}, nil
}

func lockBindingByTurn(tx *gorm.DB, turnID agentv1.TurnID) (BindingRecord, bool, error) {
	if tx == nil {
		return BindingRecord{}, false, ErrLedgerUnavailable
	}
	var row bindingRow
	err := tx.Table(BindingTable).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("turn_id = ?", string(turnID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BindingRecord{}, false, nil
	}
	if err != nil {
		return BindingRecord{}, false, err
	}
	record, err := row.record()
	return record, err == nil, err
}

func lockOutcomeByTurn(tx *gorm.DB, turnID agentv1.TurnID) (OutcomeRecord, bool, error) {
	if tx == nil {
		return OutcomeRecord{}, false, ErrLedgerUnavailable
	}
	var row outcomeRow
	err := tx.Table(OutcomeTable).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("turn_id = ?", string(turnID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OutcomeRecord{}, false, nil
	}
	if err != nil {
		return OutcomeRecord{}, false, err
	}
	record, err := row.record()
	return record, err == nil, err
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
