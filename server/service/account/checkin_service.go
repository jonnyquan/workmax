package account

import (
	"errors"
	"fmt"
	"math/rand"
	"server/globals"
	"server/model"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type CheckinService struct{}

// MySQL "Duplicate entry" error code. The DB-level unique index on
// w_user_checkin (uid, checkin_date) is the primary defense against
// double-checkin; we detect this specific error to translate it into
// the friendly AlreadyDone response rather than bubbling a 500 up.
const mysqlDuplicateKey = 1062

func isDuplicateKeyError(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlDuplicateKey
}

// getLocalMidnight 获取本地时区的当天午夜时间
func getLocalMidnight(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

// checkinRewardRules 读取签到奖励规则；任一字段缺省或非法时退回到
// model 中的安全常量，确保 YAML 漏配不会破坏签到流程。MinPerDay 大于
// MaxPerDay 时强制把 Min 拉到 Max，避免 rand.Intn 拿到负数 panic。
func checkinRewardRules() (minPerDay, maxPerDay, streakDays, streakBonus int) {
	cfg := globals.GraConf.Credits.Checkin

	minPerDay = cfg.MinPerDay
	if minPerDay <= 0 {
		minPerDay = model.CHECKIN_MIN_CREDITS
	}
	maxPerDay = cfg.MaxPerDay
	if maxPerDay <= 0 {
		maxPerDay = model.CHECKIN_MAX_CREDITS
	}
	if minPerDay > maxPerDay {
		minPerDay = maxPerDay
	}
	streakDays = cfg.StreakDays
	if streakDays <= 0 {
		streakDays = model.CHECKIN_STREAK_DAYS
	}
	streakBonus = cfg.StreakBonus
	if streakBonus <= 0 {
		streakBonus = model.CHECKIN_STREAK_BONUS
	}
	return
}

// DoCheckin 执行每日签到。
//
// Atomicity model:
//   - One DB transaction wraps the checkin INSERT + the credit grant.
//   - The unique index on (uid, checkin_date) makes the INSERT itself
//     the race winner — no SELECT-then-INSERT TOCTOU window. If a
//     concurrent request lost the race, its INSERT fails with mysql
//     error 1062 and we translate that to AlreadyDone.
//   - If the credit grant fails (transaction error, deadlock, etc.),
//     the whole transaction rolls back including the checkin row, so
//     the user can retry. Previously a grant failure was logged-and-
//     swallowed, leaving the user "checked in" but unrewarded.
func (s *CheckinService) DoCheckin(uid int) (*model.CheckinResult, error) {
	now := time.Now()
	today := getLocalMidnight(now)

	// Pre-flight: if already checked in today, short-circuit before
	// opening a transaction. This is the common case — saves a round
	// trip + lock acquisition. The race-safe path below catches the
	// concurrent attempts that slip past this check.
	var existingCheckin model.UserCheckin
	if err := globals.GraDBs["system"].
		Where("uid = ? AND checkin_date = ?", uid, today).
		First(&existingCheckin).Error; err == nil {
		return alreadyDoneResult(existingCheckin), nil
	}

	// 计算连续签到天数 (read of yesterday's row; safe outside the txn
	// because it's only used to derive streakDays, not to gate
	// uniqueness — the unique index is what gates uniqueness).
	streakDays := 1
	yesterday := today.AddDate(0, 0, -1)
	var lastCheckin model.UserCheckin
	if err := globals.GraDBs["system"].
		Where("uid = ? AND checkin_date = ?", uid, yesterday).
		First(&lastCheckin).Error; err == nil {
		streakDays = lastCheckin.StreakDays + 1
	}

	minPerDay, maxPerDay, streakStep, streakBonusAmt := checkinRewardRules()
	creditsEarned := minPerDay + rand.Intn(maxPerDay-minPerDay+1)
	bonusEarned := 0
	if streakDays > 0 && streakDays%streakStep == 0 {
		bonusEarned = streakBonusAmt
	}
	totalCredits := creditsEarned + bonusEarned

	checkin := model.UserCheckin{
		UID:           uid,
		CheckinDate:   today,
		CreditsEarned: creditsEarned,
		StreakDays:    streakDays,
		BonusEarned:   bonusEarned,
	}

	// alreadyDone is set by the transaction body when the INSERT hits
	// the unique index — propagated out so the caller returns the
	// friendly AlreadyDone result without surfacing the SQL error.
	var alreadyDone bool
	txErr := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&checkin).Error; err != nil {
			if isDuplicateKeyError(err) {
				// Concurrent checkin landed first. Re-fetch its row so
				// the caller can return its rewards rather than the
				// would-be values from this attempt. Use the outer
				// scope's existingCheckin to carry the result out.
				if err2 := tx.Where("uid = ? AND checkin_date = ?", uid, today).
					First(&existingCheckin).Error; err2 != nil {
					return fmt.Errorf("checkin lost race but row missing: %w", err2)
				}
				alreadyDone = true
				return nil
			}
			return fmt.Errorf("failed to create checkin record: %w", err)
		}
		// Credit grant runs in the SAME transaction — if the pack
		// service returns an error, the checkin row rolls back too,
		// so the user can retry without being marked checked-in.
		if err := s.grantCheckinCreditsTx(tx, uid, totalCredits, checkin.Id); err != nil {
			return fmt.Errorf("failed to grant checkin credits: %w", err)
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	if alreadyDone {
		return alreadyDoneResult(existingCheckin), nil
	}

	globals.Info(fmt.Sprintf("User %d checked in: +%d credits (streak: %d days)", uid, totalCredits, streakDays))
	return &model.CheckinResult{
		Success:       true,
		Message:       "Checkin successful",
		CreditsEarned: creditsEarned,
		BonusEarned:   bonusEarned,
		TotalEarned:   totalCredits,
		StreakDays:    streakDays,
		AlreadyDone:   false,
	}, nil
}

// alreadyDoneResult shapes a CheckinResult from a UserCheckin row that
// the caller already discovered exists for today. Extracted so both
// the pre-flight short-circuit and the race-loser path return an
// identical envelope to the API.
func alreadyDoneResult(row model.UserCheckin) *model.CheckinResult {
	return &model.CheckinResult{
		Success:       false,
		Message:       "Already checked in today",
		AlreadyDone:   true,
		StreakDays:    row.StreakDays,
		CreditsEarned: row.CreditsEarned,
		BonusEarned:   row.BonusEarned,
		TotalEarned:   row.CreditsEarned + row.BonusEarned,
	}
}

// grantCheckinCreditsTx adds the daily Credits + a UserRewards audit
// row inside an existing transaction handle. DoCheckin is the only
// caller; folding it into the outer txn means a grant failure rolls
// back the checkin row too (previously the checkin row could persist
// while the grant silently failed, leaving "checked in but unrewarded"
// users with no retry path).
func (s *CheckinService) grantCheckinCreditsTx(tx *gorm.DB, uid int, credits int, checkinID uint) error {
	packService := NewCreditsPackService()
	remark := fmt.Sprintf("checkin_id=%d", checkinID)
	expiresAt := rewardExpireAt()
	if err := packService.AddToPackTx(tx, uid, model.CreditsSourceCheckin, "checkin", credits, &expiresAt, remark); err != nil {
		return err
	}

	reward := model.UserRewards{
		UID:         uid,
		SourceType:  model.UserRewardsSourceTypeCheckin,
		SourceID:    int(checkinID),
		Description: "Daily check-in",
		RewardItem:  model.UserRewardsItemTypeCredits,
		RewardNum:   credits,
		ExpireDate:  expiresAt,
	}
	if err := tx.Create(&reward).Error; err != nil {
		return err
	}

	return nil
}

// GetCheckinStatus 获取签到状态
func (s *CheckinService) GetCheckinStatus(uid int) (*model.CheckinStatus, error) {
	now := time.Now()
	today := getLocalMidnight(now)

	status := &model.CheckinStatus{
		CheckedInToday: false,
		StreakDays:     0,
	}

	// 检查今天是否已签到
	var todayCheckin model.UserCheckin
	err := globals.GraDBs["system"].
		Where("uid = ? AND checkin_date = ?", uid, today).
		First(&todayCheckin).Error

	if err == nil {
		status.CheckedInToday = true
		status.StreakDays = todayCheckin.StreakDays
		status.LastCheckin = todayCheckin.CheckinDate
	} else {
		// 查找最近一次签到
		var lastCheckin model.UserCheckin
		err = globals.GraDBs["system"].
			Where("uid = ?", uid).
			Order("checkin_date DESC").
			First(&lastCheckin).Error

		if err == nil {
			status.LastCheckin = lastCheckin.CheckinDate
			// 检查是否是昨天签到的（连续签到）
			yesterday := today.AddDate(0, 0, -1)
			if lastCheckin.CheckinDate.Equal(yesterday) {
				status.StreakDays = lastCheckin.StreakDays
			}
		}
	}

	// 统计总签到次数和总获得Credits
	var totalStats struct {
		Count   int `json:"count"`
		Credits int `json:"credits"`
	}
	globals.GraDBs["system"].Model(&model.UserCheckin{}).
		Select("COUNT(*) as count, COALESCE(SUM(credits_earned + bonus_earned), 0) as credits").
		Where("uid = ?", uid).
		Scan(&totalStats)

	status.TotalCheckins = totalStats.Count
	status.TotalCredits = totalStats.Credits

	return status, nil
}

// GetCheckinHistory 获取签到历史
func (s *CheckinService) GetCheckinHistory(uid int, days int) ([]model.UserCheckin, error) {
	var history []model.UserCheckin
	now := time.Now()
	startDate := getLocalMidnight(now.AddDate(0, 0, -days))

	err := globals.GraDBs["system"].
		Where("uid = ? AND checkin_date >= ?", uid, startDate).
		Order("checkin_date DESC").
		Find(&history).Error

	return history, err
}
