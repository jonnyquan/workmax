package globalmodel

import (
	"testing"

	"server/model"
)

// required_tier is operator-editable free text in the DB. Both directions of
// the mapping are load-bearing: a typo must not lock everyone out, and a
// higher tier must satisfy a lower requirement.
func TestNormalizeRequiredTier(t *testing.T) {
	cases := map[string]string{
		"":           model.MemberTierFree,
		"  ":         model.MemberTierFree,
		"free":       model.MemberTierFree,
		"FREE":       model.MemberTierFree,
		"pro":        model.MemberTierPro,
		" Pro ":      model.MemberTierPro,
		"premium":    model.MemberTierPro,
		"paid":       model.MemberTierPro,
		"enterprise": model.MemberTierEnterprise,
		"nonsense":   model.MemberTierFree, // fail open, never lock everyone out
	}
	for in, want := range cases {
		if got := NormalizeRequiredTier(in); got != want {
			t.Errorf("NormalizeRequiredTier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTierSatisfiesRequirement(t *testing.T) {
	cases := []struct {
		caller   string
		required string
		want     bool
	}{
		{model.MemberTierFree, model.MemberTierFree, true},
		{model.MemberTierFree, model.MemberTierPro, false},
		{model.MemberTierFree, model.MemberTierEnterprise, false},
		{model.MemberTierPro, model.MemberTierFree, true},
		{model.MemberTierPro, model.MemberTierPro, true},
		{model.MemberTierPro, model.MemberTierEnterprise, false},
		{model.MemberTierEnterprise, model.MemberTierPro, true},
		{model.MemberTierEnterprise, model.MemberTierEnterprise, true},
		// An unreadable requirement degrades to free rather than to "deny".
		{model.MemberTierFree, "typo", true},
	}
	for _, tc := range cases {
		if got := TierSatisfiesRequirement(tc.caller, tc.required); got != tc.want {
			t.Errorf("TierSatisfiesRequirement(%q, %q) = %v, want %v", tc.caller, tc.required, got, tc.want)
		}
	}
}
