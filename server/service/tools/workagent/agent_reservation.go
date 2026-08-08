package workagent

import (
	"fmt"

	"server/globals"
	accountService "server/service/account"

	"gorm.io/gorm"
)

// ReservationClosures returns the (release, finalize) closure pair
// that drives the credit-reservation lifecycle for one agent turn.
// The closures share an internal `settled` flag so the first call to
// either makes both subsequent calls no-ops — matches the canonical
// pattern both agent handlers use:
//
//	release, finalize := workagentService.ReservationClosures(...)
//	shouldFinalize := true
//	usedCredits := reservedCost  // pessimistic default
//	defer func() {
//	    if shouldFinalize { finalize(usedCredits) } else { release("agent_failed") }
//	}()
//
//	... explicit error paths flip shouldFinalize=false and may also
//	    call release(reason) directly to refund eagerly ...
//
//	... post-SDK success path updates usedCredits with actual cost
//	    computed from total_cost_usd (token-based billing) so the
//	    deferred finalize charges actuals + refunds the delta ...
//
// The settled flag means the explicit release calls and the deferred
// release fight over which one wins, but only the FIRST one finalizes
// the underlying reservation — subsequent calls are no-ops. This is
// the contract pre-2026-05; consolidating the closures here doesn't
// change semantics, only the source-of-truth.
//
// `reservedCost` is the upper bound stamped on the reservation row at
// reserve time — Finalize rejects `used > reservedCost`. The finalize
// closure clamps the caller-supplied actual to [0, reservedCost] so a
// caller bug (passing a wildly large actual) can't blow up the call.
//
// Terminal outcome errors are expected when this function races itself
// (defer + explicit refund); they're swallowed silently. Any other error logs at Error level
// because losing a credit reconciliation row is a billing-correctness
// concern.
func ReservationClosures(
	db *gorm.DB,
	svc *accountService.CreditReservationService,
	reservationID uint,
	reservedCost int,
	uid int,
	logTag string,
) (release func(reason string), finalize func(usedCredits int)) {
	settled := false

	release = func(reason string) {
		if settled {
			return
		}
		settled = true
		if err := db.Transaction(func(tx *gorm.DB) error {
			return svc.Release(tx, reservationID)
		}); err != nil {
			globals.Error(fmt.Sprintf("%s Failed to release reservation for user %d (reason=%s): %v", logTag, uid, reason, err))
		}
	}

	finalize = func(usedCredits int) {
		if settled {
			return
		}
		settled = true
		// Defensive clamp. Finalize rejects `used > reserved` with an
		// error; clamping here means an overestimate from the
		// post-turn cost extractor doesn't crash the billing path.
		// Under-clamping to 0 protects against accidental negative
		// inputs (NaN bugs in usd-to-credit math, etc.).
		if usedCredits < 0 {
			usedCredits = 0
		}
		if usedCredits > reservedCost {
			usedCredits = reservedCost
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			return svc.Finalize(tx, reservationID, usedCredits)
		}); err != nil {
			globals.Error(fmt.Sprintf("%s Failed to finalize reservation for user %d (used=%d reserved=%d): %v", logTag, uid, usedCredits, reservedCost, err))
		}
	}

	return
}
