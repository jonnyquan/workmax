package model

import (
	"server/globals"
	"time"
)

// CreditReservation tracks an in-flight credit debit so that retries in the
// TTL window can reuse the same reservation (no double charge) and failures
// can refund exactly what was debited. The row is created atomically with the
// CreditsPack debit and finalized or released once the downstream operation
// resolves.
type CreditReservation struct {
	globals.GraMODEL
	UID            int        `json:"uid" gorm:"column:uid;not null;index:idx_reservation_uid_key,priority:1;comment:用户ID"`
	Tool           string     `json:"tool" gorm:"column:tool;type:varchar(64);not null;comment:工具标识（canvas_chat / canvas_agent / canvas_optimize_prompt / image_generate ...）"`
	IdempotencyKey string     `json:"idempotencyKey" gorm:"column:idempotency_key;type:varchar(128);not null;index:idx_reservation_uid_key,priority:2,unique;comment:幂等键，通常为 quoteId 或客户端生成的 UUID"`
	RequestDigest  string     `json:"requestDigest,omitempty" gorm:"column:request_digest;type:varchar(64) CHARACTER SET ascii COLLATE ascii_bin;default:null;comment:冻结预留请求摘要"`
	QuoteID        string     `json:"quoteId" gorm:"column:quote_id;type:varchar(128);comment:关联的 quoteId（可选）"`
	Reserved       int        `json:"reserved" gorm:"column:reserved;not null;default:0;comment:预留积分数量"`
	Used           int        `json:"used" gorm:"column:used;not null;default:0;comment:实际结算使用的积分（finalize 后写入）"`
	Status         string     `json:"status" gorm:"column:status;type:varchar(16);not null;default:'reserved';comment:状态 reserved/review_hold/refund_pending/finalized/released/expired"`
	ExpiresAt      time.Time  `json:"expiresAt" gorm:"column:expires_at;type:datetime;not null;comment:过期时间"`
	FinalizedAt    *time.Time `json:"finalizedAt" gorm:"column:finalized_at;type:datetime;comment:结算时间"`
	ReleasedAt     *time.Time `json:"releasedAt" gorm:"column:released_at;type:datetime;comment:释放/退回时间"`
	Remark         string     `json:"remark" gorm:"column:remark;type:varchar(255);comment:备注"`

	// Review hold fields bind an ambiguous Agent settlement to its immutable
	// review command. CreditReservation deliberately does not copy the Agent
	// Review's pending/metered/finalized sub-state: review_hold remains the sole
	// credit state until the exact review resolution settles it.
	HoldReviewID        string     `json:"holdReviewId,omitempty" gorm:"column:hold_review_id;type:varchar(256) CHARACTER SET ascii COLLATE ascii_bin;default:null;comment:持有预留的结算复核ID"`
	HoldSettlementKey   string     `json:"holdSettlementKey,omitempty" gorm:"column:hold_settlement_key;type:varchar(256) CHARACTER SET ascii COLLATE ascii_bin;default:null;comment:结算复核绑定的幂等键"`
	HoldRequestDigest   string     `json:"holdRequestDigest,omitempty" gorm:"column:hold_request_digest;type:varchar(128) CHARACTER SET ascii COLLATE ascii_bin;default:null;comment:冻结复核请求摘要"`
	ReviewHeldAt        *time.Time `json:"reviewHeldAt,omitempty" gorm:"column:review_held_at;type:datetime(6);comment:进入复核持有状态的数据库时间"`
	RefundTargetStatus  string     `json:"refundTargetStatus,omitempty" gorm:"column:refund_target_status;type:varchar(16) CHARACTER SET ascii COLLATE ascii_bin;default:null;comment:退款成功后的目标终态"`
	RefundTargetUsed    *int       `json:"refundTargetUsed,omitempty" gorm:"column:refund_target_used;type:int unsigned;comment:退款成功后应结算的实际积分"`
	RefundDue           int        `json:"refundDue,omitempty" gorm:"column:refund_due;type:int unsigned;not null;default:0;comment:仍需原子退回的积分"`
	RefundAttempts      uint64     `json:"refundAttempts,omitempty" gorm:"column:refund_attempts;type:bigint unsigned;not null;default:0;comment:退款尝试次数"`
	NextRefundAt        *time.Time `json:"nextRefundAt,omitempty" gorm:"column:next_refund_at;type:datetime(6);comment:下次允许重试退款的数据库时间"`
	LastRefundErrorCode string     `json:"lastRefundErrorCode,omitempty" gorm:"column:last_refund_error_code;type:varchar(64) CHARACTER SET ascii COLLATE ascii_bin;default:null;comment:最近一次退款失败的闭集错误码"`
	StateChangedAt      *time.Time `json:"stateChangedAt,omitempty" gorm:"column:state_changed_at;type:datetime(6);comment:最近一次状态迁移的数据库时间"`
	StateVersion        uint64     `json:"stateVersion,omitempty" gorm:"column:state_version;type:bigint unsigned;not null;default:0;comment:状态CAS版本"`

	// ProjectID — P1 #6 slice 2 (migration 20260619). When the
	// reservation is tied to a project (agent-driven thread bound
	// to w_global_project), the project's budget cap gates
	// Reserve and the Finalize / Release diff routes back to the
	// project's running tally. 0 = no project scope (most
	// non-agent reservations).
	ProjectID uint `json:"projectId,omitempty" gorm:"column:project_id;not null;default:0;comment:可选的 w_global_project.id 项目预算关联"`
}

func (CreditReservation) TableName() string {
	return "w_credit_reservation"
}

// Reservation status values.
const (
	CreditReservationStatusReserved      = "reserved"
	CreditReservationStatusReviewHold    = "review_hold"
	CreditReservationStatusRefundPending = "refund_pending"
	CreditReservationStatusFinalized     = "finalized"
	CreditReservationStatusReleased      = "released"
	CreditReservationStatusExpired       = "expired"
)

// IsTerminal reports whether the reservation is already in a final state and
// cannot transition further. Used when an idempotency-key hit surfaces an old
// reservation — a terminal row means "request already processed; short-circuit".
func (r CreditReservation) IsTerminal() bool {
	switch r.Status {
	case CreditReservationStatusFinalized,
		CreditReservationStatusReleased,
		CreditReservationStatusExpired:
		return true
	default:
		return false
	}
}

// IsHeld reports whether ordinary TTL/release handling is suspended while an
// exact Agent settlement review owns the reservation.
func (r CreditReservation) IsHeld() bool {
	return r.Status == CreditReservationStatusReviewHold
}

// IsRefundPending reports whether the desired terminal outcome is waiting for
// an atomic Pack + Project refund retry.
func (r CreditReservation) IsRefundPending() bool {
	return r.Status == CreditReservationStatusRefundPending
}

// IsActiveDebited reports the non-terminal states in which the original debit
// remains economically active. A finalized charge is intentionally excluded:
// it is settled usage, not an in-flight hold.
func (r CreditReservation) IsActiveDebited() bool {
	return r.Status == CreditReservationStatusReserved || r.IsHeld() || r.IsRefundPending()
}
