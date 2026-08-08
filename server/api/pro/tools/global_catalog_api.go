package tools

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/service/globalasset"
	"server/service/globalmodel"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type GlobalCatalogApi struct{}

type globalAssetDTO struct {
	ID            uint      `json:"id"`
	UUID          string    `json:"uuid"`
	ProjectID     *uint     `json:"projectId,omitempty"`
	Kind          string    `json:"kind"`
	Source        string    `json:"source"`
	URL           string    `json:"url"`
	ThumbURL      string    `json:"thumbUrl"`
	MimeType      string    `json:"mimeType"`
	SizeBytes     int64     `json:"sizeBytes"`
	Width         int       `json:"width"`
	Height        int       `json:"height"`
	DurationMs    int       `json:"durationMs"`
	Status        int8      `json:"status"`
	Visibility    int8      `json:"visibility"`
	ParentAssetID uint      `json:"parentAssetId,omitempty"`
	VariantType   string    `json:"variantType"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func toGlobalAssetDTO(row model.GlobalAsset) globalAssetDTO {
	return globalAssetDTO{
		ID:            row.Id,
		UUID:          row.UUID,
		ProjectID:     row.ProjectID,
		Kind:          row.Kind,
		Source:        row.Source,
		URL:           row.URL,
		ThumbURL:      row.ThumbURL,
		MimeType:      row.MimeType,
		SizeBytes:     row.SizeBytes,
		Width:         row.Width,
		Height:        row.Height,
		DurationMs:    row.DurationMs,
		Status:        row.Status,
		Visibility:    row.Visibility,
		ParentAssetID: row.ParentAssetID,
		VariantType:   row.VariantType,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func (a *GlobalCatalogApi) ListModels(c *gin.Context) {
	mediaType := strings.TrimSpace(c.Query("mediaType"))
	if mediaType == "" {
		mediaType = strings.TrimSpace(c.Query("media"))
	}
	rows, err := globalmodel.NewRepository(globals.GraDBs["system"]).ListEnabled(mediaType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to list models"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list": rows,
		},
	})
}

func (a *GlobalCatalogApi) GetModel(c *gin.Context) {
	modelID := strings.TrimSpace(c.Param("modelId"))
	mediaType := strings.TrimSpace(c.Query("mediaType"))
	if mediaType == "" {
		mediaType = strings.TrimSpace(c.Query("media"))
	}
	if modelID == "" || mediaType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "modelId and mediaType are required"})
		return
	}
	row, err := globalmodel.NewRepository(globals.GraDBs["system"]).LoadEnabledByModelID(modelID, mediaType)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "model not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success", "data": row})
}

func (a *GlobalCatalogApi) ListAssets(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "unauthorized"})
		return
	}
	projectID, err := strconv.ParseUint(strings.TrimSpace(c.Query("projectId")), 10, 64)
	if err != nil || projectID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "projectId is required"})
		return
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	offset := 0
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			offset = parsed
		}
	}
	rows, err := globalasset.NewRepository(globals.GraDBs["system"]).ListForProject(int(uid), uint(projectID), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to list assets"})
		return
	}
	list := make([]globalAssetDTO, 0, len(rows))
	for _, row := range rows {
		list = append(list, toGlobalAssetDTO(row))
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list": list,
		},
	})
}

// GetAsset — GET /global/assets/:id. Returns the single asset by
// global_asset_id, scoped via the same accessQuery used by
// ListAssets (owner-direct + project-member read). Returns 404 on
// invalid id, missing row, or cross-tenant access — the four
// failure modes collapse to one body so an enumeration attacker
// can't distinguish "this id doesn't exist" from "this id exists
// but isn't yours".
//
// Use case: a FE caller that already holds a global_asset_id (from
// a list call, a cross-tool reference, or the workagent
// ContextPanel's ProjectAssetsSection) wants the unified preview
// URL + kind + dimensions without going through the source tool's
// detail endpoint.
func (a *GlobalCatalogApi) GetAsset(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "unauthorized"})
		return
	}
	assetID, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || assetID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "asset not found"})
		return
	}
	row, err := globalasset.NewRepository(globals.GraDBs["system"]).
		LoadForAccess(uint(assetID), int(uid))
	if err != nil {
		// LoadForAccess returns gorm.ErrRecordNotFound for both
		// "not found" and "cross-tenant" — caller treats them the
		// same to avoid an enumeration oracle.
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "asset not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    toGlobalAssetDTO(*row),
	})
}

// ResolveAssetBySource — GET /global/assets/by-source?kind=K&id=N.
// Resolves a tool-local row id back to its w_global_asset bridge.
// Kind is the public SourceKind enum (canvas_project_file /
// generation_object / workagent_file / reference_upload); BE-
// internal source_table strings stay private behind the resolver.
//
// Used by callers that already hold a tool-local id and want the
// global_asset_id so cross-tool composition routes through one
// identifier instead of needing the source tool's own detail
// endpoint to read the bridge column.
//
// Same 404-collapses-all-failures posture as GetAsset.
func (a *GlobalCatalogApi) ResolveAssetBySource(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "unauthorized"})
		return
	}
	kindRaw := strings.TrimSpace(c.Query("kind"))
	if kindRaw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "kind is required"})
		return
	}
	sourceID, err := strconv.ParseUint(strings.TrimSpace(c.Query("id")), 10, 64)
	if err != nil || sourceID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "id is required"})
		return
	}
	row, err := globalasset.NewRepository(globals.GraDBs["system"]).
		LoadBySourceForAccess(int(uid), globalasset.SourceKind(kindRaw), uint(sourceID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "asset not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    toGlobalAssetDTO(*row),
	})
}

func (a *GlobalCatalogApi) AuditAssets(c *gin.Context) {
	report, err := globalasset.AuditCoverage(c.Request.Context(), globals.GraDBs["system"])
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to audit assets"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    report,
	})
}
