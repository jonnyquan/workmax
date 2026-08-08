package oauth

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/middleware"
	"server/model"
)

// userinfoResponse is the body of GET /api/desktop/oauth/userinfo.
// Stable wire format per backend-oauth-prerequisites.md §2.4.
//
// The `quota` substructure stays present (zero-valued) even when we
// can't compute it yet — keeps the JSON shape stable so renderer
// code doesn't need conditional access.
type userinfoResponse struct {
	UserID        string    `json:"user_id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name"`
	AvatarURL     string    `json:"avatar_url,omitempty"`
	Tier          string    `json:"tier"`
	TierExpiresAt string    `json:"tier_expires_at,omitempty"`
	Quota         userinfoQ `json:"quota"`
}

type userinfoQ struct {
	MonthUsed  int `json:"month_used"`
	MonthLimit int `json:"month_limit"`
}

// UserInfo handles GET /api/desktop/oauth/userinfo. Caller is
// authenticated via OAuthBearerAuth middleware. Returns a denormalized
// snapshot of the user's account state for the desktop client to cache.
//
// P-1.6 stub: returns email + nickname + tier (derived from
// User.Member); quota fields are zero pending integration with the
// existing tools/credit_cost machinery in P-1.7 or later. Renderer
// can display "—" for zero values; the field stays present so the
// wire shape is stable.
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

	resp := userinfoResponse{
		UserID:      formatUserID(user.Id), // GraMODEL exposes Id (capital I + lower d), not ID
		Email:       user.Email,
		DisplayName: user.Nickname,
		AvatarURL:   user.Avatar,
		Tier:        tierFromMember(user.Member),
		// MonthUsed / MonthLimit left as zero — credit accounting
		// integration is P-1.7 work.
		Quota: userinfoQ{},
	}
	if !user.MemberEndTime.IsZero() && user.Member > 0 {
		resp.TierExpiresAt = user.MemberEndTime.UTC().Format("2006-01-02T15:04:05Z")
	}
	c.JSON(http.StatusOK, resp)
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

// tierFromMember maps the integer member level on the user row to
// the wire-level tier string. The mapping is deliberately coarse for
// P0 — P1 / billing integration can refine into more nuanced tiers
// (e.g. distinguishing pro / team / enterprise).
//
// Reasoning:
//   - Member == 0 → "free" (default)
//   - Member  > 0 → "pro"  (any paid tier maps to "pro" for now)
//
// Note: the desktop renderer uses this string to gate quota display,
// not to enforce permissions. Server-side billing remains the source
// of truth.
func tierFromMember(member int) string {
	if member > 0 {
		return "pro"
	}
	return "free"
}

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
