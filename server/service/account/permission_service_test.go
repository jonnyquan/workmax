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
//
// The whole file is written against the single w_user.member vocabulary in
// model/user.go. The previous second enum in this package read member=1 as a
// paid "Creator" tier; member=1 is in fact the free-plan write value, so the
// matrix below pins 0 AND 1 to identical free-tier capabilities.

func TestGetMemberLevel_ExpiryDowngrade(t *testing.T) {
	s := &PermissionService{}
	future := time.Now().Add(30 * 24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	cases := []struct {
		name string
		user model.User
		want int
	}{
		// member=0 (never enrolled) and member=1 (free plan claimed) are both
		// free. Neither is subject to the paid-expiry collapse, so an unset or
		// lapsed end time cannot push them below free.
		{"Never-enrolled ignores end time", model.User{Member: MEMBER_FREE}, MEMBER_FREE},
		{"Free plan stays free plan", model.User{Member: MEMBER_FREE_PLAN, MemberEndTime: future}, MEMBER_FREE_PLAN},
		{"Free plan past end stays free plan", model.User{Member: MEMBER_FREE_PLAN, MemberEndTime: past}, MEMBER_FREE_PLAN},

		{"Pro with future end → Pro", model.User{Member: MEMBER_PRO, MemberEndTime: future}, MEMBER_PRO},
		{"Enterprise with future end → Enterprise", model.User{Member: MEMBER_ENTERPRISE, MemberEndTime: future}, MEMBER_ENTERPRISE},

		// Expired paid tiers fall back to FREE — this is the quiet
		// downgrade path. Frontend gating is a UX hint; THIS is the gate.
		{"Pro past end → Free", model.User{Member: MEMBER_PRO, MemberEndTime: past}, MEMBER_FREE},
		{"Enterprise past end → Free", model.User{Member: MEMBER_ENTERPRISE, MemberEndTime: past}, MEMBER_FREE},

		// An unset member_end_time on a paid level means "granted without an
		// expiry window", matching the billing path (hasActivePaidMembership /
		// isSubscriptionUserActive). Before the enum unification this package
		// read the zero time as "already expired" while the credits path read
		// it as active — the same user was Pro for spending and Free for
		// permissions.
		{"Pro without end time stays Pro", model.User{Member: MEMBER_PRO}, MEMBER_PRO},

		// Unknown//future levels above ENTERPRISE must not be silently trusted
		// past their expiry either.
		{"Unknown level past end → Free", model.User{Member: MEMBER_ENTERPRISE + 5, MemberEndTime: past}, MEMBER_FREE},
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
		name           string
		canPro         bool
		canBatch       bool
		canAPI         bool
		favoritesLimit int
		hasAds         bool
		priority       bool
	}

	freeExpectations := expected{
		name: "Free", canPro: false, canBatch: false, canAPI: false,
		favoritesLimit: FREE_FAVORITES_LIMIT, hasAds: true, priority: false,
	}
	paidExpectations := func(name string) expected {
		return expected{
			name: name, canPro: true, canBatch: true, canAPI: true,
			favoritesLimit: -1, hasAds: false, priority: true,
		}
	}

	cases := []struct {
		label string
		tier  int
		want  expected
	}{
		// member=0: never enrolled. No Pro model, no batch, no API, 10
		// favorites cap, ads on, no priority support.
		{"never enrolled", MEMBER_FREE, freeExpectations},

		// member=1: free plan claimed. IDENTICAL to member=0 — this is the
		// regression the unification fixes. Reading 1 as a paid "Creator" tier
		// handed CanUseProModel + unlimited favorites + no ads to every user
		// who claimed the free plan or was downgraded by a refund.
		{"free plan claimed", MEMBER_FREE_PLAN, freeExpectations},

		// member=2: the only tier any payment path writes today.
		{"paid pro", MEMBER_PRO, paidExpectations("Pro")},

		// member=3: reserved. No writer today, but it must degrade to the full
		// paid capability set rather than to a zero-valued permission struct.
		{"enterprise", MEMBER_ENTERPRISE, paidExpectations("Enterprise")},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
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

// An unknown member level (schema drift, a manual DB edit, a tier we add
// later without touching this switch) must fail closed onto the free tier
// rather than produce a zero-valued permission struct with FavoritesLimit=0
// and an empty MemberName.
func TestCalculatePermissions_UnknownLevelFailsClosedToFree(t *testing.T) {
	s := &PermissionService{}
	u := &model.User{Member: 99, MemberEndTime: time.Now().Add(time.Hour)}

	perm := s.calculatePermissions(u)
	if perm.MemberName != "Free" {
		t.Fatalf("MemberName = %q, want %q", perm.MemberName, "Free")
	}
	if perm.CanUseProModel || perm.CanUseBatchGen || perm.CanAccessAPI {
		t.Fatalf("unknown level must not carry paid capabilities: %+v", perm)
	}
	if perm.FavoritesLimit != FREE_FAVORITES_LIMIT {
		t.Fatalf("FavoritesLimit = %d, want %d", perm.FavoritesLimit, FREE_FAVORITES_LIMIT)
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

// The permission constants in this package are aliases of the single
// w_user.member vocabulary. If someone re-introduces a divergent value here,
// every paywall in the product silently shifts by one tier — pin the identity.
func TestPermissionConstantsAliasTheCanonicalMemberVocabulary(t *testing.T) {
	cases := []struct {
		name       string
		permission int
		canonical  int
	}{
		{"free", MEMBER_FREE, model.MEMBER_SUBSCRIPTION_NONE},
		{"free plan", MEMBER_FREE_PLAN, model.MEMBER_SUBSCRIPTION_FREE},
		{"pro", MEMBER_PRO, model.MEMBER_SUBSCRIPTION_PRO},
		{"enterprise", MEMBER_ENTERPRISE, model.MEMBER_SUBSCRIPTION_ENTERPRISE},
	}
	for _, tc := range cases {
		if tc.permission != tc.canonical {
			t.Errorf("%s: permission constant = %d, canonical = %d", tc.name, tc.permission, tc.canonical)
		}
	}
}
