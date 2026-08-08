package tools

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type CanvasApi struct{}

// CanvasApi split across several files in this package:
//   - canvas_responses.go         respondCanvas*/deriveCanvasErrorCode/canvasProjectExistsForUser
//   - canvas_quote_api.go         Quote and helpers
//   - canvas_version_export_api.go  CanvasVersion CRUD
//   - canvas_generation_api.go    error codes + generation flow
//   - canvas_api.go (this file)   Project / Elements / Shots / Assets handlers

type createCanvasProjectRequest struct {
	Title             string        `json:"title"`
	Visibility        *int8         `json:"visibility"`
	ThumbnailURL      string        `json:"thumbnailUrl"`
	Document          model.JSONMap `json:"document"`
	SourceProjectUUID string        `json:"sourceProjectUuid"`
}

type sharedCanvasProjectDTO struct {
	UUID          string        `json:"uuid"`
	CreatedAt     time.Time     `json:"createdAt"`
	UpdatedAt     time.Time     `json:"updatedAt"`
	Title         string        `json:"title"`
	Visibility    int8          `json:"visibility"`
	ThumbnailURL  string        `json:"thumbnailUrl"`
	LatestVersion int           `json:"latestVersion"`
	Document      model.JSONMap `json:"document,omitempty"`
	ReadOnly      bool          `json:"readOnly"`
	Shared        bool          `json:"shared"`
}

func toSharedCanvasProjectDTO(project model.CanvasProject) sharedCanvasProjectDTO {
	return sharedCanvasProjectDTO{
		UUID:          project.UUID,
		CreatedAt:     project.CreatedAt,
		UpdatedAt:     project.UpdatedAt,
		Title:         project.Title,
		Visibility:    project.Visibility,
		ThumbnailURL:  project.ThumbnailURL,
		LatestVersion: project.LatestVersion,
		Document:      project.Document,
		ReadOnly:      true,
		Shared:        true,
	}
}

func (a *CanvasApi) CreateProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasProjectCreateBodyBytes)

	var req createCanvasProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	project, err := canvasService.CreateProject(c.Request.Context(), globals.GraDBs["system"], int(uid), canvasService.CreateProjectInput{
		Title:             req.Title,
		Visibility:        req.Visibility,
		ThumbnailURL:      req.ThumbnailURL,
		Document:          req.Document,
		SourceProjectUUID: req.SourceProjectUUID,
	})
	if err != nil {
		respondCanvasErrorFromError(c, err, "Create project failed")
		return
	}

	response.OkWithData(project, c)
}

func (a *CanvasApi) ListProjects(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	includeThumbnailRaw := strings.TrimSpace(c.DefaultQuery("includeThumbnail", "0"))
	includeThumbnail := includeThumbnailRaw == "1" || strings.EqualFold(includeThumbnailRaw, "true")
	includeTotalRaw := strings.TrimSpace(c.DefaultQuery("includeTotal", "1"))
	includeTotal := includeTotalRaw == "" || includeTotalRaw == "1" || strings.EqualFold(includeTotalRaw, "true")

	result, err := canvasService.ListProjects(c.Request.Context(), globals.GraDBs["system"], int(uid), canvasService.ListProjectsInput{
		Page:             page,
		Limit:            limit,
		IncludeThumbnail: includeThumbnail,
		SkipTotal:        !includeTotal,
	})
	if err != nil {
		respondCanvasError(c, "Get projects failed: "+err.Error())
		return
	}

	response.OkWithData(result, c)
}

func (a *CanvasApi) GetProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	id64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	project, err := canvasService.GetProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(id64))
	if err != nil {
		respondCanvasErrorFromError(c, err, "Get project failed")
		return
	}

	response.OkWithData(project, c)
}

func (a *CanvasApi) GetProjectByUUID(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	uuid := strings.TrimSpace(c.Param("uuid"))
	if uuid == "" {
		respondCanvasError(c, "Invalid project uuid")
		return
	}

	project, err := canvasService.GetProjectByUUID(c.Request.Context(), globals.GraDBs["system"], int(uid), uuid)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Get project failed")
		return
	}

	response.OkWithData(project, c)
}

func (a *CanvasApi) GetSharedProjectByUUID(c *gin.Context) {
	uuid := strings.TrimSpace(c.Param("uuid"))
	if uuid == "" {
		respondCanvasError(c, "Invalid project uuid")
		return
	}

	// C-11: optional ?t=<share_token>. Legacy URLs (no token) keep
	// working until the owner rotates; post-rotation, requests without
	// the matching token surface as ProjectNotFound. The service-level
	// matcher gives the same not-found sentinel for missing-uuid /
	// wrong-token / private-project — deliberately no signal-leak on
	// the public surface.
	token := strings.TrimSpace(c.Query("t"))

	project, err := canvasService.GetSharedProjectByUUIDWithToken(c.Request.Context(), globals.GraDBs["system"], uuid, token)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Get project failed")
		return
	}

	response.OkWithData(toSharedCanvasProjectDTO(project), c)
}

type updateCanvasProjectRequest struct {
	Title        *string `json:"title"`
	Visibility   *int8   `json:"visibility"`
	ThumbnailURL *string `json:"thumbnailUrl"`
}

func (a *CanvasApi) UpdateProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	id64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasProjectBodyBytes)
	var req updateCanvasProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	project, err := canvasService.UpdateProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(id64), canvasService.UpdateProjectInput{
		Title:        req.Title,
		Visibility:   req.Visibility,
		ThumbnailURL: req.ThumbnailURL,
	})
	if err != nil {
		respondCanvasErrorFromError(c, err, "Update project failed")
		return
	}

	response.OkWithData(project, c)
}

func (a *CanvasApi) PublishProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	id64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	project, err := canvasService.PublishProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(id64))
	if err != nil {
		respondCanvasErrorFromError(c, err, "Publish project failed")
		return
	}

	response.OkWithData(project, c)
}

const (
	maxCanvasProjectCreateBodyBytes = 8 << 20   // 8 MiB, allows creating a project directly from a template document.
	maxCanvasProjectBodyBytes       = 64 << 10  // 64 KiB
	maxCanvasAssetCreateBodyBytes   = 256 << 10 // 256 KiB
)

func (a *CanvasApi) DeleteProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	id64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}
	if err := canvasService.DeleteProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(id64)); err != nil {
		respondCanvasErrorFromError(c, err, "Delete project failed")
		return
	}

	response.Ok(c)
}

type createCanvasAssetRequest struct {
	Kind      string        `json:"kind"`
	MimeType  string        `json:"mimeType"`
	SizeBytes int64         `json:"sizeBytes"`
	Width     int           `json:"width"`
	Height    int           `json:"height"`
	URL       string        `json:"url" binding:"required"`
	ThumbURL  string        `json:"thumbUrl"`
	Metadata  model.JSONMap `json:"metadata"`
}

func (a *CanvasApi) CreateAsset(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	var req createCanvasAssetRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasAssetCreateBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	asset, err := canvasService.CreateAsset(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(projectID64), canvasService.CreateAssetInput{
		Kind:      req.Kind,
		MimeType:  req.MimeType,
		SizeBytes: req.SizeBytes,
		Width:     req.Width,
		Height:    req.Height,
		URL:       req.URL,
		ThumbURL:  req.ThumbURL,
		Metadata:  req.Metadata,
	})
	if err != nil {
		respondCanvasErrorFromError(c, err, "Create asset failed")
		return
	}

	response.OkWithData(asset, c)
}

// canvasUploadConcurrencyPerUID caps how many UploadAsset calls one
// user can have in flight simultaneously. The handler reads the entire
// file into memory before opening the DB transaction (canvas_api.go
// below: io.ReadAll), so without this cap a scripted client could
// drive N parallel reads each holding ~maxSize bytes resident — an
// easy OOM vector. Four is the Server-owned contract Desktop uploaders
// must respect; the gate also protects against scripted abuse.
const canvasUploadConcurrencyPerUID = 4

// canvasUploadSemaphores maps uid → buffered channel acting as a
// counting semaphore. Lazy-initialized via sync.Map.LoadOrStore so we
// don't hold a global lock on every upload, and entries are never
// removed (the bounded set of active users keeps memory growth
// negligible vs. one chan-of-4 per uid).
var canvasUploadSemaphores sync.Map

func acquireCanvasUploadSlot(uid int) (release func(), ok bool) {
	val, _ := canvasUploadSemaphores.LoadOrStore(uid, make(chan struct{}, canvasUploadConcurrencyPerUID))
	sem := val.(chan struct{})
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	default:
		return nil, false
	}
}

// UploadAsset handles multipart form uploads for project assets. The
// bulk of the work — MIME sniffing, filesystem layout, AssetLedger sync
// — now lives in canvasService.UploadAsset; this handler just reads
// the multipart form and classifies service errors into HTTP responses.
func (a *CanvasApi) UploadAsset(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	// Per-uid concurrency cap. Acquired BEFORE c.FormFile / io.ReadAll
	// so the heavy memory allocation is gated. Returns 429 fast if the
	// user is already at the cap — better than blocking an HTTP worker
	// holding 16 MiB+ buffer for a slot that may never free.
	releaseSlot, ok := acquireCanvasUploadSlot(int(uid))
	if !ok {
		respondCanvasErrorWithCode(c, "Too many concurrent uploads", "UPLOAD_RATE_LIMITED")
		return
	}
	defer releaseSlot()

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	maxRequestSize := getCanvasAssetUploadBodyLimit()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestSize+(1<<20))

	file, err := c.FormFile("file")
	if err != nil {
		respondCanvasErrorWithCode(c, "Failed to get uploaded file", "UPLOAD_FILE_MISSING")
		return
	}

	if file.Size > maxRequestSize {
		respondCanvasErrorWithCode(c, "File too large", "FILE_TOO_LARGE")
		return
	}

	fileReader, err := file.Open()
	if err != nil {
		respondCanvasErrorWithCode(c, "Failed to open uploaded file", "UPLOAD_FILE_OPEN_FAILED")
		return
	}
	defer fileReader.Close()

	fileBytes, err := io.ReadAll(io.LimitReader(fileReader, maxRequestSize+1))
	if err != nil {
		respondCanvasErrorWithCode(c, "Failed to read uploaded file", "UPLOAD_FILE_READ_FAILED")
		return
	}
	if int64(len(fileBytes)) > maxRequestSize {
		respondCanvasErrorWithCode(c, "File too large", "FILE_TOO_LARGE")
		return
	}

	contentType, _, ok := canvasService.ClassifyUploadedContent(file.Header.Get("Content-Type"), fileBytes)
	if !ok {
		respondCanvasErrorWithCode(c, "Unsupported file type", "UNSUPPORTED_FILE_TYPE")
		return
	}
	maxSize := getCanvasAssetUploadSizeForContentType(contentType)
	if int64(len(fileBytes)) > maxSize {
		respondCanvasErrorWithCode(c, "File too large", "FILE_TOO_LARGE")
		return
	}

	width, _ := strconv.Atoi(c.Request.FormValue("width"))
	height, _ := strconv.Atoi(c.Request.FormValue("height"))

	asset, err := canvasService.UploadAsset(
		c.Request.Context(),
		globals.GraDBs["system"],
		canvasService.NewLocalAssetStorage(),
		int(uid),
		uint(projectID64),
		canvasService.UploadAssetInput{
			Kind:              c.Request.FormValue("kind"),
			Width:             width,
			Height:            height,
			OriginalName:      file.Filename,
			HeaderContentType: file.Header.Get("Content-Type"),
			FileBytes:         fileBytes,
			MaxSize:           maxSize,
		},
	)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Create asset record failed")
		return
	}

	response.OkWithData(asset, c)
}

func (a *CanvasApi) ListAssets(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	noCount := c.Query("noCount") == "1" || strings.EqualFold(c.Query("noCount"), "true")
	search := strings.TrimSpace(c.Query("q"))
	kind := strings.TrimSpace(c.Query("kind"))

	result, err := canvasService.ListProjectFileAssets(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(projectID64), canvasService.ListAssetsInput{
		Page:    page,
		Limit:   limit,
		NoCount: noCount,
		Search:  search,
		Kind:    kind,
	})
	if err != nil {
		respondCanvasErrorFromError(c, err, "Get assets failed")
		return
	}

	response.OkWithData(result, c)
}

// elementCountFromDocument + applyElementPatches moved to
// server/service/tools/canvas/canvas_project_service.go as
// ElementCountFromDocument / ApplyElementPatches (§13 M1-W1-01).

// ============================================================================
// Canvas AI Operations — moved to canvas_generation_api.go (M2 B1 split)
// ============================================================================
// The img2img / outpaint / inpaint / mockup / edit-text / split-layers /
// upscale / remove-bg handlers, their request types, and the helper
// layer (normalize*, authorize*, buildCanvasReferenceImages,
// setCanvasOperationMeta, parseCanvas*Header, respondCanvasAIError,
// submitCanvasGenerationTask) now live in canvas_generation_api.go.
// This banner is kept as a pointer; the symbols are still in the same
// `tools` package and resolve transparently.
