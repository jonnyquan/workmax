// Package v1 contains dependency-free wire and catalog contracts shared by
// the WorkMax Go Server and the Desktop client stack.
//
// Keep this package free of Gin, GORM, service and renderer imports. It is a
// contract leaf: business implementations may depend on it, but it must not
// depend on business implementations.
package v1

// SkillCatalogVersion identifies the shape and canonical contents of the
// first-party Agent skill catalog. It is intentionally independent from an
// individual skill's prompt/manifest version.
const SkillCatalogVersion = "1.0.0-draft"

// SkillDescriptor is the server-owned, client-neutral identity of a
// first-party Agent skill. DisplayName and Description are canonical English
// fallbacks; localized Desktop strings may be added by a later compatible
// contract revision without moving ownership back to a Web bundle.
type SkillDescriptor struct {
	AgentMode   string `json:"agentMode"`
	DisplayName string `json:"name"`
	Description string `json:"description"`
}

var officialSkills = []SkillDescriptor{
	{AgentMode: "ppt", DisplayName: "Presentation", Description: "Create and refine presentation decks."},
	{AgentMode: "flashCard", DisplayName: "Flash Cards", Description: "Create visual learning cards for a topic and audience."},
	{AgentMode: "pictureBook", DisplayName: "Picture Book", Description: "Design illustrated storybook scenes and page sequences."},
	{AgentMode: "character", DisplayName: "Character", Description: "Design consistent characters, poses, expressions, and outfits."},
	{AgentMode: "logo", DisplayName: "Logo", Description: "Explore and refine logo directions and brand marks."},
	{AgentMode: "productShot", DisplayName: "Product Shot", Description: "Create product-focused visual concepts and compositions."},
	{AgentMode: "modelTryOn", DisplayName: "Model Try-On", Description: "Create garment try-on concepts while preserving product fidelity."},
	{AgentMode: "marketingPoster", DisplayName: "Marketing Poster", Description: "Design promotional posters with a clear message and call to action."},
	{AgentMode: "lifestyle", DisplayName: "Lifestyle", Description: "Create lifestyle scenes grounded in supplied brand and product assets."},
	{AgentMode: "packaging", DisplayName: "Packaging", Description: "Develop packaging concepts, hierarchy, and presentation views."},
	{AgentMode: "socialAd", DisplayName: "Social Ad", Description: "Create social advertising concepts for feed and story placements."},
	{AgentMode: "webBanner", DisplayName: "Web Banner", Description: "Design responsive web banner concepts and export-ready layouts."},
	{AgentMode: "mobileStory", DisplayName: "Mobile Story", Description: "Create vertical mobile story sequences and motion-ready frames."},
	{AgentMode: "oohBillboard", DisplayName: "OOH Billboard", Description: "Design out-of-home billboard concepts for distance readability."},
}

// OfficialSkills returns a defensive copy so callers cannot mutate the
// process-wide catalog.
func OfficialSkills() []SkillDescriptor {
	out := make([]SkillDescriptor, len(officialSkills))
	copy(out, officialSkills)
	return out
}

// OfficialAgentModes returns the canonical first-party mode identifiers in
// catalog order.
func OfficialAgentModes() []string {
	out := make([]string, 0, len(officialSkills))
	for _, skill := range officialSkills {
		out = append(out, skill.AgentMode)
	}
	return out
}

// OfficialAgentModeSet returns a new lookup set on every call. It is useful at
// HTTP admission boundaries that need O(1) validation without duplicating the
// catalog literal.
func OfficialAgentModeSet() map[string]struct{} {
	out := make(map[string]struct{}, len(officialSkills))
	for _, skill := range officialSkills {
		out[skill.AgentMode] = struct{}{}
	}
	return out
}

// LookupOfficialSkill returns the canonical descriptor for an Agent mode.
func LookupOfficialSkill(agentMode string) (SkillDescriptor, bool) {
	for _, skill := range officialSkills {
		if skill.AgentMode == agentMode {
			return skill, true
		}
	}
	return SkillDescriptor{}, false
}
