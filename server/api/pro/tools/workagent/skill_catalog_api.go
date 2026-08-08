package workagent

// skill_catalog_api.go — F3 (2026-05-17). Exposes the user-facing
// skill catalog so the FE can fetch runtime metadata (version,
// question-form / directions-fallback presence, post-script
// presence, artifact matrix, i18n label tokens) without hand-
// mirroring capability flags in TS.
//
// Endpoint:
//
//   GET /api/work-agent/skills
//
// Auth: uid-scoped (the catalog itself is universal — every
// authenticated user sees the same list today — but the gate
// reserves space for future per-user gating, e.g. paid skills,
// experimental beta skills surfaced only to internal team).
//
// Wire shape (deliberately structural, NOT human strings):
//
//   {
//     "code": 0,
//     "data": {
//       "items": [
//         {
//           "agentMode": "ppt",
//           "version": "2.0.0",
//           "hasQuestionForm": true,
//           "hasDirectionsFallback": true,
//           "hasPostScripts": true,
//           "requiredInputs": [{"kind":"topic"}],
//           "riskHints": ["post_generation_validation"],
//           "source": "official",
//           "status": "published",
//           "permissions": ["use"],
//           "artifacts": {
//             "primaryType": "deck",
//             "outputTypes": ["pptx", "pdf"],
//             "previewTypes": ["deck", "pdf"],
//             "exportTargets": ["pptx", "pdf"],
//             "critiqueAnchors": ["functionality", "hierarchy"]
//           },
//           "labelKey": "WorkAgent.modeSelector.modes.ppt.name",
//           "descriptionKey": "WorkAgent.modeSelector.modes.ppt.description"
//         },
//         ...
//       ],
//       "count": 14
//     }
//   }
//
// Name / description are canonical server-owned fallbacks from the
// dependency-free Agent v1 contract. labelKey / descriptionKey remain on the
// wire as legacy compatibility tokens while Desktop localization moves to a
// versioned Go-owned catalog; Web is not an Agent catalog owner.
//
// Why not include SystemPrompt: the prompt body is server-only,
// shipping it would balloon the payload and leak prompt-engineering
// details the platform keeps tunable.
//
// Desktop may generate a typed union from this contract for exhaustive client
// checks. The runtime catalog remains authoritative for publication,
// capability, tier and rollout state.

import (
	"hash/crc32"
	"os"
	"sort"
	"strconv"
	"strings"

	agentv1 "server/contracts/agent/v1"
	"server/globals"
	"server/model"
	"server/model/common/response"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/service/tools/workagent/skills"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// skillCatalogItem is the per-skill wire row. Fields are
// structural — booleans + a version string + canonical labels and legacy i18n
// tokens the Desktop can use to decide UX (e.g. "this skill has a question
// form, render it as a multi-step wizard"; "this skill has no
// directions fallback, hide the picker on first turn"; "render
// the picker label by piping labelKey through useTranslations").
type skillCatalogItem struct {
	AgentMode             string                    `json:"agentMode"`
	Name                  string                    `json:"name"`
	Description           string                    `json:"description"`
	Version               string                    `json:"version"`
	HasQuestionForm       bool                      `json:"hasQuestionForm"`
	HasDirectionsFallback bool                      `json:"hasDirectionsFallback"`
	HasPostScripts        bool                      `json:"hasPostScripts"`
	RequiredInputs        []skills.InputRequirement `json:"requiredInputs,omitempty"`
	RiskHints             []string                  `json:"riskHints,omitempty"`
	Source                string                    `json:"source,omitempty"`
	Status                string                    `json:"status,omitempty"`
	Permissions           []string                  `json:"permissions,omitempty"`
	RequiredTier          string                    `json:"requiredTier,omitempty"`
	AccessRequestID       uint                      `json:"accessRequestId,omitempty"`
	AccessRequestStatus   string                    `json:"accessRequestStatus,omitempty"`
	Artifacts             *skills.ArtifactMetadata  `json:"artifacts,omitempty"`
	// LabelKey / DescriptionKey are legacy i18n tokens. By default they follow
	// the convention
	// `WorkAgent.modeSelector.modes.<agentMode>.{name,description}`;
	// a future skill can opt out of the convention by setting
	// skill.yaml `i18n.labelKey` / `i18n.descriptionKey`. The
	// optional override path is unused today — all 14 skills are
	// convention-derived (see TestListSkills_LabelKeyConvention).
	LabelKey       string `json:"labelKey"`
	DescriptionKey string `json:"descriptionKey"`
}

// legacyPickerI18nNamespace is the compatibility locale-token prefix.
// Centralised here so a future
// rename ("WorkAgent.modeSelector.modes" → "WorkAgent.skills")
// is a one-line change AND the convention stays inspectable from
// outside (the contract test reads this constant).
const legacyPickerI18nNamespace = "WorkAgent.modeSelector.modes"

// deriveLabelKey returns the conventional i18n token for a
// skill's display name. A future skill with non-convention
// labels can short-circuit this by populating skill.yaml's
// (currently unused) i18n.labelKey field; the bundle path is
// kept as the override point so the source-of-truth stays in
// the skill manifest rather than scattered through Go code.
func deriveLabelKey(agentMode string) string {
	return legacyPickerI18nNamespace + "." + agentMode + ".name"
}

// deriveDescriptionKey — parallel of deriveLabelKey for the
// secondary text under each picker tile.
func deriveDescriptionKey(agentMode string) string {
	return legacyPickerI18nNamespace + "." + agentMode + ".description"
}

// ListSkills returns the user-facing skill catalog. Always-200
// once auth clears — an empty list (no skills configured) is a
// legitimate state, not an error.
//
// Iteration order is alphabetical by agentMode so the FE can
// render the picker deterministically without resorting (FE
// could still re-sort by display name when locale is in scope).
//
// Per-skill metadata pulled from the live SkillBundle (Build()
// call per skill) so the response reflects what the runtime
// actually loads. A malformed skill that fails Build() falls
// through to the "unknown" version + zero booleans path rather
// than crashing the catalog response — observability is via the
// existing registry warn-once channel.
func (api *AIChatApiNew) ListSkills(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	// Source of truth = allowedAgentModes (the same gate the
	// chat handler uses). Sorted alphabetically for stable
	// frontend rendering — picker UX should not depend on Go's
	// map iteration order.
	modes := sortedSkillCatalogModes()

	registry := workagentService.GetSkillRegistry()
	requests, err := workagentService.NewSkillAccessRequestRepository(nil).ListForUser(int(uid))
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	projectID := parseSkillCatalogProjectID(c)
	items := make([]skillCatalogItem, 0, len(modes))
	for _, mode := range modes {
		item := buildSkillCatalogItem(registry, mode)
		if item.Source == "gray" && !isSkillGrayCohortMember(int(uid), mode, projectID) {
			item.Status = "unpublished"
			item.Permissions = nil
			item.AccessRequestID = 0
			item.AccessRequestStatus = ""
			items = append(items, item)
			continue
		}
		if item.Source == "paid" && !stringSliceContains(item.Permissions, "use") && hasSkillRequiredTierEntitlement(int(uid), item.RequiredTier) {
			item.Permissions = append(item.Permissions, "use")
		}
		if request, ok := requests[mode]; ok {
			item.AccessRequestID = request.Id
			item.AccessRequestStatus = request.Status
			if request.Status == workagentModel.SkillAccessRequestStatusApproved && !stringSliceContains(item.Permissions, "use") {
				item.Permissions = append(item.Permissions, "use")
			}
		}
		items = append(items, item)
	}

	response.OkWithData(gin.H{
		"catalogVersion": agentv1.SkillCatalogVersion,
		"items":          items,
		"count":          len(items),
	}, c)
}

func sortedSkillCatalogModes() []string {
	modes := make([]string, 0, len(allowedAgentModes))
	for m := range allowedAgentModes {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	return modes
}

func parseSkillCatalogProjectID(c *gin.Context) uint {
	for _, key := range []string{"projectId", "project_id"} {
		if raw := strings.TrimSpace(c.Query(key)); raw != "" {
			parsed, err := strconv.ParseUint(raw, 10, 32)
			if err == nil {
				return uint(parsed)
			}
			return 0
		}
	}
	return 0
}

func (api *AIChatApiNew) ListSkillAccessRequests(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	limit := 100
	if rawLimit := c.Query("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	rows, err := workagentService.NewSkillAccessRequestRepository(nil).List(c.Query("status"), limit)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"items": rows,
		"count": len(rows),
	}, c)
}

func (api *AIChatApiNew) UpdateSkillAccessRequestStatus(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	requestID, err := strconv.ParseUint(c.Param("requestId"), 10, 32)
	if err != nil || requestID == 0 {
		response.FailWithMessage("Invalid request ID", c)
		return
	}
	var req struct {
		Status     string `json:"status"`
		ReviewNote string `json:"reviewNote"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request body", c)
		return
	}
	row, err := workagentService.NewSkillAccessRequestRepository(nil).UpdateStatus(uint(requestID), req.Status, int(uid), req.ReviewNote)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"request_id":  row.Id,
		"uid":         row.UID,
		"agent_mode":  row.AgentMode,
		"source":      row.Source,
		"status":      row.Status,
		"reviewed_by": row.ReviewedBy,
		"reviewed_at": row.ReviewedAt,
		"review_note": row.ReviewNote,
	}, c)
}

func (api *AIChatApiNew) RequestSkillAccess(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	agentMode := c.Param("agentMode")
	if _, ok := allowedAgentModes[agentMode]; !ok {
		response.FailWithMessage("Unknown skill", c)
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request body", c)
		return
	}
	item := buildSkillCatalogItem(workagentService.GetSkillRegistry(), agentMode)
	row, err := workagentService.NewSkillAccessRequestRepository(nil).UpsertPending(int(uid), agentMode, item.Source, req.Reason)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"request_id": row.Id,
		"agent_mode": row.AgentMode,
		"source":     row.Source,
		"status":     row.Status,
	}, c)
}

// ListDesignSystems exposes the shipped design/media systems used by
// visual directions and future project-asset pickers. The catalog is
// universal today, but still requires auth because the Work Agent API
// surface is user-scoped and future project-local systems will be
// merged here.
func (api *AIChatApiNew) ListDesignSystems(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	items, err := skills.ListDesignSystemCatalog()
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if rawProjectID := c.Query("projectId"); rawProjectID != "" {
		projectID, err := strconv.ParseUint(rawProjectID, 10, 32)
		if err != nil {
			response.FailWithMessage("Invalid project ID", c)
			return
		}
		includePending := strings.EqualFold(strings.TrimSpace(c.Query("includePending")), "true") || strings.TrimSpace(c.Query("includePending")) == "1"
		projectItems, err := workagentService.NewArtifactAssetCandidateRepository(nil).ListDesignSystemsForProject(int(uid), uint(projectID), includePending)
		if err != nil {
			response.FailWithMessage(err.Error(), c)
			return
		}
		items = append(items, projectItems...)
	}
	response.OkWithData(gin.H{
		"items": items,
		"count": len(items),
	}, c)
}

func (api *AIChatApiNew) UpdateProjectDesignSystemStatus(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}
	designSystemID, err := strconv.ParseUint(c.Param("designSystemId"), 10, 32)
	if err != nil || designSystemID == 0 {
		response.FailWithMessage("Invalid design system ID", c)
		return
	}
	var req struct {
		Status     string `json:"status"`
		ReviewNote string `json:"reviewNote"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request body", c)
		return
	}
	row, err := workagentService.NewArtifactAssetCandidateRepository(nil).UpdateProjectDesignSystemStatus(int(uid), uint(projectID), uint(designSystemID), req.Status, req.ReviewNote)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"project_id":       row.ProjectID,
		"design_system_id": row.Id,
		"status":           row.Status,
		"reviewed_by":      row.ReviewedBy,
		"reviewed_at":      row.ReviewedAt,
		"review_note":      row.ReviewNote,
	}, c)
}

func (api *AIChatApiNew) GetProjectDesignSystemHistory(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}
	designSystemID, err := strconv.ParseUint(c.Param("designSystemId"), 10, 32)
	if err != nil || designSystemID == 0 {
		response.FailWithMessage("Invalid design system ID", c)
		return
	}
	items, err := workagentService.NewArtifactAssetCandidateRepository(nil).ListProjectDesignSystemHistory(int(uid), uint(projectID), uint(designSystemID))
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"items": items,
		"count": len(items),
	}, c)
}

func (api *AIChatApiNew) ForkProjectDesignSystem(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}
	designSystemID, err := strconv.ParseUint(c.Param("designSystemId"), 10, 32)
	if err != nil || designSystemID == 0 {
		response.FailWithMessage("Invalid design system ID", c)
		return
	}
	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request body", c)
		return
	}
	row, err := workagentService.NewArtifactAssetCandidateRepository(nil).ForkProjectDesignSystem(int(uid), uint(projectID), uint(designSystemID), req.Name, req.Slug)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"project_id":              row.ProjectID,
		"source_design_system_id": uint(designSystemID),
		"design_system_id":        row.Id,
		"candidate_id":            row.CandidateID,
		"basename":                row.Basename,
		"title":                   row.Title,
		"derived_from":            row.DerivedFrom,
		"version":                 row.Version,
		"status":                  row.Status,
	}, c)
}

func (api *AIChatApiNew) ForkOfficialDesignSystem(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}
	var req struct {
		Basename string `json:"basename"`
		Name     string `json:"name"`
		Slug     string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request body", c)
		return
	}
	row, err := workagentService.NewArtifactAssetCandidateRepository(nil).ForkOfficialDesignSystem(int(uid), uint(projectID), req.Basename, req.Name, req.Slug)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"project_id":              row.ProjectID,
		"source_design_system_id": req.Basename,
		"design_system_id":        row.Id,
		"candidate_id":            row.CandidateID,
		"basename":                row.Basename,
		"title":                   row.Title,
		"derived_from":            row.DerivedFrom,
		"version":                 row.Version,
		"status":                  row.Status,
	}, c)
}

// buildSkillCatalogItem composes one wire-row from a live
// SkillBundle. Errors collapse to a minimal item with version
// "unknown" + zero booleans — the FE still gets the agentMode
// so the picker entry doesn't vanish on a transient lookup
// failure, but the structural flags read as "we don't know".
//
// Hoisted into its own function so a future per-skill enrichment
// (paid tier flag, beta gate, etc.) lands in one place rather
// than scattering across the handler body.
func buildSkillCatalogItem(registry *skills.Registry, mode string) skillCatalogItem {
	item := skillCatalogItem{
		AgentMode:      mode,
		Version:        "unknown",
		Source:         "official",
		Status:         "unavailable",
		Permissions:    nil,
		LabelKey:       deriveLabelKey(mode),
		DescriptionKey: deriveDescriptionKey(mode),
	}
	if descriptor, ok := agentv1.LookupOfficialSkill(mode); ok {
		item.Name = descriptor.DisplayName
		item.Description = descriptor.Description
	}
	bundle, err := registry.Build(mode, skills.BuildContext{})
	if err != nil || bundle == nil {
		return item
	}
	if bundle.Version != "" {
		item.Version = bundle.Version
	}
	item.Status = "published"
	item.Permissions = []string{"use"}
	if status := skillCatalogPublicationStatus(mode); status != "" && status != "published" {
		item.Status = status
		item.Permissions = nil
		return item
	}
	if gate := skillCatalogAccessGate(mode); gate.Gated {
		item.Source = gate.Source
		item.Permissions = []string{"request_access"}
		item.RequiredTier = gate.RequiredTier
	}
	if bundle.QuestionForm != nil && bundle.QuestionForm.Enabled {
		item.HasQuestionForm = true
	}
	if bundle.DirectionsFallback != nil && bundle.DirectionsFallback.Enabled {
		item.HasDirectionsFallback = true
	}
	if len(bundle.PostScripts) > 0 {
		item.HasPostScripts = true
	}
	item.RequiredInputs = bundle.RequiredInputs
	item.RiskHints = deriveSkillRiskHints(bundle)
	item.Artifacts = bundle.Artifacts
	return item
}

func skillCatalogPublicationStatus(mode string) string {
	raw := strings.TrimSpace(os.Getenv("WORKMAX_WORKAGENT_SKILL_STATUSES"))
	if raw == "" || strings.TrimSpace(mode) == "" {
		return "published"
	}
	for _, part := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		name, status, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(name) != mode {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "published":
			return "published"
		case "unpublished", "disabled", "unavailable":
			return "unpublished"
		}
	}
	return "published"
}

func normalizeSkillCatalogPublicationStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "published":
		return "published"
	case "unpublished", "disabled", "unavailable":
		return "unpublished"
	default:
		return ""
	}
}

func isSkillGrayCohortMember(uid int, mode string, projectID uint) bool {
	mode = strings.TrimSpace(mode)
	if uid <= 0 || mode == "" {
		return true
	}
	if configured, allowed := graySkillIntListContains("WORKMAX_WORKAGENT_GRAY_SKILL_USERS", mode, uid); configured && !allowed {
		return false
	}
	if configured, allowed := graySkillUintListContains("WORKMAX_WORKAGENT_GRAY_SKILL_PROJECTS", mode, projectID); configured && !allowed {
		return false
	}
	if configured, allowed := graySkillTeamListContains(mode, uid); configured && !allowed {
		return false
	}
	if configured, allowed := graySkillPercentageAllows(mode, uid); configured && !allowed {
		return false
	}
	return true
}

func graySkillIntListContains(envName string, mode string, target int) (bool, bool) {
	if target <= 0 {
		return graySkillListConfigured(envName, mode), false
	}
	configured, allowed := graySkillListContains(envName, mode, func(value string) bool {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return err == nil && parsed == target
	})
	return configured, allowed
}

func graySkillUintListContains(envName string, mode string, target uint) (bool, bool) {
	if target == 0 {
		return graySkillListConfigured(envName, mode), false
	}
	configured, allowed := graySkillListContains(envName, mode, func(value string) bool {
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
		return err == nil && uint(parsed) == target
	})
	return configured, allowed
}

func graySkillTeamListContains(mode string, uid int) (bool, bool) {
	if uid <= 0 {
		return graySkillListConfigured("WORKMAX_WORKAGENT_GRAY_SKILL_TEAMS", mode), false
	}
	configured, allowed := graySkillListContains("WORKMAX_WORKAGENT_GRAY_SKILL_TEAMS", mode, func(value string) bool {
		teamID, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil || teamID == 0 {
			return false
		}
		return isActiveTeamMember(uid, teamID)
	})
	return configured, allowed
}

func isActiveTeamMember(uid int, teamID uint64) bool {
	db := globals.GraDBs["system"]
	if db == nil || uid <= 0 || teamID == 0 {
		return false
	}
	var count int64
	if err := db.Model(&model.TeamMember{}).
		Where("team_id = ? AND uid = ? AND status = ? AND deleted_at IS NULL", teamID, uid, model.TeamMemberStatusActive).
		Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func graySkillListConfigured(envName string, mode string) bool {
	configured, _ := graySkillListContains(envName, mode, func(string) bool {
		return false
	})
	return configured
}

func graySkillListContains(envName string, mode string, match func(string) bool) (bool, bool) {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" || mode == "" {
		return false, true
	}
	for _, part := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		name, values, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(name) != mode {
			continue
		}
		for _, value := range strings.FieldsFunc(values, func(r rune) bool {
			return r == '|' || r == ';' || r == ' '
		}) {
			if match(value) {
				return true, true
			}
		}
		return true, false
	}
	return false, true
}

func graySkillPercentageAllows(mode string, uid int) (bool, bool) {
	raw := strings.TrimSpace(os.Getenv("WORKMAX_WORKAGENT_GRAY_SKILL_PERCENT"))
	if raw == "" || mode == "" || uid <= 0 {
		return false, true
	}
	for _, part := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		name, percentValue, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(name) != mode {
			continue
		}
		percent, err := strconv.Atoi(strings.TrimSpace(percentValue))
		if err != nil {
			return true, false
		}
		return true, graySkillPercentageValueAllows(mode, uid, percent)
	}
	return false, true
}

func graySkillPercentageValueAllows(mode string, uid int, percent int) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	bucket := int(crc32.ChecksumIEEE([]byte(mode+":"+strconv.Itoa(uid))) % 100)
	return bucket < percent
}

type skillAccessGateConfig struct {
	Gated        bool
	Source       string
	RequiredTier string
}

func skillCatalogAccessGate(mode string) skillAccessGateConfig {
	raw := strings.TrimSpace(os.Getenv("WORKMAX_WORKAGENT_GATED_SKILLS"))
	if raw == "" || strings.TrimSpace(mode) == "" {
		return skillAccessGateConfig{Source: "official"}
	}
	for _, part := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		name := entry
		source := "official"
		requiredTier := ""
		if before, after, ok := strings.Cut(entry, ":"); ok {
			name = strings.TrimSpace(before)
			parts := strings.Split(after, ":")
			if clean := strings.TrimSpace(parts[0]); clean != "" {
				source = clean
			}
			if len(parts) > 1 {
				requiredTier = strings.TrimSpace(parts[1])
			}
		}
		if source == "paid" && requiredTier == "" {
			requiredTier = "pro"
		}
		if name == mode {
			return skillAccessGateConfig{Gated: true, Source: source, RequiredTier: requiredTier}
		}
	}
	return skillAccessGateConfig{Source: "official"}
}

func deriveSkillRiskHints(bundle *skills.SkillBundle) []string {
	if bundle == nil {
		return nil
	}
	hints := map[string]bool{}
	if len(bundle.PostScripts) > 0 {
		hints["post_generation_validation"] = true
	}
	for _, input := range bundle.RequiredInputs {
		switch input.Kind {
		case "brand_assets", "product_reference_image", "garment_reference_image", "character_spec", "supporting_materials":
			hints["reference_asset_sensitive"] = true
		}
	}
	if bundle.Artifacts != nil {
		for _, outputType := range bundle.Artifacts.OutputTypes {
			addArtifactRiskHint(hints, outputType)
		}
		for _, exportTarget := range bundle.Artifacts.ExportTargets {
			addArtifactRiskHint(hints, exportTarget)
		}
		for _, anchor := range bundle.Artifacts.CritiqueAnchors {
			switch anchor {
			case "compliance":
				hints["compliance_review"] = true
			case "fidelity":
				hints["source_fidelity_review"] = true
			}
		}
	}
	out := make([]string, 0, len(hints))
	for hint := range hints {
		out = append(out, hint)
	}
	sort.Strings(out)
	return out
}

func addArtifactRiskHint(hints map[string]bool, target string) {
	switch target {
	case "html":
		hints["html_static_validation"] = true
	case "deck", "pptx":
		hints["deck_export_review"] = true
	case "pdf":
		hints["document_export_review"] = true
	case "mp4", "gif":
		hints["motion_export_review"] = true
	case "png", "jpg", "svg":
		hints["visual_fidelity_review"] = true
	}
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
