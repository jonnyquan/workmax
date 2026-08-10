package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"server/model"
	request "server/model/system/request"
	"server/utils/testutil"
)

// The credits half of userinfo runs against the full test schema (w_user +
// w_credits_pack + w_credit_reservation) rather than the minimal hand-rolled
// table the rest of this package's fixtures use, because the whole point is
// that the numbers come from the same tables the Portal reads.
func newUserInfoCreditsEngine(t *testing.T, uid uint) (*gin.Engine, *OauthApi) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	api := &OauthApi{DB: testutil.NewTestDB(t)}
	engine := gin.New()
	engine.GET("/userinfo", func(c *gin.Context) {
		c.Set("claims", &request.CustomClaims{BaseClaims: request.BaseClaims{Id: uid}})
		c.Next()
	}, api.UserInfo)
	return engine, api
}

func fetchUserInfo(t *testing.T, engine *gin.Engine) userinfoResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/userinfo", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp userinfoResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body.String())
	}
	return resp
}

func TestUserInfo_ReportsRealCredits(t *testing.T) {
	engine, api := newUserInfoCreditsEngine(t, 1)

	user := model.User{
		Email:         "credits@example.com",
		Nickname:      "Credits",
		Member:        model.MEMBER_SUBSCRIPTION_PRO,
		MemberEndTime: time.Now().Add(30 * 24 * time.Hour),
	}
	if err := api.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Two packs: a subscription allowance (only counted while the membership
	// is active) and a purchased top-up.
	packs := []model.CreditsPack{
		{UID: int(user.Id), SourceType: model.CreditsSourceSubscription, SourceID: "sub_userinfo", CreditsTotal: 1000, CreditsUsed: 250},
		{UID: int(user.Id), SourceType: model.CreditsSourcePurchase, SourceID: "pack_userinfo", CreditsTotal: 500, CreditsUsed: 0},
	}
	for _, pack := range packs {
		if err := api.DB.Create(&pack).Error; err != nil {
			t.Fatalf("seed pack %s: %v", pack.SourceID, err)
		}
	}

	resp := fetchUserInfo(t, engine)

	if resp.Tier != "pro" || resp.MemberStatus != "active" {
		t.Fatalf("tier/status = %q/%q, want pro/active", resp.Tier, resp.MemberStatus)
	}
	if resp.Credits.Total != 1500 || resp.Credits.Used != 250 || resp.Credits.Remaining != 1250 {
		t.Fatalf("credits = %+v, want total 1500 / used 250 / remaining 1250", resp.Credits)
	}
	// The legacy quota keys are kept on the wire and now carry the same
	// allowance, so an older renderer stops displaying a permanent zero.
	if resp.Quota.MonthLimit != 1500 || resp.Quota.MonthUsed != 250 {
		t.Fatalf("quota = %+v, want limit 1500 / used 250", resp.Quota)
	}
}

// A free user must not be credited with the subscription pack left over from a
// lapsed membership — the spend path already refuses it, so the snapshot has to
// agree or the Desktop advertises a balance the user cannot spend.
func TestUserInfo_ExpiredMemberDropsSubscriptionCredits(t *testing.T) {
	engine, api := newUserInfoCreditsEngine(t, 1)

	user := model.User{
		Email:         "lapsed-credits@example.com",
		Nickname:      "Lapsed",
		Member:        model.MEMBER_SUBSCRIPTION_PRO,
		MemberEndTime: time.Now().Add(-24 * time.Hour),
	}
	if err := api.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, pack := range []model.CreditsPack{
		{UID: int(user.Id), SourceType: model.CreditsSourceSubscription, SourceID: "sub_lapsed", CreditsTotal: 1000, CreditsUsed: 0},
		{UID: int(user.Id), SourceType: model.CreditsSourcePurchase, SourceID: "pack_lapsed", CreditsTotal: 300, CreditsUsed: 100},
	} {
		if err := api.DB.Create(&pack).Error; err != nil {
			t.Fatalf("seed pack %s: %v", pack.SourceID, err)
		}
	}

	resp := fetchUserInfo(t, engine)

	if resp.Tier != "free" || resp.MemberStatus != "expired" {
		t.Fatalf("tier/status = %q/%q, want free/expired", resp.Tier, resp.MemberStatus)
	}
	if resp.Credits.Total != 300 || resp.Credits.Remaining != 200 {
		t.Fatalf("credits = %+v, want only the purchased pack (total 300 / remaining 200)", resp.Credits)
	}
}

// Credits already debited by an in-flight turn are still refundable. Reporting
// them without the split would make a running turn look like a permanent
// charge.
func TestUserInfo_SplitsReservedPendingOutOfUsed(t *testing.T) {
	engine, api := newUserInfoCreditsEngine(t, 1)

	user := model.User{
		Email:    "reserved@example.com",
		Nickname: "Reserved",
		Member:   model.MEMBER_SUBSCRIPTION_NONE,
	}
	if err := api.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := api.DB.Create(&model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase, SourceID: "pack_reserved",
		CreditsTotal: 400, CreditsUsed: 120,
	}).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	if err := api.DB.Create(&model.CreditReservation{
		UID: int(user.Id), Reserved: 70, Status: model.CreditReservationStatusReserved,
	}).Error; err != nil {
		t.Fatalf("seed reservation: %v", err)
	}

	resp := fetchUserInfo(t, engine)

	if resp.Credits.Used != 120 || resp.Credits.ReservedPending != 70 {
		t.Fatalf("credits = %+v, want used 120 with 70 still reservable-back", resp.Credits)
	}
	if resp.Credits.Remaining != 280 {
		t.Fatalf("remaining = %d, want 280", resp.Credits.Remaining)
	}
}
