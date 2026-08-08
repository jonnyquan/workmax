package account

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/service/project"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultReservationTTL            = 10 * time.Minute
	processingReservationExtension   = 30 * time.Minute
	maxReservationToolBytes          = 64
	maxReservationKeyBytes           = 128
	maxReservationQuoteIDBytes       = 128
	maxReservationReviewIDBytes      = 256
	maxReservationSettlementBytes    = 256
	maxReservationSchemaInt          = int64(1<<31 - 1)
	reservationDigestBytes           = 64
	reservationReviewDigestBytes     = len("sha256:") + sha256.Size*2
	maxRefundBackoff                 = time.Hour
	agentTurnReservationBindingTable = "w_agent_turn_reservation_binding"
)

// CreditReservationService owns the durable credit hold and composes the
// CreditsPack money movement. All state transitions run in the caller's
// transaction. Agent settlement may call into this service while holding its
// Turn/Review rows; this service never calls back into agentturn.
type CreditReservationService struct {
	creditsPack *CreditsPackService
}

func NewCreditReservationService() *CreditReservationService {
	return &CreditReservationService{creditsPack: NewCreditsPackService()}
}

var (
	// ErrReservationTerminal is the common classification for attempts to
	// change an incompatible terminal outcome.
	ErrReservationTerminal = errors.New("credit reservation is terminal")

	// Kept as the legacy sentinel because existing handlers already branch on
	// it. Exact Finalize replay with the same used amount now returns nil.
	ErrReservationAlreadyFinalized = fmt.Errorf("%w: finalized", ErrReservationTerminal)
	ErrReservationAlreadyReleased  = fmt.Errorf("%w: released", ErrReservationTerminal)
	ErrReservationExpired          = fmt.Errorf("%w: expired", ErrReservationTerminal)

	ErrReservationTTLExpired         = errors.New("credit reservation ttl expired")
	ErrReservationReplayConflict     = errors.New("credit reservation replay conflicts with immutable request")
	ErrReservationSettlementConflict = errors.New("credit reservation settlement conflicts with durable outcome")
	ErrReservationReviewPending      = errors.New("credit reservation is held for settlement review")
	ErrReservationReviewConflict     = errors.New("credit reservation review binding conflict")
	ErrReservationInProgress         = errors.New("credit reservation request is already in progress")
	ErrReservationAlreadyProcessed   = errors.New("credit reservation request was already processed")
)

// ReservationRequest describes one immutable reservation admission. Remark is
// audit-only and TTL is an execution-policy snapshot; both are deliberately
// excluded from RequestDigest. Commercial identity and idempotency fields are
// included.
type ReservationRequest struct {
	UID            int
	Tool           string
	IdempotencyKey string
	QuoteID        string
	Reserved       int
	TTL            time.Duration
	Remark         string
	ProjectID      uint
}

type ReservationResult struct {
	Reservation *model.CreditReservation
	Created     bool
}

// ReservationSettlementSnapshot is a read-only value view of the durable
// Reservation state observed while its row lock is held. It deliberately does
// not expose the GORM model: settlement adapters can persist an exact receipt
// without accidentally mutating or saving a stale model instance.
//
// RefundTargetUsed remains nullable because zero is a meaningful release
// target and must be distinguishable from a reservation with no refund intent.
type ReservationSettlementSnapshot struct {
	ReservationID  uint
	UID            int
	Tool           string
	IdempotencyKey string
	RequestDigest  string
	QuoteID        string
	ProjectID      uint

	Status   string
	Reserved int
	Used     int

	HoldReviewID      string
	HoldSettlementKey string
	HoldRequestDigest string
	ReviewHeldAt      *time.Time

	RefundTargetStatus  string
	RefundTargetUsed    *int
	RefundDue           int
	RefundAttempts      uint64
	NextRefundAt        *time.Time
	LastRefundErrorCode string

	ExpiresAt      time.Time
	FinalizedAt    *time.Time
	ReleasedAt     *time.Time
	StateChangedAt *time.Time
	StateVersion   uint64
}

// ReservationReviewHold is an opaque exact binding copied from a durable
// Settlement Review. P0-046 exposes the transaction-local Credits primitive;
// the later Turn-to-Reservation ledger will resolve which reservation a Turn
// owns. No production Worker composition is wired in this phase.
type ReservationReviewHold struct {
	ReviewID      string
	SettlementKey string
	RequestDigest string
}

func (hold ReservationReviewHold) validate() error {
	if err := validateReservationText("reviewId", hold.ReviewID, maxReservationReviewIDBytes); err != nil {
		return err
	}
	if err := validateReservationText("settlementKey", hold.SettlementKey, maxReservationSettlementBytes); err != nil {
		return err
	}
	if len(hold.RequestDigest) != reservationReviewDigestBytes || !strings.HasPrefix(hold.RequestDigest, "sha256:") {
		return fmt.Errorf("requestDigest must use canonical sha256:<lowercase-hex>")
	}
	hexDigest := strings.TrimPrefix(hold.RequestDigest, "sha256:")
	if _, err := hex.DecodeString(hexDigest); err != nil || strings.ToLower(hexDigest) != hexDigest {
		return fmt.Errorf("requestDigest must use canonical sha256:<lowercase-hex>")
	}
	return nil
}

func validateReservationText(name, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	return nil
}

func normalizedReservationTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return defaultReservationTTL
	}
	return ttl
}

// CanonicalReservationRequestDigest validates and hashes the immutable
// commercial identity of one Reservation admission. TTL is execution policy
// and Remark is audit metadata, so neither participates in this digest.
// Reserve calls this same function before writing: read-only admission
// verifiers therefore cannot drift from the durable request identity.
func CanonicalReservationRequestDigest(req ReservationRequest) (string, error) {
	if req.UID <= 0 || int64(req.UID) > maxReservationSchemaInt {
		return "", fmt.Errorf("invalid uid")
	}
	if req.Reserved < 0 {
		return "", fmt.Errorf("reserved must be non-negative")
	}
	if int64(req.Reserved) > maxReservationSchemaInt {
		return "", fmt.Errorf("reserved exceeds signed INT schema boundary")
	}
	if uint64(req.ProjectID) > uint64(maxReservationSchemaInt) {
		return "", fmt.Errorf("projectId exceeds signed INT schema boundary")
	}
	if err := validateReservationText("tool", req.Tool, maxReservationToolBytes); err != nil {
		return "", err
	}
	if err := validateReservationText("idempotencyKey", req.IdempotencyKey, maxReservationKeyBytes); err != nil {
		return "", err
	}
	if len(req.QuoteID) > maxReservationQuoteIDBytes {
		return "", fmt.Errorf("quoteId exceeds %d bytes", maxReservationQuoteIDBytes)
	}

	hash := sha256.New()
	for _, value := range []string{
		"workmax-credit-reservation-v1",
		strconv.Itoa(req.UID),
		req.Tool,
		req.IdempotencyKey,
		req.QuoteID,
		strconv.Itoa(req.Reserved),
		strconv.FormatUint(uint64(req.ProjectID), 10),
	} {
		fmt.Fprintf(hash, "%d:%s", len(value), value)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// databaseReservationNow makes TTL and retry decisions from the database
// clock. MySQL is formatted as textual UTC so parseTime/session settings
// cannot alter it; SQLite returns the same epoch instant in the location its
// GORM driver uses for textual DATETIME predicates.
func databaseReservationNow(tx *gorm.DB) (time.Time, error) {
	if tx == nil || tx.Config == nil || tx.Dialector == nil {
		return time.Time{}, fmt.Errorf("nil transaction")
	}
	switch tx.Dialector.Name() {
	case "mysql":
		var row struct {
			CurrentTime string `gorm:"column:reservation_now"`
		}
		query := "SELECT DATE_FORMAT(UTC_TIMESTAMP(6), '%Y-%m-%d %H:%i:%s.%f') AS reservation_now"
		if err := tx.Raw(query).Scan(&row).Error; err != nil {
			return time.Time{}, fmt.Errorf("read reservation database time: %w", err)
		}
		now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", row.CurrentTime, time.UTC)
		if err != nil || now.IsZero() {
			return time.Time{}, fmt.Errorf("reservation database returned invalid UTC time")
		}
		return now.UTC().Truncate(time.Microsecond), nil
	case "sqlite":
		var row struct {
			UnixSeconds int64 `gorm:"column:unix_seconds"`
		}
		if err := tx.Raw("SELECT CAST(strftime('%s', 'now') AS INTEGER) AS unix_seconds").Scan(&row).Error; err != nil {
			return time.Time{}, fmt.Errorf("read reservation database time: %w", err)
		}
		if row.UnixSeconds <= 0 {
			return time.Time{}, fmt.Errorf("reservation database returned invalid unix time")
		}
		// GORM's SQLite driver serializes DATETIME values in time.Local. Return
		// the same instant in that location so SQLite's textual predicates do
		// not compare a UTC string with a local-offset value.
		return time.Unix(row.UnixSeconds, 0).In(time.Local), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported reservation database dialect")
	}
}

// Reserve first materializes the unique Reservation row, then locks Project
// and Pack rows. The unique insert is the missing-row serialization anchor: a
// concurrent same-key request cannot both pass a SELECT gap and debit money.
func (s *CreditReservationService) Reserve(tx *gorm.DB, req ReservationRequest) (ReservationResult, error) {
	if tx == nil {
		return ReservationResult{}, fmt.Errorf("nil transaction")
	}
	digest, err := CanonicalReservationRequestDigest(req)
	if err != nil {
		return ReservationResult{}, err
	}

	ttl := normalizedReservationTTL(req.TTL)
	now, err := databaseReservationNow(tx)
	if err != nil {
		return ReservationResult{}, err
	}
	reservation := &model.CreditReservation{
		GraMODEL:       globals.GraMODEL{CreatedAt: now, UpdatedAt: now},
		UID:            req.UID,
		Tool:           req.Tool,
		IdempotencyKey: req.IdempotencyKey,
		RequestDigest:  digest,
		QuoteID:        req.QuoteID,
		Reserved:       req.Reserved,
		Status:         model.CreditReservationStatusReserved,
		ExpiresAt:      now.Add(ttl),
		Remark:         req.Remark,
		ProjectID:      req.ProjectID,
		StateChangedAt: &now,
		StateVersion:   1,
	}
	if createErr := tx.Create(reservation).Error; createErr != nil {
		if !isReservationDuplicateKeyError(createErr) {
			return ReservationResult{}, createErr
		}
		// Production MySQL and the hermetic SQLite harness both keep the
		// transaction usable after a unique-key statement failure. Re-read the
		// winner under lock and reject any same-key/different-request replay.
		var existing model.CreditReservation
		lookupErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uid = ? AND idempotency_key = ?", req.UID, req.IdempotencyKey).
			First(&existing).Error
		if lookupErr != nil {
			return ReservationResult{}, createErr
		}
		if !reservationReplayMatches(existing, req, digest) {
			return ReservationResult{}, ErrReservationReplayConflict
		}
		return ReservationResult{Reservation: &existing, Created: false}, nil
	}

	// Canonical financial order: Reservation -> Project -> Pack(id ASC).
	if req.ProjectID > 0 && req.Reserved > 0 {
		if err := project.NewRepository(tx).AddBudgetSpent(req.ProjectID, uint(req.UID), req.Reserved); err != nil {
			return ReservationResult{}, err
		}
	}

	allocations := make([]creditsPackAllocation, 0)
	if req.Reserved > 0 {
		allocations, err = s.creditsPack.ReserveCreditsDetailedAtTx(tx, req.UID, req.Reserved, now)
		if err != nil {
			return ReservationResult{}, err
		}
	}
	if len(allocations) > 0 {
		rows := make([]model.CreditReservationAllocation, 0, len(allocations))
		for _, allocation := range allocations {
			rows = append(rows, model.CreditReservationAllocation{
				ReservationID: reservation.Id,
				PackID:        allocation.PackID,
				Credits:       allocation.Credits,
			})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return ReservationResult{}, err
		}
	}
	return ReservationResult{Reservation: reservation, Created: true}, nil
}

func isReservationDuplicateKeyError(err error) bool {
	if isDuplicateKeyError(err) {
		return true
	}
	// modernc SQLite exposes extended result codes through Code(). Limit the
	// replay path to UNIQUE/PRIMARY KEY violations; CHECK, trigger and I/O
	// failures must never be disguised as an idempotent winner.
	var sqliteErr interface{ Code() int }
	if !errors.As(err, &sqliteErr) {
		return false
	}
	const (
		sqliteConstraintPrimaryKey = 1555
		sqliteConstraintUnique     = 2067
	)
	return sqliteErr.Code() == sqliteConstraintPrimaryKey || sqliteErr.Code() == sqliteConstraintUnique
}

func reservationReplayMatches(existing model.CreditReservation, req ReservationRequest, digest string) bool {
	if existing.RequestDigest != "" {
		return existing.RequestDigest == digest
	}
	// Legacy rows predate request_digest. Their persisted immutable fields are
	// still compared; TTL cannot be reconstructed and is intentionally omitted.
	return existing.UID == req.UID && existing.Tool == req.Tool &&
		existing.IdempotencyKey == req.IdempotencyKey && existing.QuoteID == req.QuoteID &&
		existing.Reserved == req.Reserved && existing.ProjectID == req.ProjectID
}

// HoldForReview exempts one exact reservation from ordinary TTL reclamation.
// Exact replay is a no-op; a different Review/Settlement tuple fails closed.
func (s *CreditReservationService) HoldForReview(tx *gorm.DB, reservationID uint, hold ReservationReviewHold) error {
	return s.holdForReview(tx, reservationID, hold, true)
}

// HoldForReviewAfterTurnAuthorization is reserved for a caller that already
// owns and has validated the durable Turn terminalization boundary. Agent
// reservations are excluded from the generic TTL sweeper after binding, so an
// Attempt authorized before expiry must still be able to enter Review after a
// long execution. All state, hold-tuple and row-lock checks remain unchanged.
func (s *CreditReservationService) HoldForReviewAfterTurnAuthorization(
	tx *gorm.DB,
	reservationID uint,
	hold ReservationReviewHold,
) error {
	return s.holdForReview(tx, reservationID, hold, false)
}

func (s *CreditReservationService) holdForReview(
	tx *gorm.DB,
	reservationID uint,
	hold ReservationReviewHold,
	requireUnexpired bool,
) error {
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}
	if err := hold.validate(); err != nil {
		return err
	}
	var reservation model.CreditReservation
	if err := lockReservation(tx, reservationID, &reservation); err != nil {
		return err
	}
	if reservationHasHoldBinding(reservation) {
		if reservationHoldMatches(reservation, hold) {
			return nil
		}
		return ErrReservationReviewConflict
	}
	if reservation.Status == model.CreditReservationStatusRefundPending {
		return ErrReservationSettlementConflict
	}
	if reservation.IsTerminal() {
		return reservationTerminalError(reservation.Status)
	}
	if reservation.Status != model.CreditReservationStatusReserved {
		return ErrReservationSettlementConflict
	}
	now, err := databaseReservationNow(tx)
	if err != nil {
		return err
	}
	if requireUnexpired && !reservation.ExpiresAt.After(now) {
		return ErrReservationTTLExpired
	}
	return tx.Model(&reservation).Updates(map[string]interface{}{
		"status":              model.CreditReservationStatusReviewHold,
		"hold_review_id":      hold.ReviewID,
		"hold_settlement_key": hold.SettlementKey,
		"hold_request_digest": hold.RequestDigest,
		"review_held_at":      &now,
		"state_changed_at":    &now,
		"state_version":       gorm.Expr("state_version + 1"),
	}).Error
}

// HoldForReviewWithResult applies the existing exact review hold transition
// and then returns the actual durable state under the same transaction and row
// lock. An error never carries a speculative snapshot.
func (s *CreditReservationService) HoldForReviewWithResult(
	tx *gorm.DB,
	reservationID uint,
	hold ReservationReviewHold,
) (ReservationSettlementSnapshot, error) {
	if err := s.HoldForReview(tx, reservationID, hold); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return s.LockSettlementSnapshot(tx, reservationID)
}

// HoldForReviewAfterTurnAuthorizationWithResult is the structured result
// variant used by the Turn-to-Reservation settlement authority.
func (s *CreditReservationService) HoldForReviewAfterTurnAuthorizationWithResult(
	tx *gorm.DB,
	reservationID uint,
	hold ReservationReviewHold,
) (ReservationSettlementSnapshot, error) {
	if err := s.HoldForReviewAfterTurnAuthorization(tx, reservationID, hold); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return s.LockSettlementSnapshot(tx, reservationID)
}

func reservationHoldMatches(reservation model.CreditReservation, hold ReservationReviewHold) bool {
	return reservation.HoldReviewID == hold.ReviewID &&
		reservation.HoldSettlementKey == hold.SettlementKey &&
		reservation.HoldRequestDigest == hold.RequestDigest
}

func reservationHasHoldBinding(reservation model.CreditReservation) bool {
	return reservation.HoldReviewID != "" || reservation.HoldSettlementKey != "" ||
		reservation.HoldRequestDigest != "" || reservation.ReviewHeldAt != nil
}

func enforceReservationHold(reservation model.CreditReservation, hold *ReservationReviewHold) error {
	if !reservationHasHoldBinding(reservation) {
		if hold != nil {
			return ErrReservationReviewConflict
		}
		return nil
	}
	if hold == nil {
		return ErrReservationReviewPending
	}
	if !reservationHoldMatches(reservation, *hold) {
		return ErrReservationReviewConflict
	}
	return nil
}

// Finalize settles an ordinary, non-review reservation. Exact replay of the
// same used amount succeeds; conflicting terminal outcomes are distinguishable.
func (s *CreditReservationService) Finalize(tx *gorm.DB, reservationID uint, used int) error {
	return s.finalize(tx, reservationID, used, nil, true)
}

// FinalizeAfterTurnAuthorization settles a Reservation whose owning Turn has
// already crossed the durable terminal authorization boundary. TTL gates new
// execution, not payment for an Attempt that was authorized before expiry.
func (s *CreditReservationService) FinalizeAfterTurnAuthorization(
	tx *gorm.DB,
	reservationID uint,
	used int,
) error {
	return s.finalize(tx, reservationID, used, nil, false)
}

// FinalizeWithResult preserves Finalize's idempotency and error semantics while
// exposing whether the durable outcome is finalized or still refund_pending.
func (s *CreditReservationService) FinalizeWithResult(
	tx *gorm.DB,
	reservationID uint,
	used int,
) (ReservationSettlementSnapshot, error) {
	if err := s.Finalize(tx, reservationID, used); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return s.LockSettlementSnapshot(tx, reservationID)
}

// FinalizeAfterTurnAuthorizationWithResult is the structured result variant
// used by the immutable Turn settlement ledger.
func (s *CreditReservationService) FinalizeAfterTurnAuthorizationWithResult(
	tx *gorm.DB,
	reservationID uint,
	used int,
) (ReservationSettlementSnapshot, error) {
	if err := s.FinalizeAfterTurnAuthorization(tx, reservationID, used); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return s.LockSettlementSnapshot(tx, reservationID)
}

// FinalizeReview settles the exact review-held reservation without copying
// Agent Review's metered sub-state into the Credits table.
func (s *CreditReservationService) FinalizeReview(tx *gorm.DB, reservationID uint, hold ReservationReviewHold, used int) error {
	if err := hold.validate(); err != nil {
		return err
	}
	return s.finalize(tx, reservationID, used, &hold, false)
}

// FinalizeReviewWithResult settles only the exact held Review tuple and returns
// the row's durable post-transition state. A pending refund is returned as
// refund_pending rather than being misreported as finalized.
func (s *CreditReservationService) FinalizeReviewWithResult(
	tx *gorm.DB,
	reservationID uint,
	hold ReservationReviewHold,
	used int,
) (ReservationSettlementSnapshot, error) {
	if err := s.FinalizeReview(tx, reservationID, hold, used); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return s.LockSettlementSnapshot(tx, reservationID)
}

func (s *CreditReservationService) finalize(
	tx *gorm.DB,
	reservationID uint,
	used int,
	hold *ReservationReviewHold,
	requireUnexpired bool,
) error {
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}
	if used < 0 {
		return fmt.Errorf("used must be non-negative")
	}
	var reservation model.CreditReservation
	if err := lockReservation(tx, reservationID, &reservation); err != nil {
		return err
	}
	if err := enforceReservationHold(reservation, hold); err != nil {
		return err
	}
	if used > reservation.Reserved {
		return fmt.Errorf("used (%d) exceeds reserved (%d)", used, reservation.Reserved)
	}
	if reservation.Status == model.CreditReservationStatusFinalized {
		if reservation.Used == used {
			return nil
		}
		return ErrReservationSettlementConflict
	}
	if reservation.Status == model.CreditReservationStatusReleased {
		return ErrReservationAlreadyReleased
	}
	if reservation.Status == model.CreditReservationStatusExpired {
		return ErrReservationExpired
	}
	if reservation.Status == model.CreditReservationStatusRefundPending {
		if !refundIntentMatches(reservation, model.CreditReservationStatusFinalized, used) {
			return ErrReservationSettlementConflict
		}
		now, err := databaseReservationNow(tx)
		if err != nil {
			return err
		}
		if reservation.NextRefundAt != nil && reservation.NextRefundAt.After(now) {
			return nil
		}
		return s.attemptPendingRefundLocked(tx, &reservation, now)
	}
	if reservation.Status != model.CreditReservationStatusReserved && reservation.Status != model.CreditReservationStatusReviewHold {
		return ErrReservationSettlementConflict
	}
	if requireUnexpired && reservation.Status == model.CreditReservationStatusReserved {
		now, err := databaseReservationNow(tx)
		if err != nil {
			return err
		}
		if !reservation.ExpiresAt.After(now) {
			return ErrReservationTTLExpired
		}
	}

	now, err := databaseReservationNow(tx)
	if err != nil {
		return err
	}
	refund := reservation.Reserved - used
	if refund == 0 {
		return markReservationTerminal(tx, &reservation, model.CreditReservationStatusFinalized, used, now)
	}
	if err := s.queueRefundLocked(tx, &reservation, model.CreditReservationStatusFinalized, used, refund, now); err != nil {
		return err
	}
	return s.attemptPendingRefundLocked(tx, &reservation, now)
}

// Release refunds an ordinary reservation in full. Review-held reservations
// require ReleaseReview so a legacy caller cannot bypass the exact hold owner.
func (s *CreditReservationService) Release(tx *gorm.DB, reservationID uint) error {
	return s.release(tx, reservationID, nil)
}

// ReleaseWithResult preserves Release's exact replay semantics and returns the
// actual released or refund_pending state held by the database row.
func (s *CreditReservationService) ReleaseWithResult(
	tx *gorm.DB,
	reservationID uint,
) (ReservationSettlementSnapshot, error) {
	if err := s.Release(tx, reservationID); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return s.LockSettlementSnapshot(tx, reservationID)
}

func (s *CreditReservationService) ReleaseReview(tx *gorm.DB, reservationID uint, hold ReservationReviewHold) error {
	if err := hold.validate(); err != nil {
		return err
	}
	return s.release(tx, reservationID, &hold)
}

// ReleaseReviewWithResult releases only the exact held Review tuple and
// returns the durable released or refund_pending state.
func (s *CreditReservationService) ReleaseReviewWithResult(
	tx *gorm.DB,
	reservationID uint,
	hold ReservationReviewHold,
) (ReservationSettlementSnapshot, error) {
	if err := s.ReleaseReview(tx, reservationID, hold); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return s.LockSettlementSnapshot(tx, reservationID)
}

func (s *CreditReservationService) release(tx *gorm.DB, reservationID uint, hold *ReservationReviewHold) error {
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}
	var reservation model.CreditReservation
	if err := lockReservation(tx, reservationID, &reservation); err != nil {
		return err
	}
	if err := enforceReservationHold(reservation, hold); err != nil {
		return err
	}
	if reservation.Status == model.CreditReservationStatusReleased {
		return nil
	}
	if reservation.Status == model.CreditReservationStatusFinalized {
		return ErrReservationAlreadyFinalized
	}
	if reservation.Status == model.CreditReservationStatusExpired {
		return ErrReservationExpired
	}
	if reservation.Status == model.CreditReservationStatusRefundPending {
		if !refundIntentMatches(reservation, model.CreditReservationStatusReleased, 0) {
			return ErrReservationSettlementConflict
		}
		now, err := databaseReservationNow(tx)
		if err != nil {
			return err
		}
		if reservation.NextRefundAt != nil && reservation.NextRefundAt.After(now) {
			return nil
		}
		return s.attemptPendingRefundLocked(tx, &reservation, now)
	}
	if reservation.Status != model.CreditReservationStatusReserved && reservation.Status != model.CreditReservationStatusReviewHold {
		return ErrReservationSettlementConflict
	}
	now, err := databaseReservationNow(tx)
	if err != nil {
		return err
	}
	if reservation.Reserved == 0 {
		return markReservationTerminal(tx, &reservation, model.CreditReservationStatusReleased, 0, now)
	}
	if err := s.queueRefundLocked(tx, &reservation, model.CreditReservationStatusReleased, 0, reservation.Reserved, now); err != nil {
		return err
	}
	return s.attemptPendingRefundLocked(tx, &reservation, now)
}

func lockReservation(tx *gorm.DB, reservationID uint, out *model.CreditReservation) error {
	if reservationID == 0 {
		return gorm.ErrRecordNotFound
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reservationID).First(out).Error
}

// LockSettlementSnapshot locks one Reservation in the caller-owned
// transaction and returns a detached value snapshot of its durable settlement
// state. It never falls back to a global database handle.
func (s *CreditReservationService) LockSettlementSnapshot(
	tx *gorm.DB,
	reservationID uint,
) (ReservationSettlementSnapshot, error) {
	if tx == nil {
		return ReservationSettlementSnapshot{}, fmt.Errorf("nil transaction")
	}
	var reservation model.CreditReservation
	if err := lockReservation(tx, reservationID, &reservation); err != nil {
		return ReservationSettlementSnapshot{}, err
	}
	return reservationSettlementSnapshot(reservation), nil
}

func reservationSettlementSnapshot(reservation model.CreditReservation) ReservationSettlementSnapshot {
	return ReservationSettlementSnapshot{
		ReservationID:  reservation.Id,
		UID:            reservation.UID,
		Tool:           reservation.Tool,
		IdempotencyKey: reservation.IdempotencyKey,
		RequestDigest:  reservation.RequestDigest,
		QuoteID:        reservation.QuoteID,
		ProjectID:      reservation.ProjectID,

		Status:   reservation.Status,
		Reserved: reservation.Reserved,
		Used:     reservation.Used,

		HoldReviewID:      reservation.HoldReviewID,
		HoldSettlementKey: reservation.HoldSettlementKey,
		HoldRequestDigest: reservation.HoldRequestDigest,
		ReviewHeldAt:      copyReservationSettlementTime(reservation.ReviewHeldAt),

		RefundTargetStatus:  reservation.RefundTargetStatus,
		RefundTargetUsed:    copyReservationSettlementInt(reservation.RefundTargetUsed),
		RefundDue:           reservation.RefundDue,
		RefundAttempts:      reservation.RefundAttempts,
		NextRefundAt:        copyReservationSettlementTime(reservation.NextRefundAt),
		LastRefundErrorCode: reservation.LastRefundErrorCode,

		ExpiresAt:      reservation.ExpiresAt,
		FinalizedAt:    copyReservationSettlementTime(reservation.FinalizedAt),
		ReleasedAt:     copyReservationSettlementTime(reservation.ReleasedAt),
		StateChangedAt: copyReservationSettlementTime(reservation.StateChangedAt),
		StateVersion:   reservation.StateVersion,
	}
}

func copyReservationSettlementTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyReservationSettlementInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func reservationTerminalError(status string) error {
	switch status {
	case model.CreditReservationStatusFinalized:
		return ErrReservationAlreadyFinalized
	case model.CreditReservationStatusReleased:
		return ErrReservationAlreadyReleased
	case model.CreditReservationStatusExpired:
		return ErrReservationExpired
	default:
		return ErrReservationSettlementConflict
	}
}

func refundIntentMatches(reservation model.CreditReservation, target string, targetUsed int) bool {
	return reservation.RefundTargetStatus == target && reservation.RefundTargetUsed != nil &&
		*reservation.RefundTargetUsed == targetUsed &&
		reservation.RefundDue == reservation.Reserved-targetUsed
}

func (s *CreditReservationService) queueRefundLocked(
	tx *gorm.DB,
	reservation *model.CreditReservation,
	target string,
	targetUsed int,
	refund int,
	now time.Time,
) error {
	if refund <= 0 || targetUsed < 0 || targetUsed > reservation.Reserved || refund != reservation.Reserved-targetUsed {
		return ErrReservationSettlementConflict
	}
	if target != model.CreditReservationStatusFinalized &&
		target != model.CreditReservationStatusReleased &&
		target != model.CreditReservationStatusExpired {
		return ErrReservationSettlementConflict
	}
	if (target == model.CreditReservationStatusReleased || target == model.CreditReservationStatusExpired) && targetUsed != 0 {
		return ErrReservationSettlementConflict
	}
	if err := tx.Model(reservation).Updates(map[string]interface{}{
		"status":                 model.CreditReservationStatusRefundPending,
		"refund_target_status":   target,
		"refund_target_used":     targetUsed,
		"refund_due":             refund,
		"next_refund_at":         &now,
		"last_refund_error_code": nil,
		"state_changed_at":       &now,
		"state_version":          gorm.Expr("state_version + 1"),
	}).Error; err != nil {
		return err
	}
	reservation.Status = model.CreditReservationStatusRefundPending
	reservation.RefundTargetStatus = target
	reservation.RefundTargetUsed = &targetUsed
	reservation.RefundDue = refund
	reservation.NextRefundAt = &now
	reservation.LastRefundErrorCode = ""
	return nil
}

// attemptPendingRefundLocked keeps the durable refund intent outside a
// savepoint and rolls every Project/Pack mutation back to that savepoint on
// failure. A failed attempt therefore commits only refund_pending metadata,
// never a partial refund or a false terminal outcome.
func (s *CreditReservationService) attemptPendingRefundLocked(tx *gorm.DB, reservation *model.CreditReservation, now time.Time) error {
	if reservation.Status != model.CreditReservationStatusRefundPending ||
		reservation.RefundTargetUsed == nil || reservation.RefundDue <= 0 ||
		!refundIntentMatches(*reservation, reservation.RefundTargetStatus, *reservation.RefundTargetUsed) {
		return ErrReservationSettlementConflict
	}

	savepoint := fmt.Sprintf("credit_refund_%d", reservation.Id)
	if err := tx.SavePoint(savepoint).Error; err != nil {
		return fmt.Errorf("create credit refund savepoint: %w", err)
	}
	refundErr := s.refundFinancialRowsLocked(tx, reservation)
	if refundErr != nil {
		if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
			return fmt.Errorf("rollback failed credit refund: %w", rollbackErr)
		}
		attempts := reservation.RefundAttempts + 1
		next := now.Add(refundRetryBackoff(attempts))
		code := classifyReservationRefundError(refundErr)
		if err := tx.Model(reservation).Updates(map[string]interface{}{
			"refund_attempts":        attempts,
			"next_refund_at":         &next,
			"last_refund_error_code": code,
			"state_changed_at":       &now,
			"state_version":          gorm.Expr("state_version + 1"),
		}).Error; err != nil {
			return err
		}
		reservation.RefundAttempts = attempts
		reservation.NextRefundAt = &next
		reservation.LastRefundErrorCode = code
		globals.Error(fmt.Sprintf("[CreditReservation] refund pending reservation=%d code=%s attempt=%d", reservation.Id, code, attempts))
		return nil
	}
	return markReservationTerminal(tx, reservation, reservation.RefundTargetStatus, *reservation.RefundTargetUsed, now)
}

func (s *CreditReservationService) refundFinancialRowsLocked(tx *gorm.DB, reservation *model.CreditReservation) error {
	// Project must be locked/refunded before any Pack lock or mutation.
	if reservation.ProjectID > 0 {
		if err := project.NewRepository(tx).RefundBudgetSpentExact(
			reservation.ProjectID, uint(reservation.UID), reservation.RefundDue,
		); err != nil {
			return fmt.Errorf("project refund: %w", err)
		}
	}
	if err := s.creditsPack.RefundAllocationsCheckedTx(
		tx, reservation.Id, reservation.UID, reservation.Reserved, reservation.RefundDue,
	); err != nil {
		return err
	}
	return nil
}

func refundRetryBackoff(attempt uint64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 6 {
		shift = 6
	}
	backoff := time.Minute * time.Duration(1<<shift)
	if backoff > maxRefundBackoff {
		return maxRefundBackoff
	}
	return backoff
}

func classifyReservationRefundError(err error) string {
	switch {
	case errors.Is(err, project.ErrBudgetRefundInvariant), errors.Is(err, gorm.ErrRecordNotFound):
		return "project_invariant"
	case errors.Is(err, ErrCreditsRefundAllocationInvalid):
		return "allocation_invalid"
	case errors.Is(err, ErrCreditsRefundAllocationIncomplete):
		return "allocation_incomplete"
	case errors.Is(err, ErrCreditsRefundPackInvariant):
		return "pack_invariant"
	default:
		return "database_error"
	}
}

func markReservationTerminal(tx *gorm.DB, reservation *model.CreditReservation, status string, used int, now time.Time) error {
	updates := map[string]interface{}{
		"used":                   used,
		"status":                 status,
		"refund_target_status":   nil,
		"refund_target_used":     nil,
		"refund_due":             0,
		"next_refund_at":         nil,
		"last_refund_error_code": nil,
		"state_changed_at":       &now,
		"state_version":          gorm.Expr("state_version + 1"),
	}
	switch status {
	case model.CreditReservationStatusFinalized:
		updates["finalized_at"] = &now
	case model.CreditReservationStatusReleased, model.CreditReservationStatusExpired:
		updates["released_at"] = &now
	default:
		return ErrReservationSettlementConflict
	}
	if err := tx.Model(reservation).Updates(updates).Error; err != nil {
		return err
	}
	reservation.Status = status
	reservation.Used = used
	reservation.RefundTargetStatus = ""
	reservation.RefundTargetUsed = nil
	reservation.RefundDue = 0
	reservation.NextRefundAt = nil
	reservation.LastRefundErrorCode = ""
	return nil
}

// FindActive returns a reservation still authorized for its operation. Review
// holds stay active past ordinary TTL. refund_pending is economically debited
// but no longer authorizes execution and is intentionally excluded.
func (s *CreditReservationService) FindActive(tx *gorm.DB, uid int, idempotencyKey string) (*model.CreditReservation, error) {
	if tx == nil {
		tx = globals.GraDBs["system"]
	}
	now, err := databaseReservationNow(tx)
	if err != nil {
		return nil, err
	}
	var reservation model.CreditReservation
	err = tx.Where(
		"uid = ? AND idempotency_key = ? AND ((status = ? AND expires_at > ?) OR status = ?)",
		uid, idempotencyKey, model.CreditReservationStatusReserved, now, model.CreditReservationStatusReviewHold,
	).First(&reservation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}

// FindForSettlement returns and locks the immutable reservation identified by
// an owner key regardless of TTL or status. Settlement callers must lock their
// owner row (GenerationTask/Turn) first, then use this method before calling
// Finalize or Release. FindActive is deliberately unsuitable here: expiry only
// revokes execution authorization; it does not erase an economically debited
// ledger row that still needs an exact terminal outcome.
func (s *CreditReservationService) FindForSettlement(tx *gorm.DB, uid int, idempotencyKey string) (*model.CreditReservation, error) {
	if tx == nil {
		return nil, fmt.Errorf("nil transaction")
	}
	if uid <= 0 || strings.TrimSpace(idempotencyKey) == "" {
		return nil, nil
	}
	var reservation model.CreditReservation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("uid = ? AND idempotency_key = ?", uid, idempotencyKey).
		First(&reservation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}

type reservationSweepOutcome int

const (
	reservationSweepSkipped reservationSweepOutcome = iota
	reservationSweepExtended
	reservationSweepTerminal
	reservationSweepRefundPending
)

// SweepExpiredReservations processes ordinary TTL rows and due refund intents.
// Agent-bound rows are owned by the Agent settlement path and are excluded
// before the generic sweeper takes any per-reservation locks. Review-held rows
// are also excluded by status and lock-time recheck. A failed refund is counted
// as failed after its durable refund_pending intent commits.
func (s *CreditReservationService) SweepExpiredReservations(parent *gorm.DB, batchSize int, safetyMargin time.Duration) (swept, failed int, err error) {
	if parent == nil {
		parent = globals.GraDBs["system"]
	}
	if batchSize <= 0 {
		batchSize = 200
	}
	now, err := databaseReservationNow(parent)
	if err != nil {
		return 0, 0, err
	}
	cutoff := now.Add(-safetyMargin)
	var rows []model.CreditReservation
	candidatePredicate := fmt.Sprintf(`
		((w_credit_reservation.status = ? AND w_credit_reservation.expires_at < ?)
			OR (w_credit_reservation.status = ? AND
				(w_credit_reservation.next_refund_at IS NULL OR w_credit_reservation.next_refund_at <= ?)))
		AND NOT EXISTS (
			SELECT 1
			FROM %s AS agent_reservation_binding
			WHERE agent_reservation_binding.reservation_id = w_credit_reservation.id
		)`, agentTurnReservationBindingTable)
	if err := parent.Where(
		candidatePredicate,
		model.CreditReservationStatusReserved, cutoff,
		model.CreditReservationStatusRefundPending, now,
	).Order("CASE WHEN status = 'refund_pending' THEN 0 ELSE 1 END, expires_at ASC, id ASC").
		Limit(batchSize).Find(&rows).Error; err != nil {
		return 0, 0, err
	}

	for i := range rows {
		outcome, sweepErr := s.sweepOne(parent, rows[i], cutoff)
		if sweepErr != nil {
			failed++
			globals.Error(fmt.Sprintf("[CreditReservation] sweep reservation=%d failed", rows[i].Id))
			continue
		}
		switch outcome {
		case reservationSweepTerminal, reservationSweepExtended:
			swept++
		case reservationSweepRefundPending:
			failed++
		}
	}
	return swept, failed, nil
}

func (s *CreditReservationService) sweepOne(parent *gorm.DB, candidate model.CreditReservation, cutoff time.Time) (outcome reservationSweepOutcome, err error) {
	err = parent.Transaction(func(tx *gorm.DB) error {
		// Legacy GenerationTask is an owner row. Lock it before Reservation so
		// task refund (Task -> Reservation) and the sweeper cannot invert.
		var task model.GenerationTask
		taskErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uid = ? AND task_id = ?", candidate.UID, candidate.IdempotencyKey).
			First(&task).Error
		if taskErr != nil && !errors.Is(taskErr, gorm.ErrRecordNotFound) {
			return taskErr
		}

		var reservation model.CreditReservation
		if err := lockReservation(tx, candidate.Id, &reservation); err != nil {
			return err
		}
		now, err := databaseReservationNow(tx)
		if err != nil {
			return err
		}
		switch reservation.Status {
		case model.CreditReservationStatusReviewHold:
			outcome = reservationSweepSkipped
			return nil
		case model.CreditReservationStatusRefundPending:
			if reservation.NextRefundAt != nil && reservation.NextRefundAt.After(now) {
				outcome = reservationSweepSkipped
				return nil
			}
			before := reservation.RefundAttempts
			if err := s.attemptPendingRefundLocked(tx, &reservation, now); err != nil {
				return err
			}
			if reservation.RefundAttempts > before {
				outcome = reservationSweepRefundPending
			} else {
				outcome = reservationSweepTerminal
			}
			return nil
		case model.CreditReservationStatusReserved:
			if !reservation.ExpiresAt.Before(cutoff) {
				outcome = reservationSweepSkipped
				return nil
			}
		default:
			outcome = reservationSweepSkipped
			return nil
		}

		if taskErr == nil && (task.Status == model.TaskStatusPending || task.Status == model.TaskStatusProcessing) {
			expires := now.Add(processingReservationExtension)
			if err := tx.Model(&reservation).Updates(map[string]interface{}{
				"expires_at":       expires,
				"state_changed_at": &now,
				"state_version":    gorm.Expr("state_version + 1"),
			}).Error; err != nil {
				return err
			}
			outcome = reservationSweepExtended
			return nil
		}

		if reservation.Reserved == 0 {
			if err := markReservationTerminal(tx, &reservation, model.CreditReservationStatusExpired, 0, now); err != nil {
				return err
			}
			outcome = reservationSweepTerminal
			return nil
		}
		if err := s.queueRefundLocked(
			tx, &reservation, model.CreditReservationStatusExpired, 0, reservation.Reserved, now,
		); err != nil {
			return err
		}
		if err := s.attemptPendingRefundLocked(tx, &reservation, now); err != nil {
			return err
		}
		if reservation.Status == model.CreditReservationStatusRefundPending {
			outcome = reservationSweepRefundPending
		} else {
			outcome = reservationSweepTerminal
		}
		return nil
	})
	return outcome, err
}
