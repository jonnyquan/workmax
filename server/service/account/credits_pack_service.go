package account

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"server/globals"
	"server/model"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrInsufficientCredits is the sentinel returned when a debit can't be
// satisfied by the user's credit packs. Callers should branch on
// errors.Is(err, ErrInsufficientCredits) to surface a 402 Payment Required;
// any other error means the DB / locking / allocation layer failed and
// should produce a 5xx with the underlying error logged.
//
// The error string is preserved as "insufficient credits" so the existing
// string-match fallback in service/tools/task_error.go (which classifies
// errors arriving from external providers, not just our own returns) keeps
// working without coordination.
var ErrInsufficientCredits = errors.New("insufficient credits")

var (
	// ErrCreditsRefundAllocationInvalid identifies malformed immutable rows.
	ErrCreditsRefundAllocationInvalid = errors.New("credit refund allocation is invalid")
	// ErrCreditsRefundAllocationIncomplete identifies a valid-looking set whose
	// total cannot authorize the Reservation's expected refund.
	ErrCreditsRefundAllocationIncomplete = errors.New("credit refund allocation set is incomplete")
	// ErrCreditsRefundPackInvariant identifies a missing or inconsistent Pack.
	ErrCreditsRefundPackInvariant = errors.New("credit refund pack invariant violation")

	// ErrRefundAllocationIntegrity means the immutable allocation set cannot
	// safely authorize a refund. Examples include duplicate Pack references,
	// non-positive allocation amounts and a sum that differs from the amount
	// frozen on the Reservation.
	ErrRefundAllocationIntegrity = errors.New("credit refund allocation integrity violation")
	// ErrRefundPackIntegrity means the allocation set names a missing or
	// internally inconsistent Pack, or the Pack no longer carries enough used
	// credits to contain the allocation being refunded.
	ErrRefundPackIntegrity = ErrCreditsRefundPackInvariant
)

// RefundIntegrityError identifies durable-data corruption separately from a
// transient database error. Callers can use errors.Is with one of the two
// sentinels above and errors.As to retain the Reservation/Pack correlation for
// an audit record without parsing an error string.
type RefundIntegrityError struct {
	Kind          error
	ReservationID uint
	PackID        uint
	Detail        string
}

func (err *RefundIntegrityError) Error() string {
	if err == nil {
		return "credit refund integrity violation"
	}
	message := "credit refund integrity violation"
	if err.Kind != nil {
		message = err.Kind.Error()
	}
	if err.PackID > 0 {
		return fmt.Sprintf("%s (reservation=%d pack=%d): %s", message, err.ReservationID, err.PackID, err.Detail)
	}
	return fmt.Sprintf("%s (reservation=%d): %s", message, err.ReservationID, err.Detail)
}

func (err *RefundIntegrityError) Unwrap() []error {
	if err == nil {
		return nil
	}
	if err.Kind == nil {
		return nil
	}
	switch err.Kind {
	case ErrCreditsRefundAllocationInvalid, ErrCreditsRefundAllocationIncomplete:
		return []error{err.Kind, ErrRefundAllocationIntegrity}
	default:
		return []error{err.Kind}
	}
}

func refundIntegrityError(kind error, reservationID, packID uint, format string, args ...any) error {
	return &RefundIntegrityError{
		Kind: kind, ReservationID: reservationID, PackID: packID,
		Detail: fmt.Sprintf(format, args...),
	}
}

type CreditsPackService struct{}

type subscriptionEnsureCacheEntry struct {
	PlanKey            string
	MemberSubscription string
	MemberStartTime    time.Time
	MemberEndTime      time.Time
	SourceID           string
	ExpiresAt          time.Time
}

var subscriptionEnsureCache sync.Map

type creditsPackAllocation struct {
	PackID   uint
	Credits  int
	Priority int
}

func NewCreditsPackService() *CreditsPackService {
	return &CreditsPackService{}
}

func (s *CreditsPackService) getPlanCredits(planKey string) int {
	plan, ok := globals.GraConf.Stripe.Plans[planKey]
	if !ok {
		return 0
	}
	if plan.MonthlyCredits > 0 {
		return plan.MonthlyCredits
	}
	return plan.Credits
}

func (s *CreditsPackService) isDeferredMonthlyCreditsPlan(planKey string) bool {
	planKey = strings.ToLower(strings.TrimSpace(planKey))
	return strings.Contains(planKey, "annual") || strings.Contains(planKey, "yearly") || strings.Contains(planKey, "lifetime")
}

func (s *CreditsPackService) resolveActivePlanTx(tx *gorm.DB, uid int, now time.Time) (model.User, string, error) {
	var user model.User
	if err := tx.Select("id, member, member_start_time, member_end_time, member_subscription").Where("id = ?", uid).First(&user).Error; err != nil {
		return model.User{}, "", err
	}

	if !user.MemberStartTime.IsZero() {
		if planKey, ok := getCachedSubscriptionPlan(user, now); ok {
			return user, planKey, nil
		}
	}

	user, planKey, _, err := s.resolveActivePlanForUserTx(tx, user, uid, now)
	return user, planKey, err
}

func (s *CreditsPackService) resolveActivePlanForUserTx(tx *gorm.DB, user model.User, uid int, now time.Time) (model.User, string, int, error) {
	if user.Member <= model.MEMBER_SUBSCRIPTION_FREE {
		return user, "", 0, nil
	}

	if !user.MemberEndTime.IsZero() && !user.MemberEndTime.After(now) {
		return user, "", 0, nil
	}

	// member_subscription stores the external Stripe subscription identity
	// (for example sub_123, or canceled_sub_123), never our configured plan
	// key. The durable billing Order owns the plan identity. Reading the Stripe
	// ID as a plan key silently disables annual/lifetime monthly replenishment
	// after the first cycle.
	var order model.Order
	err := tx.
		Where("uid = ? AND order_type = ? AND status = ? AND product_id <> ?", uid, model.ORDER_TYPE_MEMBER, model.STATUS_COMPLETE, "free").
		Order("pay_time DESC, id DESC").
		First(&order).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return user, "", 0, nil
		}
		return user, "", 0, err
	}

	planKey := strings.TrimSpace(order.ProductID)
	credits := order.CreditsAmount
	if credits <= 0 && planKey != "" {
		// Explicit legacy compatibility only: new paid Orders freeze a positive
		// credits_amount. Historical zero rows must use the currently configured
		// allowance until they are reconciled/backfilled.
		credits = s.getPlanCredits(planKey)
	}
	if s.isDeferredMonthlyCreditsPlan(planKey) && credits <= 0 {
		return user, "", 0, fmt.Errorf("positive deferred subscription credits are not frozen for plan %s", planKey)
	}
	if planKey == "" || !s.isDeferredMonthlyCreditsPlan(planKey) || !user.MemberStartTime.IsZero() {
		return user, planKey, credits, nil
	}

	// Old users may predate member_start_time. Derive a stable cycle anchor from
	// the earliest paid Order for the current subscription+plan. Falling back to
	// `now` would mint a fresh date-keyed annual Pack every day.
	anchorQuery := tx.Where(
		"uid = ? AND order_type = ? AND status = ? AND product_id = ? AND pay_time IS NOT NULL",
		uid, model.ORDER_TYPE_MEMBER, model.STATUS_COMPLETE, planKey,
	)
	externalSubscriptionID := strings.TrimPrefix(strings.TrimSpace(user.MemberSubscription), "canceled_")
	if externalSubscriptionID != "" {
		anchorQuery = anchorQuery.Where("subscription_id = ?", externalSubscriptionID)
	}
	var anchorOrder model.Order
	if err := anchorQuery.Order("pay_time ASC, id ASC").First(&anchorOrder).Error; err != nil {
		return user, "", 0, fmt.Errorf("resolve deferred subscription cycle anchor: %w", err)
	}
	if anchorOrder.PayTime.IsZero() || anchorOrder.PayTime.After(now) {
		return user, "", 0, fmt.Errorf("deferred subscription cycle anchor is invalid for user %d", uid)
	}
	user.MemberStartTime = anchorOrder.PayTime
	return user, planKey, credits, nil
}

func addCalendarMonthsClamped(anchor time.Time, months int) time.Time {
	year, month, day := anchor.Date()
	hour, minute, second := anchor.Clock()
	targetFirst := time.Date(year, month+time.Month(months), 1, hour, minute, second, anchor.Nanosecond(), anchor.Location())
	lastDay := time.Date(targetFirst.Year(), targetFirst.Month()+1, 0, hour, minute, second, anchor.Nanosecond(), anchor.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(targetFirst.Year(), targetFirst.Month(), day, hour, minute, second, anchor.Nanosecond(), anchor.Location())
}

func subscriptionCycleBounds(start, now time.Time) (time.Time, time.Time, error) {
	if start.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("subscription cycle anchor is required")
	}
	loc := now.Location()
	start = start.In(loc)
	now = now.In(loc)
	if now.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("subscription cycle anchor is after database time")
	}
	months := (now.Year()-start.Year())*12 + int(now.Month()-start.Month())
	cycleStart := addCalendarMonthsClamped(start, months)
	if cycleStart.After(now) {
		months--
		cycleStart = addCalendarMonthsClamped(start, months)
	}
	cycleEnd := addCalendarMonthsClamped(start, months+1)
	return cycleStart, cycleEnd, nil
}

func (s *CreditsPackService) ensureCurrentSubscriptionCreditsTx(tx *gorm.DB, uid int, now time.Time) error {
	user, err := lockCreditsOwnerUserTx(tx, uid)
	if err != nil {
		return err
	}
	user, planKey, credits, err := s.resolveActivePlanForUserTx(tx, user, uid, now)
	if err != nil {
		return err
	}
	if err := lockExistingCreditsPacksTx(tx, uid); err != nil {
		return err
	}
	return s.ensureCurrentSubscriptionCreditsForUserTx(tx, user, uid, planKey, credits, now)
}

func lockCreditsOwnerUserTx(tx *gorm.DB, uid int) (model.User, error) {
	var user model.User
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "ban", "ban_note", "member", "member_start_time", "member_end_time", "member_subscription").
		Where("id = ?", uid).
		First(&user).Error
	return user, err
}

func lockExistingCreditsPacksTx(tx *gorm.DB, uid int) error {
	var existingPackIDs []uint
	return tx.Model(&model.CreditsPack{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("uid = ?", uid).
		Order("id ASC").
		Pluck("id", &existingPackIDs).Error
}

func (s *CreditsPackService) ensureCurrentSubscriptionCreditsForUserTx(tx *gorm.DB, user model.User, uid int, planKey string, credits int, now time.Time) error {
	if user.Member <= model.MEMBER_SUBSCRIPTION_FREE {
		return nil
	}
	if !user.MemberEndTime.IsZero() && !user.MemberEndTime.After(now) {
		return nil
	}
	if planKey == "" || !s.isDeferredMonthlyCreditsPlan(planKey) {
		return nil
	}

	if credits <= 0 {
		return fmt.Errorf("positive deferred subscription credits are not frozen for plan %s", planKey)
	}

	cycleStart, cycleEnd, err := subscriptionCycleBounds(user.MemberStartTime, now)
	if err != nil {
		return err
	}
	if !user.MemberEndTime.IsZero() && !user.MemberEndTime.After(cycleStart) {
		return nil
	}

	// A cycle Pack owns the full calendar cycle. Membership end is a separate
	// availability gate; clipping Pack expiry to the pre-renewal member end makes
	// an early renewal reinterpret the same immutable cycle identity.
	expiresAt := cycleEnd

	// Cycle identity is deliberately independent from planKey. A provider-side
	// upgrade can change the plan during an already-funded cycle; including the
	// new plan in the key would mint a second full Pack while the old plan Pack is
	// still spendable. The User owner plus exact anchored cycle start is the
	// financial identity, while planKey remains descriptive metadata.
	sourceID := "cycle:" + cycleStart.UTC().Format("20060102T150405.000000000Z")
	remark := fmt.Sprintf("subscription credits (%s)", planKey)
	// Never use the process cache as proof that the Pack row committed. This
	// method commonly runs inside a larger reservation transaction; a later
	// insufficient-credit or allocation error may roll that transaction back
	// after rememberSubscriptionCycleEnsured runs. The DB lookup below is the
	// authoritative check, while the cache remains only a plan-resolution hint.

	var currentPack model.CreditsPack
	err = tx.
		Select("id", "credits_total", "credits_used", "expires_at").
		Where("uid = ? AND source_type = ? AND source_id = ?", uid, model.CreditsSourceSubscription, sourceID).
		First(&currentPack).Error
	if err == nil {
		// An already-created cycle is the durable entitlement snapshot for that
		// cycle. A later renewal/upgrade may change the latest Order allowance but
		// only the next absent cycle may adopt it. Validate internal integrity, not
		// today's mutable entitlement against historical frozen total/expiry.
		if currentPack.CreditsTotal <= 0 || currentPack.CreditsUsed < 0 ||
			currentPack.CreditsUsed > currentPack.CreditsTotal || currentPack.ExpiresAt == nil {
			return fmt.Errorf("subscription cycle Pack %d is internally invalid", currentPack.Id)
		}
		if currentPack.ExpiresAt.After(cycleEnd) {
			return fmt.Errorf("subscription cycle Pack %d overlaps the next cycle", currentPack.Id)
		}
		rememberSubscriptionCycleEnsured(user, planKey, sourceID, expiresAt, now)
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}

	// Compatibility and plan-switch gate: old releases used planKey:date and
	// per-Order source identities. Preserve one still-active subscription Pack
	// instead of adding a second full allowance for the new plan. More than one
	// active Pack is already an ambiguous overgrant and must be reconciled rather
	// than silently compounded.
	var activePacks []model.CreditsPack
	if err := tx.
		Select("id", "credits_total", "credits_used", "expires_at").
		Where("uid = ? AND source_type = ? AND expires_at IS NOT NULL AND expires_at > ?", uid, model.CreditsSourceSubscription, cycleStart).
		Order("id ASC").
		Find(&activePacks).Error; err != nil {
		return err
	}
	if len(activePacks) > 1 {
		return fmt.Errorf("user %d has multiple active subscription Packs", uid)
	}
	if len(activePacks) == 1 {
		active := activePacks[0]
		if active.CreditsTotal <= 0 || active.CreditsUsed < 0 ||
			active.CreditsUsed > active.CreditsTotal || active.ExpiresAt == nil {
			return fmt.Errorf("subscription cycle Pack %d is internally invalid", active.Id)
		}
		if active.ExpiresAt.After(cycleEnd) {
			return fmt.Errorf("subscription cycle Pack %d overlaps the next cycle", active.Id)
		}
		return nil
	}

	// Historical singleton subscription Packs are immutable ledger evidence.
	// Never retag one into a new cycle: credits_used may contain finalized spend
	// or live Reservation allocations whose later refund must remain attached to
	// the original Pack. A fresh cycle always receives a fresh Pack.
	pack := model.CreditsPack{
		UID:          uid,
		SourceType:   model.CreditsSourceSubscription,
		SourceID:     sourceID,
		CreditsTotal: credits,
		CreditsUsed:  0,
		ExpiresAt:    &expiresAt,
		Remark:       remark,
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "uid"}, {Name: "source_type"}, {Name: "source_id"}},
		DoNothing: true,
	}).Create(&pack).Error; err != nil {
		return err
	}
	rememberSubscriptionCycleEnsured(user, planKey, sourceID, expiresAt, now)
	return nil
}

func subscriptionEnsureCacheKey(uid int) string {
	return fmt.Sprintf("subscription-cycle:%d", uid)
}

func getCachedSubscriptionPlan(user model.User, now time.Time) (string, bool) {
	entry, ok := getSubscriptionEnsureCacheEntry(user, now)
	if !ok {
		return "", false
	}
	return entry.PlanKey, true
}

func isSubscriptionCycleEnsured(user model.User, planKey, sourceID string, now time.Time) bool {
	entry, ok := getSubscriptionEnsureCacheEntry(user, now)
	if !ok {
		return false
	}
	return entry.PlanKey == planKey && entry.SourceID == sourceID
}

func getSubscriptionEnsureCacheEntry(user model.User, now time.Time) (subscriptionEnsureCacheEntry, bool) {
	raw, ok := subscriptionEnsureCache.Load(subscriptionEnsureCacheKey(int(user.Id)))
	if !ok {
		return subscriptionEnsureCacheEntry{}, false
	}
	entry, ok := raw.(subscriptionEnsureCacheEntry)
	if !ok || !entry.ExpiresAt.After(now) {
		subscriptionEnsureCache.Delete(subscriptionEnsureCacheKey(int(user.Id)))
		return subscriptionEnsureCacheEntry{}, false
	}
	if entry.MemberSubscription != user.MemberSubscription ||
		!entry.MemberStartTime.Equal(user.MemberStartTime) ||
		!entry.MemberEndTime.Equal(user.MemberEndTime) {
		subscriptionEnsureCache.Delete(subscriptionEnsureCacheKey(int(user.Id)))
		return subscriptionEnsureCacheEntry{}, false
	}
	return entry, true
}

func rememberSubscriptionCycleEnsured(user model.User, planKey, sourceID string, packExpiresAt, now time.Time) {
	cacheExpiresAt := now.Add(10 * time.Minute)
	if packExpiresAt.Before(cacheExpiresAt) {
		cacheExpiresAt = packExpiresAt
	}
	if !user.MemberEndTime.IsZero() && user.MemberEndTime.Before(cacheExpiresAt) {
		cacheExpiresAt = user.MemberEndTime
	}
	subscriptionEnsureCache.Store(subscriptionEnsureCacheKey(int(user.Id)), subscriptionEnsureCacheEntry{
		PlanKey:            planKey,
		MemberSubscription: user.MemberSubscription,
		MemberStartTime:    user.MemberStartTime,
		MemberEndTime:      user.MemberEndTime,
		SourceID:           sourceID,
		ExpiresAt:          cacheExpiresAt,
	})
}

func isSubscriptionUserActive(user model.User, now time.Time) bool {
	if user.Member <= model.MEMBER_SUBSCRIPTION_FREE {
		return false
	}
	return user.MemberEndTime.IsZero() || user.MemberEndTime.After(now)
}

func (s *CreditsPackService) isSubscriptionCreditsActiveTx(tx *gorm.DB, uid int, now time.Time) bool {
	var user model.User
	if err := tx.Select("member, member_end_time").Where("id = ?", uid).First(&user).Error; err != nil {
		// Fail safe: when user state is unknown, do not treat subscription credits as active.
		return false
	}

	if user.Member <= model.MEMBER_SUBSCRIPTION_FREE {
		return false
	}

	if !user.MemberEndTime.IsZero() && !user.MemberEndTime.After(now) {
		return false
	}

	return true
}

func (s *CreditsPackService) CreatePackTx(tx *gorm.DB, uid int, sourceType, sourceID string, credits int, expiresAt *time.Time, remark string) error {
	if credits <= 0 {
		return nil
	}
	pack := model.CreditsPack{
		UID:          uid,
		SourceType:   sourceType,
		SourceID:     sourceID,
		CreditsTotal: credits,
		CreditsUsed:  0,
		ExpiresAt:    expiresAt,
		Remark:       remark,
	}
	return tx.Create(&pack).Error
}

func (s *CreditsPackService) AddToPackTx(tx *gorm.DB, uid int, sourceType, sourceID string, credits int, expiresAt *time.Time, remark string) error {
	if credits <= 0 {
		return nil
	}

	pack := model.CreditsPack{
		UID:          uid,
		SourceType:   sourceType,
		SourceID:     sourceID,
		CreditsTotal: credits,
		CreditsUsed:  0,
		ExpiresAt:    expiresAt,
		Remark:       remark,
	}

	updates := map[string]interface{}{
		"credits_total": gorm.Expr("credits_total + ?", credits),
		"updated_at":    time.Now(),
	}
	if expiresAt != nil {
		updates["expires_at"] = expiresAt
	}
	if remark != "" {
		updates["remark"] = gorm.Expr("CASE WHEN remark = '' THEN ? ELSE CONCAT(remark, ';', ?) END", remark, remark)
	}

	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "uid"},
			{Name: "source_type"},
			{Name: "source_id"},
		},
		DoUpdates: clause.Assignments(updates),
	}).Create(&pack).Error
}

func subscriptionCyclePackSourceID(planKey, grantKey string) (string, error) {
	planKey = strings.TrimSpace(planKey)
	grantKey = strings.TrimSpace(grantKey)
	if planKey == "" || grantKey == "" {
		return "", fmt.Errorf("subscription cycle plan and grant key are required")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s", len(planKey), planKey, len(grantKey), grantKey)))
	// CreditsPack.source_id is varchar(64). A 240-bit suffix leaves room for a
	// readable namespace without relying on truncation of external invoice IDs.
	return "sub:" + hex.EncodeToString(digest[:])[:60], nil
}

// createSubscriptionCyclePackTx creates one immutable allowance Pack for one
// durable billing-order grant. The caller must lock the matching Order owner
// row first; that serializes duplicate delivery even on legacy schemas that do
// not yet have a (uid, source_type, source_id) unique key.
func (s *CreditsPackService) createSubscriptionCyclePackTx(
	tx *gorm.DB,
	uid int,
	planKey string,
	grantKey string,
	credits int,
	expiresAt *time.Time,
	remark string,
) error {
	if tx == nil {
		return fmt.Errorf("create subscription cycle Pack: nil transaction")
	}
	if uid <= 0 {
		return fmt.Errorf("create subscription cycle Pack: invalid uid")
	}
	if credits <= 0 {
		return nil
	}
	sourceID, err := subscriptionCyclePackSourceID(planKey, grantKey)
	if err != nil {
		return err
	}

	var existing model.CreditsPack
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "credits_total", "credits_used").
		Where("uid = ? AND source_type = ? AND source_id = ?", uid, model.CreditsSourceSubscription, sourceID).
		First(&existing).Error
	if err == nil {
		if existing.CreditsTotal != credits || existing.CreditsUsed < 0 || existing.CreditsUsed > existing.CreditsTotal {
			return fmt.Errorf("subscription grant Pack %d conflicts with frozen allowance", existing.Id)
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.CreatePackTx(tx, uid, model.CreditsSourceSubscription, sourceID, credits, expiresAt, remark)
}

// upsertLegacySubscriptionPackTx exists only for migration-focused tests.
// Production
// billing grants use createSubscriptionCyclePackTx so each order owns an
// immutable Pack. If this compatibility path touches the historical singleton,
// it preserves credits_used and grants fresh availability without erasing
// active Reservation allocations.
func (s *CreditsPackService) upsertLegacySubscriptionPackTx(tx *gorm.DB, uid int, sourceID string, credits int, expiresAt *time.Time, remark string) error {
	normalizedSourceID := "subscription"
	if sourceID != "" && sourceID != normalizedSourceID {
		if remark == "" {
			remark = fmt.Sprintf("subscription_id=%s", sourceID)
		} else {
			remark = fmt.Sprintf("%s;subscription_id=%s", remark, sourceID)
		}
	}

	var pack model.CreditsPack
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("uid = ? AND source_type = ? AND source_id = ?", uid, model.CreditsSourceSubscription, normalizedSourceID).
		First(&pack).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return s.CreatePackTx(tx, uid, model.CreditsSourceSubscription, normalizedSourceID, credits, expiresAt, remark)
		}
		return err
	}

	updates := map[string]interface{}{
		"source_id": normalizedSourceID,
		// Preserve the immutable debit ledger. credits_used can include both
		// finalized spend and active Reservation allocations; resetting it to
		// zero makes later checked refunds impossible and grants free capacity.
		// Raising total by the new allowance yields exactly `credits` available
		// after renewal while keeping every old allocation refundable.
		"credits_total": gorm.Expr("credits_used + ?", credits),
		"expires_at":    expiresAt,
		"remark":        remark,
		"updated_at":    time.Now(),
	}

	return tx.Model(&model.CreditsPack{}).Where("id = ?", pack.Id).Updates(updates).Error
}

// balanceSumTx 是实际的 SUM 操作，过滤 legacy + 过期 + 非活跃订阅的 pack。
func (s *CreditsPackService) balanceSumTx(tx *gorm.DB, uid int, now time.Time) (total, used int, err error) {
	return s.balanceSumWithSubscriptionActiveTx(tx, uid, now, s.isSubscriptionCreditsActiveTx(tx, uid, now))
}

func (s *CreditsPackService) balanceSumWithSubscriptionActiveTx(tx *gorm.DB, uid int, now time.Time, subscriptionActive bool) (total, used int, err error) {
	var sum struct {
		Total int
		Used  int
	}
	query := tx.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type <> ? AND (expires_at IS NULL OR expires_at > ?)", uid, model.CreditsSourceLegacy, now)
	if !subscriptionActive {
		query = query.Where("source_type <> ?", model.CreditsSourceSubscription)
	}
	if err = query.
		Select("COALESCE(SUM(credits_total),0) as total, COALESCE(SUM(credits_used),0) as used").
		Scan(&sum).Error; err != nil {
		return 0, 0, err
	}
	return sum.Total, sum.Used, nil
}

// GetBalanceTx 现场 SUM，返回用户当前 credits 余额（total, used, remaining）。
// PR3a 起作为 credits 的唯一真源，替代 w_user_quota.credits_total / credits_used 缓存。
func (s *CreditsPackService) GetBalanceTx(tx *gorm.DB, uid int) (total, used, remaining int, err error) {
	total, used, err = s.balanceSumTx(tx, uid, time.Now())
	if err != nil {
		return 0, 0, 0, err
	}
	remaining = total - used
	if remaining < 0 {
		remaining = 0
	}
	return total, used, remaining, nil
}

func (s *CreditsPackService) GetBalanceForUserTx(tx *gorm.DB, user model.User) (total, used, remaining int, err error) {
	if tx == nil {
		tx = globals.GraDBs["system"]
	}
	uid := int(user.Id)
	now := time.Now()
	total, used, err = s.balanceSumWithSubscriptionActiveTx(tx, uid, now, isSubscriptionUserActive(user, now))
	if err != nil {
		return 0, 0, 0, err
	}
	remaining = total - used
	if remaining < 0 {
		remaining = 0
	}
	return total, used, remaining, nil
}

// GetBalance 默认连接版本。
func (s *CreditsPackService) GetBalance(uid int) (total, used, remaining int, err error) {
	return s.GetBalanceTx(globals.GraDBs["system"], uid)
}

// BackfillActiveSubscriptionCredits is the write-side safety net for deferred
// subscription plans (annual/yearly/lifetime). Balance reads must stay read-only;
// this job ensures current-cycle subscription packs exist outside the request
// path.
func (s *CreditsPackService) BackfillActiveSubscriptionCredits(parent *gorm.DB, batchSize int) (ensured, skipped, failed int, err error) {
	ensured, skipped, failed, _, _, err = s.BackfillActiveSubscriptionCreditsAfter(parent, 0, batchSize)
	return ensured, skipped, failed, err
}

// BackfillActiveSubscriptionCreditsAfter processes one ordered page of active
// subscription users. The caller can persist lastUserID to spread work across
// ticks instead of repeatedly scanning the same first page.
func (s *CreditsPackService) BackfillActiveSubscriptionCreditsAfter(parent *gorm.DB, afterUserID uint, batchSize int) (ensured, skipped, failed int, lastUserID uint, hasMore bool, err error) {
	if parent == nil {
		parent = globals.GraDBs["system"]
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	now := time.Now()

	var users []model.User
	query := parent.
		Select("id, member, member_start_time, member_end_time, member_subscription").
		Where("member > ? AND id > ? AND (member_end_time IS NULL OR member_end_time > ?)", model.MEMBER_SUBSCRIPTION_FREE, afterUserID, now).
		Order("id ASC").
		Limit(batchSize)
	if err = query.Find(&users).Error; err != nil {
		return 0, 0, 0, afterUserID, false, err
	}
	hasMore = len(users) == batchSize

	for _, user := range users {
		user := user
		lastUserID = user.Id
		if e := parent.Transaction(func(tx *gorm.DB) error {
			lockedUser, e := lockCreditsOwnerUserTx(tx, int(user.Id))
			if e != nil {
				return e
			}
			lockedUser, planKey, credits, e := s.resolveActivePlanForUserTx(tx, lockedUser, int(user.Id), now)
			if e != nil {
				return e
			}
			if planKey == "" || !s.isDeferredMonthlyCreditsPlan(planKey) {
				skipped++
				return nil
			}
			if credits <= 0 {
				return fmt.Errorf("positive deferred subscription credits are not frozen for plan %s", planKey)
			}
			if e := lockExistingCreditsPacksTx(tx, int(user.Id)); e != nil {
				return e
			}
			if e := s.ensureCurrentSubscriptionCreditsForUserTx(tx, lockedUser, int(user.Id), planKey, credits, now); e != nil {
				return e
			}
			ensured++
			return nil
		}); e != nil {
			failed++
			globals.Warn(fmt.Sprintf("[CreditsPack] subscription backfill failed for user %d: %v", user.Id, e))
		}
	}
	if len(users) == 0 {
		lastUserID = afterUserID
	}

	return ensured, skipped, failed, lastUserID, hasMore, nil
}

// GetReservedPendingTx returns credits that are debited from packs but not
// finalized yet. The legacy pack `credits_used` field includes this amount, so
// callers that display "spent" should subtract it.
func (s *CreditsPackService) GetReservedPendingTx(tx *gorm.DB, uid int) (int, error) {
	if tx == nil {
		tx = globals.GraDBs["system"]
	}
	var pending int64
	if err := tx.Model(&model.CreditReservation{}).
		// TTL controls execution authorization, not the economic ledger. A
		// reserved row remains debited until the sweeper atomically refunds it
		// and writes a terminal state, even if expires_at is already in the past.
		Where("uid = ? AND status IN ?", uid, []string{
			model.CreditReservationStatusReserved,
			model.CreditReservationStatusReviewHold,
			model.CreditReservationStatusRefundPending,
		}).
		Select("COALESCE(SUM(reserved), 0)").
		Scan(&pending).Error; err != nil {
		return 0, err
	}
	if pending < 0 {
		return 0, nil
	}
	return int(pending), nil
}

func (s *CreditsPackService) GetReservedPending(uid int) (int, error) {
	return s.GetReservedPendingTx(globals.GraDBs["system"], uid)
}

func (s *CreditsPackService) ReserveCreditsDetailedTx(tx *gorm.DB, uid int, cost int) ([]creditsPackAllocation, error) {
	return s.ReserveCreditsDetailedAtTx(tx, uid, cost, time.Now())
}

// ReserveCreditsDetailedAtTx lets the Reservation owner supply its
// database-authoritative admission time. The compatibility entry point above
// retains its historical application-time behaviour for non-Reservation
// callers.
func (s *CreditsPackService) ReserveCreditsDetailedAtTx(tx *gorm.DB, uid int, cost int, now time.Time) ([]creditsPackAllocation, error) {
	if cost <= 0 {
		return nil, nil
	}

	// Subscription ensure first locks the User control row, then every existing
	// Pack by primary key before it may update/create a cycle Pack. Reserve,
	// billing grant and scheduler backfill therefore share User -> Pack(id ASC)
	// and cannot double-create a deferred cycle on schemas without source UNIQUE.
	if err := s.ensureCurrentSubscriptionCreditsTx(tx, uid, now); err != nil {
		return nil, err
	}

	var packs []model.CreditsPack
	query := tx.
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("uid = ? AND source_type <> ? AND (expires_at IS NULL OR expires_at > ?) AND credits_total > credits_used", uid, model.CreditsSourceLegacy, now)
	if !s.isSubscriptionCreditsActiveTx(tx, uid, now) {
		query = query.Where("source_type <> ?", model.CreditsSourceSubscription)
	}
	if err := query.
		// Database locks always follow one canonical order. Allocation policy is
		// deliberately applied only after every eligible Pack is locked; mixing
		// expiry priority with lock acquisition lets a refund take the same rows
		// in the opposite order and deadlock.
		Order("id ASC").
		Find(&packs).Error; err != nil {
		return nil, err
	}

	allocationPacks := append([]model.CreditsPack(nil), packs...)
	sort.Slice(allocationPacks, func(left, right int) bool {
		leftExpiry := allocationPacks[left].ExpiresAt
		rightExpiry := allocationPacks[right].ExpiresAt
		switch {
		case leftExpiry == nil && rightExpiry != nil:
			return false
		case leftExpiry != nil && rightExpiry == nil:
			return true
		case leftExpiry != nil && rightExpiry != nil && !leftExpiry.Equal(*rightExpiry):
			return leftExpiry.Before(*rightExpiry)
		default:
			return allocationPacks[left].Id < allocationPacks[right].Id
		}
	})

	remaining := cost
	allocations := make([]creditsPackAllocation, 0, len(allocationPacks))
	for _, pack := range allocationPacks {
		available := pack.CreditsTotal - pack.CreditsUsed
		if available <= 0 {
			continue
		}
		use := available
		if remaining < use {
			use = remaining
		}
		res := tx.Model(&model.CreditsPack{}).
			Where("id = ? AND credits_used + ? <= credits_total", pack.Id, use).
			UpdateColumn("credits_used", gorm.Expr("credits_used + ?", use))
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, ErrInsufficientCredits
		}
		allocations = append(allocations, creditsPackAllocation{
			PackID:   pack.Id,
			Credits:  use,
			Priority: len(allocations),
		})
		remaining -= use
		if remaining == 0 {
			break
		}
	}

	if remaining > 0 {
		return nil, ErrInsufficientCredits
	}
	return allocations, nil
}

// RefundAllocationsTx is the compatibility entry point for callers that do
// not yet carry the Reservation's immutable Reserved value. It still enforces
// positive, unique allocations, proves that their total covers the requested
// refund, locks Packs by primary key and validates every Pack before the first
// mutation. New settlement code should use RefundAllocationsCheckedTx so a
// corrupt allocation total cannot silently redefine the Reservation.
func (s *CreditsPackService) RefundAllocationsTx(tx *gorm.DB, reservationID uint, credits int) error {
	return s.refundAllocationsTx(tx, reservationID, 0, 0, credits, false)
}

// RefundAllocationsCheckedTx refunds at most refundCredits from an immutable
// allocation set whose sum must equal expectedReserved. Allocation LIFO
// semantics are preserved when deciding how much each Pack receives, while
// the actual row locks and updates always follow Pack primary-key order.
func (s *CreditsPackService) RefundAllocationsCheckedTx(
	tx *gorm.DB,
	reservationID uint,
	expectedUID int,
	expectedReserved int,
	refundCredits int,
) error {
	return s.refundAllocationsTx(tx, reservationID, expectedUID, expectedReserved, refundCredits, true)
}

func (s *CreditsPackService) refundAllocationsTx(
	tx *gorm.DB,
	reservationID uint,
	expectedUID int,
	expectedReserved int,
	refundCredits int,
	checkExpectedReserved bool,
) error {
	if refundCredits == 0 {
		return nil
	}
	if tx == nil {
		return fmt.Errorf("refund allocations: nil transaction")
	}
	if reservationID == 0 {
		return refundIntegrityError(
			ErrCreditsRefundAllocationInvalid, reservationID, 0, "reservation id must be positive",
		)
	}
	if refundCredits < 0 {
		return refundIntegrityError(
			ErrCreditsRefundAllocationInvalid, reservationID, 0, "refund credits must be positive",
		)
	}
	if checkExpectedReserved {
		if expectedUID <= 0 {
			return refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, 0, "expected uid must be positive",
			)
		}
		if expectedReserved <= 0 {
			return refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, 0, "expected reserved credits must be positive",
			)
		}
		if refundCredits > expectedReserved {
			return refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, 0,
				"refund credits %d exceed expected reserved credits %d", refundCredits, expectedReserved,
			)
		}
	}

	// Pin the parent before reading its immutable children. The Reservation
	// service already owns this lock in normal settlement; the re-lock is
	// harmless and makes the compatibility entry point obey the same order.
	var parent model.CreditReservation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "uid", "reserved").
		Where("id = ?", reservationID).
		First(&parent).Error; err != nil {
		return err
	}
	if checkExpectedReserved && (parent.UID != expectedUID || parent.Reserved != expectedReserved) {
		return refundIntegrityError(
			ErrCreditsRefundAllocationInvalid, reservationID, 0,
			"parent uid/reserved %d/%d differs from expected %d/%d",
			parent.UID, parent.Reserved, expectedUID, expectedReserved,
		)
	}

	// First take a non-locking immutable snapshot only to derive Pack IDs. A
	// locking range scan here would acquire an Allocation next-key/gap lock
	// before Packs and can deadlock a concurrent Reserve that already owns a
	// Pack lock and is inserting its Allocation tail under MySQL RR.
	var allocationSnapshot []model.CreditReservationAllocation
	if err := tx.Where("reservation_id = ?", reservationID).
		Order("id ASC").
		Find(&allocationSnapshot).Error; err != nil {
		return err
	}
	if len(allocationSnapshot) == 0 {
		return refundIntegrityError(
			ErrCreditsRefundAllocationIncomplete, reservationID, 0, "allocation set is empty",
		)
	}

	snapshotPlan, err := buildRefundAllocationPlan(
		reservationID, allocationSnapshot, expectedReserved, refundCredits, checkExpectedReserved,
	)
	if err != nil {
		return err
	}
	var packs []model.CreditsPack
	if err := tx.
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", snapshotPlan.packIDs).
		Order("id ASC").
		Find(&packs).Error; err != nil {
		return err
	}

	// Lock Allocations only after Packs, then prove the rows are byte-for-byte
	// identical on every financial/identity field used by settlement. Legal
	// writers are already blocked by the parent Reservation lock; this recheck
	// also fails closed if legacy data bypassed that contract.
	var allocations []model.CreditReservationAllocation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("reservation_id = ?", reservationID).
		Order("id ASC").
		Find(&allocations).Error; err != nil {
		return err
	}
	if !sameRefundAllocationRows(allocationSnapshot, allocations) {
		return refundIntegrityError(
			ErrCreditsRefundAllocationInvalid, reservationID, 0,
			"allocation set changed between Pack discovery and lock",
		)
	}
	plan, err := buildRefundAllocationPlan(
		reservationID, allocations, expectedReserved, refundCredits, checkExpectedReserved,
	)
	if err != nil {
		return err
	}
	if !samePackIDs(snapshotPlan.packIDs, plan.packIDs) {
		return refundIntegrityError(
			ErrCreditsRefundAllocationInvalid, reservationID, 0,
			"allocation Pack set changed between discovery and lock",
		)
	}
	if len(packs) != len(plan.packIDs) {
		return refundIntegrityError(
			ErrCreditsRefundPackInvariant, reservationID, 0,
			"locked %d packs for %d allocations", len(packs), len(plan.packIDs),
		)
	}
	for index, pack := range packs {
		if pack.Id != plan.packIDs[index] {
			return refundIntegrityError(
				ErrCreditsRefundPackInvariant, reservationID, plan.packIDs[index],
				"locked pack set is not canonical",
			)
		}
		allocationCredits := plan.allocationByPack[pack.Id]
		if checkExpectedReserved && pack.UID != expectedUID {
			return refundIntegrityError(
				ErrCreditsRefundPackInvariant, reservationID, pack.Id,
				"pack uid %d differs from reservation uid %d", pack.UID, expectedUID,
			)
		}
		if pack.CreditsUsed < 0 || pack.CreditsTotal < pack.CreditsUsed {
			return refundIntegrityError(
				ErrCreditsRefundPackInvariant, reservationID, pack.Id,
				"pack balance is outside [0,total]: used=%d total=%d", pack.CreditsUsed, pack.CreditsTotal,
			)
		}
		if pack.CreditsUsed < allocationCredits {
			return refundIntegrityError(
				ErrCreditsRefundPackInvariant, reservationID, pack.Id,
				"pack used credits %d do not contain allocation %d", pack.CreditsUsed, allocationCredits,
			)
		}
	}

	// Every integrity check is complete before the first Pack mutation. A
	// database error during these guarded updates must still be returned so the
	// caller rolls the enclosing transaction back.
	for _, pack := range packs {
		refund := plan.refundByPack[pack.Id]
		if refund == 0 {
			continue
		}
		res := tx.Model(&model.CreditsPack{}).
			Where("id = ? AND credits_used >= ?", pack.Id, refund).
			UpdateColumn("credits_used", gorm.Expr("credits_used - ?", refund))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return refundIntegrityError(
				ErrCreditsRefundPackInvariant, reservationID, pack.Id,
				"guarded refund of %d credits affected %d rows", refund, res.RowsAffected,
			)
		}
	}
	return nil
}

func sameRefundAllocationRows(left, right []model.CreditReservationAllocation) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Id != right[index].Id ||
			left[index].ReservationID != right[index].ReservationID ||
			left[index].PackID != right[index].PackID ||
			left[index].Credits != right[index].Credits {
			return false
		}
	}
	return true
}

func samePackIDs(left, right []uint) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type refundAllocationPlan struct {
	allocationByPack map[uint]int
	refundByPack     map[uint]int
	packIDs          []uint
}

func buildRefundAllocationPlan(
	reservationID uint,
	allocations []model.CreditReservationAllocation,
	expectedReserved int,
	refundCredits int,
	checkExpectedReserved bool,
) (refundAllocationPlan, error) {
	plan := refundAllocationPlan{
		allocationByPack: make(map[uint]int, len(allocations)),
		refundByPack:     make(map[uint]int, len(allocations)),
		packIDs:          make([]uint, 0, len(allocations)),
	}
	allocationTotal := 0
	for _, allocation := range allocations {
		if allocation.ReservationID != reservationID {
			return refundAllocationPlan{}, refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, allocation.PackID,
				"allocation names reservation %d", allocation.ReservationID,
			)
		}
		if allocation.PackID == 0 {
			return refundAllocationPlan{}, refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, 0, "allocation pack id must be positive",
			)
		}
		if allocation.Credits <= 0 {
			return refundAllocationPlan{}, refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, allocation.PackID,
				"allocation credits must be positive, got %d", allocation.Credits,
			)
		}
		if _, duplicate := plan.allocationByPack[allocation.PackID]; duplicate {
			return refundAllocationPlan{}, refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, allocation.PackID,
				"allocation pack is duplicated",
			)
		}
		if allocationTotal > math.MaxInt-allocation.Credits {
			return refundAllocationPlan{}, refundIntegrityError(
				ErrCreditsRefundAllocationInvalid, reservationID, allocation.PackID,
				"allocation total overflows int",
			)
		}
		plan.allocationByPack[allocation.PackID] = allocation.Credits
		plan.packIDs = append(plan.packIDs, allocation.PackID)
		allocationTotal += allocation.Credits
	}
	if checkExpectedReserved && allocationTotal != expectedReserved {
		return refundAllocationPlan{}, refundIntegrityError(
			ErrCreditsRefundAllocationIncomplete, reservationID, 0,
			"allocation total %d differs from expected reserved credits %d", allocationTotal, expectedReserved,
		)
	}
	if refundCredits > allocationTotal {
		return refundAllocationPlan{}, refundIntegrityError(
			ErrCreditsRefundAllocationIncomplete, reservationID, 0,
			"refund credits %d exceed allocation total %d", refundCredits, allocationTotal,
		)
	}

	// Preserve the historic allocation LIFO policy while decoupling it from
	// the Pack row order used below for database concurrency.
	remaining := refundCredits
	for index := len(allocations) - 1; index >= 0 && remaining > 0; index-- {
		allocation := allocations[index]
		refund := allocation.Credits
		if refund > remaining {
			refund = remaining
		}
		plan.refundByPack[allocation.PackID] = refund
		remaining -= refund
	}
	if remaining != 0 {
		return refundAllocationPlan{}, refundIntegrityError(
			ErrCreditsRefundAllocationIncomplete, reservationID, 0,
			"allocation set leaves %d credits without a refund source", remaining,
		)
	}
	sort.Slice(plan.packIDs, func(left, right int) bool { return plan.packIDs[left] < plan.packIDs[right] })
	return plan, nil
}

func (s *CreditsPackService) RevokeOrderCreditsTx(tx *gorm.DB, orderNo string) error {
	if strings.TrimSpace(orderNo) == "" {
		return nil
	}

	now := time.Now()
	return tx.Model(&model.CreditsPack{}).
		Where("source_id = ? AND source_type IN ?", orderNo, []string{model.CreditsSourcePurchase, model.CreditsSourceBonus}).
		Updates(map[string]interface{}{
			"credits_total": gorm.Expr("credits_used"),
			"expires_at":    &now,
			"remark":        gorm.Expr("CASE WHEN remark = '' THEN ? ELSE CONCAT(remark, ';', ?) END", "order refunded", "order refunded"),
			"updated_at":    now,
		}).Error
}

func (s *CreditsPackService) ClipSubscriptionCreditsTx(tx *gorm.DB, uid int, cutoff time.Time) error {
	return tx.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type = ? AND (expires_at IS NULL OR expires_at > ?)", uid, model.CreditsSourceSubscription, cutoff).
		Updates(map[string]interface{}{
			"expires_at": cutoff,
			"updated_at": time.Now(),
		}).Error
}
