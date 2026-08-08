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
		1,
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
		t.Errorf("Tier: got %q, want pro (Member=1)", resp.Tier)
	}
	if resp.TierExpiresAt == "" {
		t.Error("TierExpiresAt: empty (expected ISO 8601 from MemberEndTime)")
	}
	// Quota fields present and zero (P1 wires real values).
	if resp.Quota.MonthUsed != 0 || resp.Quota.MonthLimit != 0 {
		t.Errorf("Quota: got %+v, want zero-valued stub", resp.Quota)
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

func TestTierFromMember(t *testing.T) {
	cases := map[int]string{
		-1:  "free", // negative is treated as free (defensive)
		0:   "free",
		1:   "pro",
		2:   "pro",
		100: "pro",
	}
	for in, want := range cases {
		if got := tierFromMember(in); got != want {
			t.Errorf("tierFromMember(%d): got %q, want %q", in, got, want)
		}
	}
}
