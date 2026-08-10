package account

import (
	"testing"
	"time"

	"server/globals"
	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// IsPremiumMember is the backend gate for the Pro ("work-plus") model tier in
// the Agent chat handler. It reads the same w_user.member integer that
// PermissionService reads, so the two must agree on every level — a
// disagreement is what let a free-plan user (member=1) be "Creator" for
// permissions while being non-premium for the model gate.
func TestIsPremiumMember_PerLevel(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	future := time.Now().Add(30 * 24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	cases := []struct {
		name    string
		member  int
		endTime time.Time
		want    bool
	}{
		// Free band: neither value is a paid entitlement, regardless of the
		// window on the row. member=1 in particular is the free-plan write
		// value and carries a real (future) member_end_time.
		{"never enrolled", model.MEMBER_SUBSCRIPTION_NONE, time.Time{}, false},
		{"never enrolled with future window", model.MEMBER_SUBSCRIPTION_NONE, future, false},
		{"free plan with future window", model.MEMBER_SUBSCRIPTION_FREE, future, false},
		{"free plan with lapsed window", model.MEMBER_SUBSCRIPTION_FREE, past, false},

		// Paid band.
		{"pro with future window", model.MEMBER_SUBSCRIPTION_PRO, future, true},
		{"pro with lapsed window", model.MEMBER_SUBSCRIPTION_PRO, past, false},
		// No window recorded = granted without an expiry. This matches the
		// billing path (hasActivePaidMembership / isSubscriptionUserActive):
		// those users can spend subscription credits, so they must also pass
		// the model gate.
		{"pro without window", model.MEMBER_SUBSCRIPTION_PRO, time.Time{}, true},

		{"enterprise with future window", model.MEMBER_SUBSCRIPTION_ENTERPRISE, future, true},
		{"enterprise with lapsed window", model.MEMBER_SUBSCRIPTION_ENTERPRISE, past, false},
	}

	service := NewQuotaService()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := model.User{
				Nickname:      tc.name,
				Email:         tc.name + "@example.com",
				Member:        tc.member,
				MemberEndTime: tc.endTime,
			}
			if err := db.Create(&user).Error; err != nil {
				t.Fatalf("seed user: %v", err)
			}

			got, err := service.IsPremiumMember(int(user.Id))
			if err != nil {
				t.Fatalf("IsPremiumMember: %v", err)
			}
			if got != tc.want {
				t.Fatalf("IsPremiumMember = %v, want %v", got, tc.want)
			}

			// The two gates must never disagree: whenever IsPremiumMember says
			// yes, PermissionService must be handing out the paid capability
			// set, and vice versa.
			perm := (&PermissionService{}).calculatePermissions(&user)
			if perm.CanUseProModel != tc.want {
				t.Fatalf("PermissionService disagrees: CanUseProModel = %v, IsPremiumMember = %v",
					perm.CanUseProModel, tc.want)
			}
		})
	}
}
