package workagent

import (
	"fmt"
	"strings"

	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
)

type skillRuntimeAccessDecision struct {
	Allowed      bool
	Source       string
	RequiredTier string
	Code         string
	Message      string
}

func ensureSkillRuntimeAccess(uid int, agentMode string) (skillRuntimeAccessDecision, error) {
	return ensureSkillRuntimeAccessForProject(uid, agentMode, 0)
}

func ensureSkillRuntimeAccessForProject(uid int, agentMode string, projectID uint) (skillRuntimeAccessDecision, error) {
	if status := skillCatalogPublicationStatus(agentMode); status != "" && status != "published" {
		return skillRuntimeAccessDecision{
			Allowed: false,
			Source:  "official",
			Code:    "SKILL_UNAVAILABLE",
			Message: fmt.Sprintf("%s skill is %s and cannot be used.", agentMode, status),
		}, nil
	}
	gate := skillCatalogAccessGate(agentMode)
	source := strings.TrimSpace(gate.Source)
	if source == "" {
		source = "official"
	}
	requiredTier := strings.TrimSpace(gate.RequiredTier)
	if !gate.Gated {
		return skillRuntimeAccessDecision{Allowed: true, Source: source, RequiredTier: requiredTier}, nil
	}
	if source == "paid" && hasSkillRequiredTierEntitlement(uid, requiredTier) {
		return skillRuntimeAccessDecision{Allowed: true, Source: source, RequiredTier: requiredTier}, nil
	}
	if source == "gray" && !isSkillGrayCohortMember(uid, agentMode, projectID) {
		return skillRuntimeAccessDecision{
			Allowed: false,
			Source:  source,
			Code:    "SKILL_UNAVAILABLE",
			Message: fmt.Sprintf("%s skill is not available for this gray-release cohort.", agentMode),
		}, nil
	}
	requests, err := workagentService.NewSkillAccessRequestRepository(nil).ListForUser(uid)
	if err != nil {
		return skillRuntimeAccessDecision{}, err
	}
	if request, ok := requests[agentMode]; ok && request.Status == workagentModel.SkillAccessRequestStatusApproved {
		return skillRuntimeAccessDecision{Allowed: true, Source: source, RequiredTier: requiredTier}, nil
	}
	message := fmt.Sprintf("%s skill access requires approval before use.", source)
	if source == "paid" && requiredTier != "" {
		message = fmt.Sprintf("paid skill access requires %s approval before use.", requiredTier)
	}
	return skillRuntimeAccessDecision{
		Allowed:      false,
		Source:       source,
		RequiredTier: requiredTier,
		Message:      message,
	}, nil
}
