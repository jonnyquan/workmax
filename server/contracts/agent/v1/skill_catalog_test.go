package v1

import "testing"

func TestOfficialSkillsAreUniqueAndComplete(t *testing.T) {
	seen := make(map[string]struct{})
	for _, skill := range OfficialSkills() {
		if skill.AgentMode == "" {
			t.Fatal("official skill has empty agent mode")
		}
		if skill.DisplayName == "" {
			t.Errorf("skill %q has empty display name", skill.AgentMode)
		}
		if skill.Description == "" {
			t.Errorf("skill %q has empty description", skill.AgentMode)
		}
		if _, exists := seen[skill.AgentMode]; exists {
			t.Errorf("duplicate official skill %q", skill.AgentMode)
		}
		seen[skill.AgentMode] = struct{}{}
	}

	if len(seen) != 14 {
		t.Fatalf("official skill count = %d, want 14", len(seen))
	}
}

func TestOfficialSkillsReturnsDefensiveCopy(t *testing.T) {
	first := OfficialSkills()
	first[0].AgentMode = "mutated"

	second := OfficialSkills()
	if second[0].AgentMode == "mutated" {
		t.Fatal("OfficialSkills exposed mutable package state")
	}
}

func TestOfficialAgentModeSetAndLookupStayAligned(t *testing.T) {
	set := OfficialAgentModeSet()
	for _, mode := range OfficialAgentModes() {
		if _, ok := set[mode]; !ok {
			t.Errorf("mode %q missing from lookup set", mode)
		}
		if descriptor, ok := LookupOfficialSkill(mode); !ok || descriptor.AgentMode != mode {
			t.Errorf("lookup %q = (%+v, %v)", mode, descriptor, ok)
		}
	}
}
