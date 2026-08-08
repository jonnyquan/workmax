package account

import (
	"server/model"
	"testing"
	"time"
)

// permission_service.go drives every paywall decision on the server
// (CanUseProModel, CanUseBatchGeneration, FavoritesLimit). The lookup
// path funnels through getMemberLevel + calculatePermissions, both of
// which are pure on *model.User, so we can pin them without a DB. A
// silent drift in tier→capability mapping would either leak Pro features
// to Free users or lock paying users out of features they're entitled to.
// The expiry check on getMemberLevel is also load-bearing: it's the ONLY
// place a paid tier gets downgraded back to FREE when MemberEndTime lapses.

func TestGetMemberLevel_ExpiryDowngrade(t *testing.T) {
	s := &PermissionService{}
	future := time.Now().Add(30 * 24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	cases := []struct {
		name string
		user model.User
		want int
	}{
		// Member=0 is Free; the expiry branch is gated on Member > 0, so
		// a zero-valued MemberEndTime on a Free user does NOT mis-classify
		// as "expired" — Free stays Free.
		{"Free user ignores end time", model.User{Member: MEMBER_FREE}, MEMBER_FREE},

		{"Creator with future end → Creator", model.User{Member: MEMBER_CREATOR, MemberEndTime: future}, MEMBER_CREATOR},
		{"Pro with future end → Pro", model.User{Member: MEMBER_PRO, MemberEndTime: future}, MEMBER_PRO},
		{"Lifetime with future end → Lifetime", model.User{Member: MEMBER_LIFETIME, MemberEndTime: future}, MEMBER_LIFETIME},

		// Expired paid tiers fall back to FREE — this is the quiet
		// downgrade path. Frontend gating is a UX hint; THIS is the gate.
		{"Creator past end → Free", model.User{Member: MEMBER_CREATOR, MemberEndTime: past}, MEMBER_FREE},
		{"Pro past end → Free", model.User{Member: MEMBER_PRO, MemberEndTime: past}, MEMBER_FREE},

		// "Lifetime" in this enum is still subject to the expiry check —
		// the domain model expresses lifetime by setting an end time far
		// in the future, not by a special "never expires" branch. Pin
		// this so a future "lifetime bypasses expiry" change is visible.
		{"Lifetime past end → Free (no special bypass)", model.User{Member: MEMBER_LIFETIME, MemberEndTime: past}, MEMBER_FREE},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.getMemberLevel(&tc.user); got != tc.want {
				t.Errorf("getMemberLevel = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCalculatePermissions_PerTierMatrix(t *testing.T) {
	s := &PermissionService{}
	future := time.Now().Add(30 * 24 * time.Hour)

	type expected struct {
		name            string
		canPro          bool
		canBatch        bool
		canAPI          bool
		favoritesLimit  int
		hasAds          bool
		priority        bool
	}

	cases := []struct {
		tier int
		want expected
	}{
		// Free: no Pro model, no batch, no API, 10 favorites cap,
		// ads on, no priority support. Changing any of these here
		// silently widens or narrows the free tier.
		{MEMBER_FREE, expected{
			name: "Free", canPro: false, canBatch: false, canAPI: false,
			favoritesLimit: FREE_FAVORITES_LIMIT, hasAds: true, priority: false,
		}},
		// Creator: Pro model unlocked, no batch, no API, unlimited
		// favorites, no ads, no priority. Batch+API are the Pro-tier
		// differentiators — the Creator tier must NOT include them.
		{MEMBER_CREATOR, expected{
			name: "Creator", canPro: true, canBatch: false, canAPI: false,
			favoritesLimit: -1, hasAds: false, priority: false,
		}},
		// Pro: full capability set. API access and priority support
		// are both reserved for this tier and above.
		{MEMBER_PRO, expected{
			name: "Pro", canPro: true, canBatch: true, canAPI: true,
			favoritesLimit: -1, hasAds: false, priority: true,
		}},
		// Lifetime: same capability set as Pro but with a distinct
		// MemberName for UI badge rendering. If Lifetime silently
		// drifted to match Pro verbatim (or vice versa), the account
		// page badge would show the wrong tier.
		{MEMBER_LIFETIME, expected{
			name: "Lifetime", canPro: true, canBatch: true, canAPI: true,
			favoritesLimit: -1, hasAds: false, priority: true,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.want.name, func(t *testing.T) {
			u := &model.User{Member: tc.tier, MemberEndTime: future}
			got := s.calculatePermissions(u)
			if got.MemberLevel != tc.tier {
				t.Errorf("MemberLevel = %d, want %d", got.MemberLevel, tc.tier)
			}
			if got.MemberName != tc.want.name {
				t.Errorf("MemberName = %q, want %q", got.MemberName, tc.want.name)
			}
			if got.CanUseProModel != tc.want.canPro {
				t.Errorf("CanUseProModel = %v, want %v", got.CanUseProModel, tc.want.canPro)
			}
			if got.CanUseBatchGen != tc.want.canBatch {
				t.Errorf("CanUseBatchGen = %v, want %v", got.CanUseBatchGen, tc.want.canBatch)
			}
			if got.CanAccessAPI != tc.want.canAPI {
				t.Errorf("CanAccessAPI = %v, want %v", got.CanAccessAPI, tc.want.canAPI)
			}
			if got.FavoritesLimit != tc.want.favoritesLimit {
				t.Errorf("FavoritesLimit = %d, want %d", got.FavoritesLimit, tc.want.favoritesLimit)
			}
			if got.HasAds != tc.want.hasAds {
				t.Errorf("HasAds = %v, want %v", got.HasAds, tc.want.hasAds)
			}
			if got.HasPrioritySupport != tc.want.priority {
				t.Errorf("HasPrioritySupport = %v, want %v", got.HasPrioritySupport, tc.want.priority)
			}
		})
	}
}

// Expiry interacts with calculatePermissions via getMemberLevel — a
// user with Member=PRO but a past end-time must be presented as Free.
// This is what prevents a lapsed-subscription user from keeping Pro
// privileges until their next login.
func TestCalculatePermissions_ExpiredUserDowngradesToFree(t *testing.T) {
	s := &PermissionService{}
	past := time.Now().Add(-time.Hour)

	u := &model.User{Member: MEMBER_PRO, MemberEndTime: past}
	perm := s.calculatePermissions(u)

	if perm.MemberLevel != MEMBER_FREE {
		t.Fatalf("expired Pro must collapse to Free tier, got level %d", perm.MemberLevel)
	}
	if perm.MemberName != "Free" {
		t.Fatalf("MemberName = %q, want %q", perm.MemberName, "Free")
	}
	if perm.CanAccessAPI || perm.CanUseBatchGen || perm.CanUseProModel {
		t.Fatalf("expired Pro still has paid capabilities: %+v", perm)
	}
	if perm.FavoritesLimit != FREE_FAVORITES_LIMIT {
		t.Fatalf("FavoritesLimit = %d, want %d", perm.FavoritesLimit, FREE_FAVORITES_LIMIT)
	}
	if !perm.HasAds {
		t.Fatalf("expired Pro should see ads")
	}
}
