package tools

// Canvas Version/Export subdomain — §10.1 B1 split from canvas_api.go.
// Houses the four CanvasVersion handlers (Create/List/Get/Update) and the
// reserved PPTX export endpoint. Shared helpers (respondCanvasError*, respondCanvasUnauthorized,
// canvasProjectExistsForUser) remain in canvas_api.go; element counting has
// moved to canvasService.ElementCountFromDocument (§13 M1-W1-01).

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type createCanvasVersionRequest struct {
	Document     model.JSONMap `json:"document" binding:"required"`
	Message      string        `json:"message"`
	Source       string        `json:"source"`
	ThumbnailURL string        `json:"thumbnailUrl"`
}

const maxCanvasVersionBodyBytes = 8 << 20 // 8 MiB

func (a *CanvasApi) CreateVersion(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	uidInt := int(uid)

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasVersionBodyBytes)
	var req createCanvasVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	migratedDoc, schemaErr := canvasService.NormalizeCanvasDocumentForStorage(req.Document)
	if schemaErr != nil {
		if respondCanvasSchemaErrorIfApplicable(c, schemaErr) {
			return
		}
		respondCanvasError(c, "Schema validation failed: "+schemaErr.Error())
		return
	}

	created, err := canvasService.CreateCanvasVersion(
		c.Request.Context(),
		globals.GraDBs["system"],
		uidInt,
		projectID64,
		canvasService.CreateVersionInput{
			Document:     migratedDoc,
			Message:      req.Message,
			Source:       req.Source,
			ThumbnailURL: req.ThumbnailURL,
		},
	)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Create version failed")
		return
	}

	response.OkWithData(created, c)
}

func (a *CanvasApi) ListVersions(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	uidInt := int(uid)

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	includeTotalRaw := strings.TrimSpace(c.DefaultQuery("includeTotal", "1"))
	includeTotal := includeTotalRaw == "1" || strings.EqualFold(includeTotalRaw, "true")

	result, err := canvasService.ListCanvasVersions(
		c.Request.Context(),
		globals.GraDBs["system"],
		uidInt,
		projectID64,
		canvasService.ListVersionsInput{
			Page:         page,
			Limit:        limit,
			IncludeTotal: includeTotal,
		},
	)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Get versions failed")
		return
	}

	response.OkWithData(result, c)
}

func (a *CanvasApi) GetVersion(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	uidInt := int(uid)

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	versionNum, err := strconv.Atoi(c.Param("version"))
	if err != nil || versionNum <= 0 {
		respondCanvasError(c, "Invalid version")
		return
	}

	v, err := canvasService.GetCanvasVersion(
		c.Request.Context(),
		globals.GraDBs["system"],
		uidInt,
		projectID64,
		versionNum,
	)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Get version failed")
		return
	}

	migratedDoc, schemaErr := canvasService.NormalizeCanvasDocumentForStorage(v.Document)
	if schemaErr != nil {
		if respondCanvasSchemaErrorIfApplicable(c, schemaErr) {
			return
		}
		respondCanvasError(c, "Schema validation failed: "+schemaErr.Error())
		return
	}
	v.Document = migratedDoc

	response.OkWithData(v, c)
}

type updateCanvasVersionRequest struct {
	Document                 model.JSONMap `json:"document" binding:"required"`
	Message                  *string       `json:"message,omitempty"`
	Source                   *string       `json:"source,omitempty"`
	ThumbnailURL             *string       `json:"thumbnailUrl,omitempty"`
	ExpectedProjectUpdatedAt *string       `json:"expectedProjectUpdatedAt,omitempty"`
}

func (a *CanvasApi) UpdateVersion(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	uidInt := int(uid)

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	versionNum, err := strconv.Atoi(c.Param("version"))
	if err != nil || versionNum <= 0 {
		respondCanvasError(c, "Invalid version")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasVersionBodyBytes)
	var req updateCanvasVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	migratedDoc, schemaErr := canvasService.NormalizeCanvasDocumentForStorage(req.Document)
	if schemaErr != nil {
		if respondCanvasSchemaErrorIfApplicable(c, schemaErr) {
			return
		}
		respondCanvasError(c, "Schema validation failed: "+schemaErr.Error())
		return
	}
	var expectedProjectUpdatedAt *time.Time
	if req.ExpectedProjectUpdatedAt != nil && strings.TrimSpace(*req.ExpectedProjectUpdatedAt) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*req.ExpectedProjectUpdatedAt))
		if err != nil {
			respondCanvasErrorWithCode(c, "Invalid expectedProjectUpdatedAt", "INVALID_PROJECT_REVISION")
			return
		}
		expectedProjectUpdatedAt = &parsed
	}

	updated, err := canvasService.UpdateCanvasVersion(
		c.Request.Context(),
		globals.GraDBs["system"],
		uidInt,
		projectID64,
		versionNum,
		canvasService.UpdateVersionInput{
			Document:                 migratedDoc,
			Message:                  req.Message,
			Source:                   req.Source,
			ThumbnailURL:             req.ThumbnailURL,
			ExpectedProjectUpdatedAt: expectedProjectUpdatedAt,
		},
	)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Update version failed")
		return
	}

	response.OkWithData(updated, c)
}
