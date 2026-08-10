package oauth

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/middleware"
	"server/model"
	accountsvc "server/service/account"
)

// userinfoResponse is the body of GET /api/desktop/oauth/userinfo.
// Stable wire format per backend-oauth-prerequisites.md §2.4.
//
// The `quota` substructure stays present (zero-valued when the credits
// lookup fails) — keeps the JSON shape stable so renderer code doesn't
// need conditional access.
//
// Backward compatibility rule for this struct: existing keys keep their
// meaning and their presence. member_status and credits were ADDED; nothing
// was renamed or removed. (`tier` did change VALUE for free-plan users, but
// that is the bug being fixed — see tierFromMember's history below.)
type userinfoResponse struct {
	UserID        string    `json:"user_id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name"`
	AvatarURL     string    `json:"avatar_url,omitempty"`
	Tier          string    `json:"tier"`
	TierExpiresAt string    `json:"tier_expires_at,omitempty"`
	Quota         userinfoQ `json:"quota"`
	// MemberStatus mirrors /api/account/quota's memberStatus vocabulary
	// (active / expired / free) so Desktop and Portal describe the same
	// account in the same words.
	MemberStatus string `json:"member_status"`
	// Credits is the real spendable balance from w_credits_pack, the same
	// source /api/account/quota reads. Added rather than folded into `quota`
	// because credits are not month-scoped: packs carry their own expiry.
	Credits userinfoCredits `json:"credits"`
}

type userinfoQ struct {
	MonthUsed  int `json:"month_used"`
	MonthLimit int `json:"month_limit"`
}

type userinfoCredits struct {
	Total int `json:"total"`
	Used  int `json:"used"`
	// Remaining is Total-Used floored at zero — what the user can still spend.
	Remaining int `json:"remaining"`
	// ReservedPending is the slice of Used that belongs to in-flight turns and
	// may still be refunded. Surfacing it stops the Desktop from reporting a
	// permanent spend for a turn that is merely running.
	ReservedPending int `json:"reserved_pending"`
}

// UserInfo handles GET /api/desktop/oauth/userinfo. Caller is
// authenticated via OAuthBearerAuth middleware. Returns a denormalized
// snapshot of the user's account state for the desktop client to cache.
//
// Membership and credits are now real, read from the same two sources the
// Portal reads: w_user (tier + expiry) and w_credits_pack (balance). The
// credits read is fail-soft — a transient DB error degrades the response to
// zeroed credits rather than failing the whole account snapshot, because a
// Desktop client that can't fetch userinfo treats itself as signed out.
func (a *OauthApi) UserInfo(c *gin.Context) {
	claims, ok := middleware.OAuthClaims(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	uid := claims.BaseClaims.Id
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	user, err := a.loadUser(c, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	now := time.Now()
	activePaid := model.IsActivePaidMember(user.Member, user.MemberEndTime, now)

	resp := userinfoResponse{
		UserID:       formatUserID(user.Id), // GraMODEL exposes Id (capital I + lower d), not ID
		Email:        user.Email,
		DisplayName:  user.Nickname,
		AvatarURL:    user.Avatar,
		Tier:         model.EffectiveMemberTier(user.Member, user.MemberEndTime, now),
		MemberStatus: memberStatusFromUser(user, now),
	}
	// Only a live paid window is a tier expiry. Previously any non-zero
	// member_end_time on any member > 0 was emitted — which handed a
	// free-plan user (member=1) a "your pro tier ends on…" date, and handed a
	// lapsed subscriber a date already in the past.
	if activePaid && !user.MemberEndTime.IsZero() {
		resp.TierExpiresAt = user.MemberEndTime.UTC().Format("2006-01-02T15:04:05Z")
	}

	total, used, remaining, reserved := a.loadCredits(c, user)
	resp.Credits = userinfoCredits{
		Total:           total,
		Used:            used,
		Remaining:       remaining,
		ReservedPending: reserved,
	}
	// month_used / month_limit predate credits and are what the renderer's
	// existing progress display reads. Map them onto the credits allowance so
	// the legacy keys stop being permanently zero: limit = everything granted
	// and still valid, used = everything already debited (in-flight
	// reservations included, so limit-used is what is actually spendable).
	resp.Quota = userinfoQ{MonthUsed: used, MonthLimit: total}

	c.JSON(http.StatusOK, resp)
}

// memberStatusFromUser mirrors /api/account/quota. Free covers both
// member=0 (never enrolled) and member=1 (free plan claimed) — neither is a
// paid entitlement.
func memberStatusFromUser(user *model.User, now time.Time) string {
	if user.Member <= model.MEMBER_SUBSCRIPTION_FREE {
		return "free"
	}
	if model.IsActivePaidMember(user.Member, user.MemberEndTime, now) {
		return "active"
	}
	return "expired"
}

// loadCredits reads the live pack balance. Reuses CreditsPackService — the
// same code path /api/account/quota uses — so Desktop and Portal can never
// disagree about a balance.
//
// Fail-soft by design: userinfo is the Desktop's session-health probe, and a
// credits hiccup must not read as "signed out".
func (a *OauthApi) loadCredits(c *gin.Context, user *model.User) (total, used, remaining, reservedPending int) {
	if a.DB == nil {
		return 0, 0, 0, 0
	}
	db := a.DB.WithContext(c.Request.Context())
	service := accountsvc.NewCreditsPackService()

	total, used, remaining, err := service.GetBalanceForUserTx(db, *user)
	if err != nil {
		return 0, 0, 0, 0
	}
	pending, err := service.GetReservedPendingTx(db, int(user.Id))
	if err != nil {
		// Balance is still trustworthy; only the in-flight split is unknown.
		return total, used, remaining, 0
	}
	return total, used, remaining, pending
}

// loadUser fetches the user row. Lifted onto OauthApi so tests can
// run against the test DB without bootstrapping the global
// AccountService singleton.
func (a *OauthApi) loadUser(c *gin.Context, uid uint) (*model.User, error) {
	db := a.DB
	if db == nil {
		// Defensive fallback — shouldn't happen in production where
		// NewOauthApi wires DB.
		return nil, errors.New("OauthApi.DB not configured")
	}
	var user model.User
	err := db.WithContext(c.Request.Context()).Where("id = ?", uid).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// Tier derivation now lives in model.EffectiveMemberTier (model/user.go),
// shared with /api/desktop/models and the Portal.
//
// The local tierFromMember it replaced mapped `member > 0` to "pro". Since
// member=1 is the free-plan write value, every user who claimed the free plan
// or was downgraded by a refund was reported to the Desktop as a Pro member —
// while the server-side gate (IsPremiumMember) correctly refused them the Pro
// model. The client showed an entitlement the backend would not honor.
//
// The renderer uses this string for display, not enforcement; server-side
// billing remains the source of truth.

// formatUserID returns the same `u_<id>` shape the design doc shows.
// Centralized so the JSON wire form stays stable if we ever want to
// shorten it (e.g. base62 encoding for compactness).
func formatUserID(id uint) string {
	if id == 0 {
		return ""
	}
	// Plain `u_N` keeps the response readable in curl + matches
	// backend-oauth-prerequisites.md §2.4's example.
	return "u_" + strconv.FormatUint(uint64(id), 10)
}
