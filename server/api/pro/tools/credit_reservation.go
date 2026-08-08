package tools

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/service/account"
	toolsService "server/service/tools"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Solution-agnostic credit-reservation helpers used by every
// AI-assisted tool handler in this package (short-drama, video-ad,
// comic, ecommerce). The reservation lifecycle is:
//
//   reserve → run the tool → finalize (success) OR release (failure)
//
// Idempotency comes from the (X-Idempotency-Key) header, falling
// back to a per-user nanosecond auto-key when absent. The header
// pattern lets clients safely retry without double-charging.
//
// History: these were originally named reserveShortDramaCredits etc
// and lived in short_drama_outline_api.go. They were always
// solution-agnostic — the rename + extraction here closes that
// long-standing naming bug.

// reserveSolutionCredits books a reservation for `creditCost` units
// against `uid` for the given `tool`. On insufficient credits it
// writes a 200-with-error response and returns ok=false. On any
// other DB / service error it writes a 500 response. Callers should
// `return` immediately when ok is false.
//
// `tool` shows up in the reservation's idempotency key namespace and
// in the audit remark — pass a stable identifier per tool (e.g.
// "ad_scene_render", "drama_outline_generate").
func reserveSolutionCredits(c *gin.Context, uid int, tool string, creditCost int) (uint, bool) {
	db := globals.GraDBs["system"]
	svc := account.NewCreditReservationService()

	clientKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if clientKey == "" {
		clientKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}
	if clientKey == "" {
		clientKey = fmt.Sprintf("auto_%d_%d", uid, time.Now().UnixNano())
	}
	key := fmt.Sprintf("%s:%s", tool, clientKey)

	var reservationID uint
	err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, account.ReservationRequest{
			UID:            uid,
			Tool:           tool,
			IdempotencyKey: key,
			Reserved:       creditCost,
			Remark:         tool,
		})
		if err != nil {
			return err
		}
		if !res.Created {
			if res.Reservation.IsTerminal() {
				return account.ErrReservationAlreadyProcessed
			}
			return account.ErrReservationInProgress
		}
		reservationID = res.Reservation.Id
		return nil
	})
	if err != nil {
		if errors.Is(err, account.ErrInsufficientCredits) {
			response.FailWithDetailed(gin.H{"creditsRequired": creditCost}, "Insufficient credits", c)
			return 0, false
		}
		if errors.Is(err, account.ErrReservationInProgress) {
			c.JSON(http.StatusConflict, gin.H{
				"code": response.ERROR, "message": "Request already in progress",
				"data": gin.H{"errorCode": "RESERVATION_IN_PROGRESS", "retryable": true},
			})
			return 0, false
		}
		if errors.Is(err, account.ErrReservationAlreadyProcessed) || errors.Is(err, account.ErrReservationReplayConflict) {
			c.JSON(http.StatusConflict, gin.H{
				"code": response.ERROR, "message": "Idempotency key already used",
				"data": gin.H{"errorCode": "RESERVATION_REPLAY_CONFLICT", "retryable": false},
			})
			return 0, false
		}
		globals.Error(fmt.Sprintf("[Credit] Reserve failed user=%d tool=%s cost=%d: %v", uid, tool, creditCost, err))
		response.FailWithMessage("Server error", c)
		return 0, false
	}
	return reservationID, true
}

// releaseSolutionReservation unbooks a reservation that the caller
// has decided not to charge for (typically: the tool errored out
// before producing a result). Idempotent: already-finalized rows
// log nothing.
func releaseSolutionReservation(reservationID uint) {
	if reservationID == 0 {
		return
	}
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return account.NewCreditReservationService().Release(tx, reservationID)
	})
	if err != nil {
		globals.Error(fmt.Sprintf("[Credit] release reservation %d: %v", reservationID, err))
	}
}

// finalizeSolutionReservation commits the reservation, charges the
// user `used` credits (≤ the reserved amount), and writes a success
// row to w_usage_record so protected operator metrics see the activity.
// Idempotent on already-finalized rows: skips the usage_record write
// since the original finalize already wrote it.
//
// The usage_record write happens after the finalize tx commits — a
// usage_record failure must not roll back a successful credit charge,
// and a missing analytics row is far less harmful than double-billing.
func finalizeSolutionReservation(c *gin.Context, reservationID uint, used int) {
	if reservationID == 0 {
		return
	}
	db := globals.GraDBs["system"]
	var reservation model.CreditReservation
	shouldWriteUsage := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var before model.CreditReservation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "used").Where("id = ?", reservationID).First(&before).Error; err != nil {
			return err
		}
		if err := account.NewCreditReservationService().Finalize(tx, reservationID, used); err != nil {
			return err
		}
		if err := tx.Select("uid", "tool", "status", "used").Where("id = ?", reservationID).First(&reservation).Error; err != nil {
			return err
		}
		shouldWriteUsage = before.Status != model.CreditReservationStatusFinalized &&
			reservation.Status == model.CreditReservationStatusFinalized && reservation.Used == used
		return nil
	})
	if err != nil {
		globals.Error(fmt.Sprintf("[Credit] finalize reservation %d: %v", reservationID, err))
		return
	}
	if !shouldWriteUsage {
		return
	}

	meta := &toolsService.UsageRecordMeta{}
	if c != nil && c.Request != nil {
		meta.IP = utils.GetClientIP(c.Request)
		meta.DeviceInfo = c.GetHeader("User-Agent")
	}
	if err := toolsService.CreateUsageRecordTx(db, reservation.UID, reservation.Tool, 0, used, model.STATUS_SUCCESS, 0, meta); err != nil {
		globals.Error(fmt.Sprintf("[Credit] write usage_record after finalize %d: %v", reservationID, err))
	}
}
