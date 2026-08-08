package workagent

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type passModeRequest struct {
	Mode                 string `json:"mode" binding:"required"`
	Source               string `json:"source"`
	SelectedVariationID  string `json:"selected_variation_id"`
	SelectedArtifactID   string `json:"selected_artifact_id"`
	SelectedFileID       string `json:"selected_file_id"`
	DesignSystemBasename string `json:"design_system_basename"`
	AssetContract        string `json:"asset_contract"`
}

// SubmitPassMode handles POST /api/work-agent/threads/:id/pass-mode.
//
// This is the generic P3 state transition endpoint used by UI gates
// that are not tied to a specific catalog, such as variations_picker.
// Direction and discovery have richer domain endpoints; this endpoint
// only persists the workflow pass marker after the usual thread
// ownership check.
func (api *AIChatApiNew) SubmitPassMode(c *gin.Context) {
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

	var req passModeRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: mode required"})
		return
	}

	threadRepo := workagentService.DefaultThreadRepository()
	if _, err := threadRepo.LoadByIDForOwner(threadID, uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Thread not found"})
			return
		}
		globals.Error(fmt.Sprintf("[PassMode API] thread lookup failed: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify thread ownership"})
		return
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "ui"
	}
	mode := workagentService.WorkAgentPassMode(strings.TrimSpace(req.Mode))
	selectedArtifactID := strings.TrimSpace(req.SelectedArtifactID)
	selectedFileID := strings.TrimSpace(req.SelectedFileID)
	if source == "variation_picker" && mode == workagentService.WorkAgentPassModeFinalize && selectedArtifactID == "" && selectedFileID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Variation finalize requires a draft artifact or file id"})
		return
	}
	if source == "variation_picker" && mode == workagentService.WorkAgentPassModeFinalize {
		if err := validateVariationFinalizeDraftSelection(uid, threadID, selectedArtifactID, selectedFileID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	state := workagentService.WorkAgentPassModeState{
		Mode:                mode,
		Source:              source,
		SelectedVariationID: strings.TrimSpace(req.SelectedVariationID),
		SelectedArtifactID:  selectedArtifactID,
		SelectedFileID:      selectedFileID,
		DesignSystem:        strings.TrimSpace(req.DesignSystemBasename),
		AssetContract:       strings.TrimSpace(req.AssetContract),
	}
	if ok := workagentService.PersistPassModeState(uid, threadID, state); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pass mode rejected"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"persisted":              true,
		"pass_mode":              strings.TrimSpace(req.Mode),
		"source":                 source,
		"selected_variation_id":  state.SelectedVariationID,
		"selected_artifact_id":   state.SelectedArtifactID,
		"selected_file_id":       state.SelectedFileID,
		"design_system_basename": state.DesignSystem,
		"asset_contract":         state.AssetContract,
	}})
}

func validateVariationFinalizeDraftSelection(uid uint, threadID uint, selectedArtifactID string, selectedFileID string) error {
	artifactID, artifactIDErr := parseOptionalDraftArtifactID(selectedArtifactID)
	fileID, fileIDErr := parseOptionalDraftFileID(selectedFileID)
	if selectedArtifactID != "" && artifactIDErr != nil {
		return fmt.Errorf("Variation finalize selected artifact is invalid")
	}
	if selectedFileID != "" && fileIDErr != nil {
		return fmt.Errorf("Variation finalize selected file is invalid")
	}
	var selectedFile *workagentModel.ThreadFile
	if artifactID != 0 {
		artifact, file, err := workagentService.NewArtifactRegistryRepository(nil).LoadForOwnerWithThreadFile(int(uid), threadID, artifactID)
		if err != nil {
			return fmt.Errorf("Variation finalize selected artifact was not found")
		}
		if !isVariationDraftArtifactStatus(artifact.Status) {
			return fmt.Errorf("Variation finalize selected artifact must still be a draft")
		}
		if file.FileSource != workagentModel.FileSourceOutput {
			return fmt.Errorf("Variation finalize selected artifact must be an output draft")
		}
		selectedFile = file
	}
	if fileID != 0 {
		file, err := loadVariationDraftThreadFile(uid, threadID, fileID)
		if err != nil {
			return err
		}
		if selectedFile != nil && selectedFile.Id != file.Id {
			return fmt.Errorf("Variation finalize selected artifact and file do not match")
		}
	}
	return nil
}

func parseOptionalDraftArtifactID(raw string) (uint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	id, err := parseArtifactID(raw)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}

func parseOptionalDraftFileID(raw string) (uint, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	value = strings.TrimPrefix(value, "thread-file-")
	value = strings.TrimPrefix(value, "file-")
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid file id")
	}
	return uint(id), nil
}

func loadVariationDraftThreadFile(uid uint, threadID uint, fileID uint) (*workagentModel.ThreadFile, error) {
	db := globals.GraDBs["system"]
	if db == nil || uid == 0 || threadID == 0 || fileID == 0 {
		return nil, fmt.Errorf("Variation finalize selected file was not found")
	}
	var file workagentModel.ThreadFile
	if err := db.Where("id = ? AND uid = ? AND thread_id = ?", fileID, uid, threadID).First(&file).Error; err != nil {
		return nil, fmt.Errorf("Variation finalize selected file was not found")
	}
	if file.FileSource != workagentModel.FileSourceOutput {
		return nil, fmt.Errorf("Variation finalize selected file must be an output draft")
	}
	var artifact workagentModel.ArtifactRegistry
	err := db.Where("uid = ? AND thread_id = ? AND thread_file_id = ?", uid, threadID, fileID).
		Order("version DESC, id DESC").
		First(&artifact).Error
	if err == nil && !isVariationDraftArtifactStatus(artifact.Status) {
		return nil, fmt.Errorf("Variation finalize selected file must still be a draft")
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("Variation finalize selected file could not be verified")
	}
	return &file, nil
}

func isVariationDraftArtifactStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case workagentModel.ArtifactStatusDraft, workagentModel.ArtifactStatusNeedsReview:
		return true
	default:
		return false
	}
}
