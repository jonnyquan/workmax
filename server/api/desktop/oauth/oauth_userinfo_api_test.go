package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwt "github.com/golang-jwt/jwt/v4"

	"server/globals"
	"server/middleware"
	"server/model"
	desktopoauthmodel "server/model/desktop/oauth"
	request "server/model/system/request"
)

// newTestApiWithUserInfo extends newTestApiWithToken with the
// /userinfo route + a seeded user row. Returns the engine, api,
// and the seeded user.
func newTestApiWithUserInfo(t *testing.T) (*gin.Engine, *OauthApi, *model.User) {
	t.Helper()
	r, api := newTestApiWithToken(t)
	// The full model.User declares MySQL-specific GORM tags
	// (tinyint(1), double(13,2) etc) that confuse SQLite's
	// AutoMigrate. For this test we only need the columns the
	// handler actually reads, so hand-craft a minimal table.
	if err := api.DB.Exec(`
		CREATE TABLE w_user (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			email           TEXT NOT NULL DEFAULT '',
			nickname        TEXT NOT NULL DEFAULT '',
			avatar          TEXT NOT NULL DEFAULT '',
			member          INTEGER NOT NULL DEFAULT 0,
			member_end_time DATETIME,
			created_at      DATETIME,
			updated_at      DATETIME,
			deleted_at      DATETIME
		)
	`).Error; err != nil {
		t.Fatalf("create user table: %v", err)
	}
	// Raw INSERT — gorm.Create would emit every field from the
	// model.User struct (phone, identity_code, ...) regardless of
	// .Table(), and our minimal SQLite schema doesn't have them.
	if err := api.DB.Exec(
		`INSERT INTO w_user (email, nickname, avatar, member, member_end_time, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"user42@example.com",
		"User Forty Two",
		"https://example.com/avatar.png",
		// A real paid member. member=1 is the FREE-plan write value, not a paid
		// tier — TestUserInfo_FreePlanMemberIsNotReportedAsPro pins that case.
		model.MEMBER_SUBSCRIPTION_PRO,
		time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
		time.Now().UTC(),
		time.Now().UTC(),
	).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	user := &model.User{}
	if err := api.DB.Raw(`SELECT * FROM w_user WHERE email = ?`, "user42@example.com").Scan(user).Error; err != nil {
		t.Fatalf("re-read seeded user: %v", err)
	}

	r.GET("/api/desktop/oauth/userinfo", middleware.OAuthBearerAuth(desktopoauthmodel.DesktopClientID), api.UserInfo)
	return r, api, user
}

// mintAccessTokenForTest signs a JWT against the test SigningKey
// the way /token issueAccessToken does — but without going through
// the handler stack. Lets tests authenticate as a specific uid.
func mintAccessTokenForTest(t *testing.T, uid uint, clientID string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := request.CustomClaims{
		BaseClaims:      request.BaseClaims{Id: uid},
		OAuthClientID:   clientID,
		OAuthScope:      desktopoauthmodel.DesktopOAuthScopeWorkAgent,
		CredentialType:  desktopoauthmodel.DesktopCredentialDeviceSession,
		DeviceID:        "0123456789abcdef0123456789abcdef",
		DeviceSessionID: "session-test",
		BufferTime:      0,
		StandardClaims: jwt.StandardClaims{
			Audience:  desktopoauthmodel.DesktopResourceAudience,
			Subject:   "u_" + strconv.FormatUint(uint64(uid), 10),
			Id:        "jti-test",
			NotBefore: now.Add(-5 * time.Second).Unix(),
			IssuedAt:  now.Unix(),
			ExpiresAt: now.Add(15 * time.Minute).Unix(),
			Issuer:    globals.GraConf.JWT.Issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(globals.GraConf.JWT.SigningKey))
	if err != nil {
		t.Fatalf("sign test token: %v", err)
	}
	return signed
}

func TestUserInfo_Happy(t *testing.T) {
	r, _, user := newTestApiWithUserInfo(t)
	token := mintAccessTokenForTest(t, user.Id, "workmax-desktop")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp userinfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, w.Body.String())
	}
	if resp.UserID == "" || resp.UserID == "u_0" {
		t.Errorf("UserID: got %q", resp.UserID)
	}
	if resp.Email != "user42@example.com" {
		t.Errorf("Email: got %q", resp.Email)
	}
	if resp.DisplayName != "User Forty Two" {
		t.Errorf("DisplayName: got %q", resp.DisplayName)
	}
	if resp.AvatarURL != "https://example.com/avatar.png" {
		t.Errorf("AvatarURL: got %q", resp.AvatarURL)
	}
	if resp.Tier != "pro" {
		t.Errorf("Tier: got %q, want pro (Member=MEMBER_SUBSCRIPTION_PRO)", resp.Tier)
	}
	if resp.MemberStatus != "active" {
		t.Errorf("MemberStatus: got %q, want active", resp.MemberStatus)
	}
	if resp.TierExpiresAt == "" {
		t.Error("TierExpiresAt: empty (expected ISO 8601 from MemberEndTime)")
	}
	// This fixture's minimal schema has no w_credits_pack table, so the
	// credits read fails — and must degrade to zeros rather than failing the
	// whole account snapshot. TestUserInfo_ReportsRealCredits covers the
	// populated path.
	if resp.Quota.MonthUsed != 0 || resp.Quota.MonthLimit != 0 {
		t.Errorf("Quota: got %+v, want zeros when the credits read fails", resp.Quota)
	}
	if resp.Credits.Total != 0 || resp.Credits.Remaining != 0 {
		t.Errorf("Credits: got %+v, want zeros when the credits read fails", resp.Credits)
	}
}

func TestUserInfo_FreeUser(t *testing.T) {
	r, api, _ := newTestApiWithUserInfo(t)
	if err := api.DB.Exec(
		`INSERT INTO w_user (email, nickname, avatar, member, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"free@example.com", "Free", "", 0, time.Now().UTC(), time.Now().UTC(),
	).Error; err != nil {
		t.Fatalf("seed free user: %v", err)
	}
	var freeID uint
	if err := api.DB.Raw(`SELECT id FROM w_user WHERE email = ?`, "free@example.com").Scan(&freeID).Error; err != nil {
		t.Fatalf("lookup free user id: %v", err)
	}
	token := mintAccessTokenForTest(t, freeID, "workmax-desktop")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	var resp userinfoResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Tier != "free" {
		t.Errorf("Tier: got %q, want free", resp.Tier)
	}
	if resp.TierExpiresAt != "" {
		t.Errorf("TierExpiresAt: got %q, want empty for free tier", resp.TierExpiresAt)
	}
}

func TestUserInfo_NoToken_Rejected(t *testing.T) {
	r, _, _ := newTestApiWithUserInfo(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (body: %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, `Bearer error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate: got %q, want OAuth bearer invalid_token", got)
	}
	if strings.Contains(w.Body.String(), "user_id") {
		t.Errorf("missing token should not reach handler, got body: %s", w.Body.String())
	}
}

func TestUserInfo_BadToken_Rejected(t *testing.T) {
	r, _, _ := newTestApiWithUserInfo(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (body: %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, `Bearer error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate: got %q, want OAuth bearer invalid_token", got)
	}
	if strings.Contains(w.Body.String(), "user_id") {
		t.Errorf("bad token should not reach handler, got body: %s", w.Body.String())
	}
}

func TestUserInfo_LegacyJWTRejected(t *testing.T) {
	r, _, user := newTestApiWithUserInfo(t)
	token := mintAccessTokenForTest(t, user.Id, "")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (body: %s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "user_id") {
		t.Errorf("legacy JWT should not reach handler, got body: %s", w.Body.String())
	}
}

func TestUserInfo_WrongOAuthClientRejected(t *testing.T) {
	r, _, user := newTestApiWithUserInfo(t)
	token := mintAccessTokenForTest(t, user.Id, "other-desktop-client")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (body: %s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "user_id") {
		t.Errorf("wrong OAuth client should not reach handler, got body: %s", w.Body.String())
	}
}

func TestUserInfo_UnknownUID_404(t *testing.T) {
	r, _, _ := newTestApiWithUserInfo(t)
	// Mint a token for a uid that's not in the DB.
	token := mintAccessTokenForTest(t, 99999, "workmax-desktop")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestFormatUserID(t *testing.T) {
	cases := map[uint]string{
		0:    "",
		1:    "u_1",
		42:   "u_42",
		9999: "u_9999",
	}
	for in, want := range cases {
		if got := formatUserID(in); got != want {
			t.Errorf("formatUserID(%d): got %q, want %q", in, got, want)
		}
	}
}

// Tier derivation moved to model.EffectiveMemberTier so Desktop, the Portal
// and the model catalog cannot drift. Pin the mapping this endpoint depends
// on, including the case that used to be wrong: member=1 is the free-plan
// value and must NOT report as pro.
func TestTierMappingForUserInfo(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)

	cases := []struct {
		name   string
		member int
		end    time.Time
		want   string
	}{
		{"negative is defensive free", -1, future, "free"},
		{"never enrolled", model.MEMBER_SUBSCRIPTION_NONE, time.Time{}, "free"},
		{"free plan claimed", model.MEMBER_SUBSCRIPTION_FREE, future, "free"},
		{"paid pro", model.MEMBER_SUBSCRIPTION_PRO, future, "pro"},
		{"expired pro", model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(-time.Hour), "free"},
		{"enterprise", model.MEMBER_SUBSCRIPTION_ENTERPRISE, future, "enterprise"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := model.EffectiveMemberTier(tc.member, tc.end, time.Now()); got != tc.want {
				t.Errorf("tier(member=%d) = %q, want %q", tc.member, got, tc.want)
			}
		})
	}
}

// The regression this endpoint shipped with: a user who claimed the free plan
// (member=1, with a real future member_end_time) was reported to the Desktop
// as a Pro member with an expiry date, while the chat handler refused them the
// Pro model. Client and server must agree.
func TestUserInfo_FreePlanMemberIsNotReportedAsPro(t *testing.T) {
	r, api, _ := newTestApiWithUserInfo(t)
	if err := api.DB.Exec(
		`INSERT INTO w_user (email, nickname, avatar, member, member_end_time, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"freeplan@example.com", "Free Plan", "", model.MEMBER_SUBSCRIPTION_FREE,
		time.Now().Add(30*24*time.Hour).UTC(), time.Now().UTC(), time.Now().UTC(),
	).Error; err != nil {
		t.Fatalf("seed free-plan user: %v", err)
	}
	var uid uint
	if err := api.DB.Raw(`SELECT id FROM w_user WHERE email = ?`, "freeplan@example.com").Scan(&uid).Error; err != nil {
		t.Fatalf("lookup free-plan user id: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+mintAccessTokenForTest(t, uid, "workmax-desktop"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body %s", w.Code, w.Body.String())
	}
	var resp userinfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Tier != "free" {
		t.Errorf("Tier: got %q, want free for a free-plan member", resp.Tier)
	}
	if resp.MemberStatus != "free" {
		t.Errorf("MemberStatus: got %q, want free", resp.MemberStatus)
	}
	if resp.TierExpiresAt != "" {
		t.Errorf("TierExpiresAt: got %q — a free plan window is not a tier expiry", resp.TierExpiresAt)
	}
}

// An expired paid member is handled as unpaid: free tier, no expiry date
// dangling in the past, and member_status says why.
func TestUserInfo_ExpiredPaidMemberReadsAsFree(t *testing.T) {
	r, api, _ := newTestApiWithUserInfo(t)
	if err := api.DB.Exec(
		`INSERT INTO w_user (email, nickname, avatar, member, member_end_time, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"lapsed@example.com", "Lapsed", "", model.MEMBER_SUBSCRIPTION_PRO,
		time.Now().Add(-24*time.Hour).UTC(), time.Now().UTC(), time.Now().UTC(),
	).Error; err != nil {
		t.Fatalf("seed lapsed user: %v", err)
	}
	var uid uint
	if err := api.DB.Raw(`SELECT id FROM w_user WHERE email = ?`, "lapsed@example.com").Scan(&uid).Error; err != nil {
		t.Fatalf("lookup lapsed user id: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+mintAccessTokenForTest(t, uid, "workmax-desktop"))
	r.ServeHTTP(w, req)

	var resp userinfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Tier != "free" {
		t.Errorf("Tier: got %q, want free for a lapsed member", resp.Tier)
	}
	if resp.MemberStatus != "expired" {
		t.Errorf("MemberStatus: got %q, want expired", resp.MemberStatus)
	}
	if resp.TierExpiresAt != "" {
		t.Errorf("TierExpiresAt: got %q, want empty once the window has passed", resp.TierExpiresAt)
	}
}
