package workagent

// agent_discovery_api.go owns the M1 question-form submission
// endpoint. Companion to agent_direction_api.go (DS-2 picker submit)
// — same shape, same IDOR posture. The flow:
//
//   1. Server emits a question_form block (QuestionFormDispatcher)
//   2. Frontend renders QuestionFormBlockRenderer with the questions
//   3. User submits → onSubmit → POST here with answers + form_id
//   4. We persist a synthetic chat_message row carrying
//      discovery_form_result metadata
//   5. The next agent turn's preflight reads that row and bakes
//      <discovery-context> into SystemAdditions
//
// The NLP pre-scan path (agent_question_form.go's
// PersistInferredDiscoveryAnswers) writes the SAME row shape via the
// shared PersistDiscoveryAnswers helper, so the preflight reader
// doesn't have to branch on origin.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	workagentService "server/service/tools/workagent"
	workagentSkills "server/service/tools/workagent/skills"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// discoverySubmissionRequest mirrors the QuestionFormBlockRenderer's
// onSubmit callback signature. answers is a flat string→string map
// (single_select today; future multi_select would JSON-encode the
// selected values into a single string slot to keep the wire shape
// stable). skip_reason is one of the legal categories enforced by
// PersistDiscoveryAnswers — empty defaults to "user_submitted".
type discoverySubmissionRequest struct {
	FormID     string            `json:"form_id"`
	Answers    map[string]string `json:"answers" binding:"required"`
	SkipReason string            `json:"skip_reason"`
}

var allowedDiscoverySkipReasons = map[string]struct{}{
	"user_submitted":   {},
	"user_skipped":     {},
	"nlp_inferred":     {},
	"context_inferred": {},
}

// SubmitDiscoveryAnswers handles POST /api/work-agent/threads/:id/discovery.
//
// Returns:
//   - 200 OK + {"data":{"persisted":true,"answers":N}} on success
//   - 400 on missing/empty answers map (binding failure)
//   - 404 when the thread doesn't belong to the caller (IDOR defence)
//   - 500 on persist failure (rare — repo path is in-process)
//
// The persistence call is fail-soft inside PersistDiscoveryAnswers
// (logs + skips on bad inputs). This handler upgrades that boolean
// to a concrete 500 so the frontend doesn't accept stale state.
func (api *AIChatApiNew) SubmitDiscoveryAnswers(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	threadIDU64, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || threadIDU64 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thread id"})
		return
	}
	threadID := uint(threadIDU64)

	var req discoverySubmissionRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: answers required"})
		return
	}
	if len(req.Answers) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "answers cannot be empty"})
		return
	}
	formID, validationErr := validateDiscoverySubmission(req)
	if validationErr != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": validationErr})
		return
	}

	// IDOR guard: verify thread ownership in a single uid-scoped
	// SELECT so a cross-tenant probe collapses to the same 404 as a
	// truly missing thread.
	threadRepo := workagentService.DefaultThreadRepository()
	if _, err := threadRepo.LoadByIDForOwner(threadID, uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Thread not found"})
			return
		}
		globals.Error(fmt.Sprintf("[Discovery API] thread lookup failed: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify thread ownership"})
		return
	}

	if ok := workagentService.PersistDiscoveryAnswers(uid, threadID, formID, req.Answers, req.SkipReason); !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Persist failed; please retry"})
		return
	}
	workagentService.PersistPassMode(uid, threadID, workagentService.WorkAgentPassModeDraft, "question_form")

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"persisted": true,
		"answers":   len(req.Answers),
		"pass_mode": string(workagentService.WorkAgentPassModeDraft),
	}})
}

func validateDiscoverySubmission(req discoverySubmissionRequest) (string, string) {
	formID := strings.TrimSpace(req.FormID)
	if formID == "" {
		return "", "form_id is required"
	}
	if _, ok := allowedAgentModes[formID]; !ok {
		return "", "unknown form_id"
	}
	if req.SkipReason != "" {
		if _, ok := allowedDiscoverySkipReasons[req.SkipReason]; !ok {
			return "", "invalid skip_reason"
		}
	}

	bundle, err := workagentService.GetSkillRegistry().Build(formID, workagentSkills.BuildContext{})
	if err != nil || bundle == nil || bundle.QuestionForm == nil || !bundle.QuestionForm.Enabled {
		return "", "form_id does not accept discovery answers"
	}

	questions := bundle.QuestionForm.Questions
	byID := make(map[string]struct {
		typ      string
		required bool
		options  map[string]struct{}
	}, len(questions))
	for _, q := range questions {
		if q.ID == "" {
			continue
		}
		typ := q.Type
		if typ == "" {
			typ = "single_select"
		}
		options := map[string]struct{}{}
		for _, opt := range q.Options {
			if opt.Value != "" {
				options[opt.Value] = struct{}{}
			}
		}
		byID[q.ID] = struct {
			typ      string
			required bool
			options  map[string]struct{}
		}{typ: typ, required: q.Required, options: options}
	}
	for answerID, value := range req.Answers {
		q, ok := byID[answerID]
		if !ok {
			return "", fmt.Sprintf("unknown answer field %q for form_id %q", answerID, formID)
		}
		if strings.TrimSpace(value) == "" {
			return "", fmt.Sprintf("answer %q cannot be empty", answerID)
		}
		switch q.typ {
		case "single_select":
			if _, ok := q.options[value]; !ok {
				return "", fmt.Sprintf("invalid option %q for answer %q", value, answerID)
			}
		case "multi_select":
			var selected []string
			if err := json.Unmarshal([]byte(value), &selected); err != nil || len(selected) == 0 {
				return "", fmt.Sprintf("answer %q must be a JSON array", answerID)
			}
			for _, item := range selected {
				if _, ok := q.options[item]; !ok {
					return "", fmt.Sprintf("invalid option %q for answer %q", item, answerID)
				}
			}
		case "text", "button":
		default:
			return "", fmt.Sprintf("unsupported question type %q for answer %q", q.typ, answerID)
		}
	}
	for id, q := range byID {
		if q.required && strings.TrimSpace(req.Answers[id]) == "" {
			return "", fmt.Sprintf("required answer %q is missing", id)
		}
	}
	return formID, ""
}
