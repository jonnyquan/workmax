package tools

// Canvas HTTP response machinery — shared by every canvas_*_api.go in
// this package. Lifted out of canvas_api.go per §10.1 B1: the six
// helpers here are the only thing that keeps the other handler files
// tied to the monolith, and every canvas route funnels through them.
//
// Shape contract (the frontend's error classifier depends on it):
//
//   respondCanvasError / respondCanvasErrorWithCode
//     → HTTP 200 with { code: ERROR, message, data.errorCode, errorCode }
//       (legacy envelope — the frontend reads `errorCode` from either
//       the top level or `data.errorCode`, so both are emitted.)
//
//   respondCanvasChatHTTPError
//     → HTTP <status> with { code: <status>, message, data.errorCode,
//       data.success=false, data.reload?, error.{message,userMessage} }
//       (Chat / Agent paths read HTTP status directly; they cannot
//       piggy-back on response.ERROR=7 like respondCanvasError does.)
//
//   respondCanvasUnauthorized → preset ChatHTTPError with reload=true,
//     so the browser can force a re-login round-trip.
//
//   deriveCanvasErrorCode maps the handler's English error message into
//     a stable code the frontend can localize. Handlers that own their
//     own taxonomy (schema migrator, prompt guard) skip it and call
//     respondCanvasErrorWithCode directly.
//
// canvasProjectExistsForUser stays here too because it's the only
// shared DB helper the handler layer reaches for (service-layer clones
// exist in canvas_project_service / canvas_version_service; the
// duplication is intentional — handler package imports service, not
// vice versa).

import (
	"errors"
	"net/http"
	"strings"

	"server/model/common/response"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// matchCanvasSentinel maps a service-layer sentinel error to its
// stable client-facing (code, message) pair. Returns ok=false when err
// is nil or not a known canvas sentinel — callers should fall back to
// deriveCanvasErrorCode (which substring-matches the *message*) only as
// a last resort.
//
// Why this exists: deriveCanvasErrorCode was the historical mapper,
// and it works by string-prefix-matching ~50 English error messages.
// Any service-layer text tweak silently breaks the frontend's error
// taxonomy. Migrating callers to errors.Is via this helper makes the
// contract explicit — the message becomes pure documentation.
//
// New sentinels: add a case here AND mirror the (code, message) pair
// in deriveCanvasErrorCode so legacy callers passing only a message
// keep working until they migrate.
func matchCanvasSentinel(err error) (code, message string, ok bool) {
	switch {
	case err == nil:
		return "", "", false
	case errors.Is(err, canvasService.ErrCanvasProjectNotFound):
		return "PROJECT_NOT_FOUND", "Project not found", true
	case errors.Is(err, canvasService.ErrCanvasVersionNotFound):
		return "VERSION_NOT_FOUND", "Version not found", true
	case errors.Is(err, canvasService.ErrCanvasVersionReadOnly):
		return "VERSION_READ_ONLY", "Version is read-only", true
	case errors.Is(err, canvasService.ErrCanvasVersionConflict):
		return "CANVAS_VERSION_CONFLICT", "Canvas document changed in another session; reload before saving.", true
	case errors.Is(err, canvasService.ErrCanvasInvalidVersion):
		return "INVALID_VERSION", "Invalid version", true
	case errors.Is(err, canvasService.ErrCanvasProjectNoVersions):
		return "PROJECT_NO_VERSIONS", "Project has no versions", true
	case errors.Is(err, canvasService.ErrCanvasTitleRequired):
		return "TITLE_REQUIRED", "Title cannot be empty", true
	case errors.Is(err, canvasService.ErrCanvasProjectPrivate):
		return "PROJECT_NOT_PUBLISHED", "Project must be unlisted or public before publishing", true
	case errors.Is(err, canvasService.ErrCanvasAssetProjectNotFound):
		return "PROJECT_NOT_FOUND", "Project not found", true
	case errors.Is(err, canvasService.ErrCanvasAssetTooLarge):
		return "FILE_TOO_LARGE", "File too large", true
	case errors.Is(err, canvasService.ErrCanvasAssetUnsupportedType):
		return "INVALID_FILE_TYPE", "Invalid file type", true
	case errors.Is(err, canvasService.ErrCanvasAssetEmptyFile):
		// Aligns with the existing UploadAsset handler shape that the
		// frontend classifier already consumes: UPLOAD_FILE_READ_FAILED
		// is "we got the file but couldn't read its body" (covers
		// 0-byte uploads). UPLOAD_FILE_MISSING is reserved for the
		// "no multipart part at all" case.
		return "UPLOAD_FILE_READ_FAILED", "Failed to read uploaded file", true
	case errors.Is(err, canvasService.ErrCanvasInvalidVideoURL):
		return "VIDEO_URL_INVALID", "videoUrl is invalid", true
	case errors.Is(err, canvasService.ErrCanvasKeyframeExtraction):
		return "KEYFRAME_EXTRACTION_FAILED", "Failed to extract keyframe", true
	case errors.Is(err, canvasService.ErrCanvasKeyframeBusy):
		return "KEYFRAME_EXTRACTION_BUSY", "Keyframe extraction is busy. Please wait for the current extraction to finish.", true
	case errors.Is(err, canvasService.ErrCanvasAssetInvalidInput):
		return canvasAIErrorInvalidReq, "Invalid asset input", true
	case errors.Is(err, canvasService.ErrCanvasRestoreDanglingAssets):
		// Handlers that need the dangling list MUST intercept this
		// sentinel before falling into respondCanvasErrorFromError —
		// the list lives on the service result, not the error. This
		// entry exists so any handler that DOESN'T need the list (or
		// forgets to intercept) still emits a stable code instead of
		// degrading to CANVAS_OPERATION_FAILED.
		return "CANVAS_RESTORE_DANGLING_ASSETS", "Restore source references assets that no longer exist", true
	case errors.Is(err, canvasService.ErrSystemKindProjectUndeletable):
		// Stage 1.5 — system_kind > 0 projects (Personal Workspace
		// today) refuse delete from any non-account-close path. The
		// canvas DELETE endpoint maps to this code so the FE can
		// render a localized "This workspace cannot be deleted"
		// message without parsing the error string.
		return "CANVAS_PROJECT_UNDELETABLE", "This workspace is system-managed and cannot be deleted", true
	}
	return "", "", false
}

// respondCanvasErrorFromError is the recommended write-path error
// responder. It surfaces SchemaError + known sentinels with their
// stable codes; on no match it falls back to deriveCanvasErrorCode
// over a caller-provided message envelope.
//
// Use this in any new handler that calls a canvas service function —
// avoid the older `respondCanvasError(c, "X failed: "+err.Error())`
// shape, which lets internal sentinel text leak into the message
// classifier and depends on substring matches that change with every
// service-layer rephrasing.
func respondCanvasErrorFromError(c *gin.Context, err error, fallbackMessage string) {
	if respondCanvasSchemaErrorIfApplicable(c, err) {
		return
	}
	if code, message, ok := matchCanvasSentinel(err); ok {
		respondCanvasErrorWithCode(c, message, code)
		return
	}
	respondCanvasError(c, fallbackMessage+": "+err.Error())
}

// respondCanvasSchemaErrorIfApplicable writes a canvas error response
// when err is a typed canvas schema error and returns true. The error
// code is the schema-validator's own taxonomy (CANVAS_SCHEMA_INVALID),
// so the frontend can show a structural-document message instead of a
// generic "operation failed."
//
// Use this at every write-path handler that calls the canvas service
// with caller-supplied document JSON — UpdateElements, CreateProject,
// CreateCanvasVersion, UpdateCanvasVersion, etc. Without it, a
// SchemaError falls through deriveCanvasErrorCode and degrades to
// CANVAS_OPERATION_FAILED.
func respondCanvasSchemaErrorIfApplicable(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	se, ok := canvasService.IsSchemaError(err)
	if !ok {
		return false
	}
	respondCanvasErrorWithCode(c, se.Message, se.Code)
	return true
}

func canvasProjectExistsForUser(db *gorm.DB, projectID uint64, uid int) (bool, error) {
	return projectService.NewRepository(db).CanViewProject(uint(projectID), uint(uid))
}

func respondCanvasChatHTTPError(c *gin.Context, status int, message string, errorCode string, reload bool) {
	data := gin.H{
		"success": false,
	}
	if reload {
		data["reload"] = true
	}
	if strings.TrimSpace(errorCode) != "" {
		data["errorCode"] = errorCode
	}

	body := gin.H{
		"code":    status,
		"message": message,
		"data":    data,
		"error": gin.H{
			"message":     message,
			"userMessage": message,
		},
	}
	if strings.TrimSpace(errorCode) != "" {
		body["errorCode"] = errorCode
	}
	c.JSON(status, body)
}

func respondCanvasUnauthorized(c *gin.Context) {
	respondCanvasChatHTTPError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized, true)
}

// deriveCanvasErrorCode is the LEGACY fallback classifier — kept for
// the few remaining call sites that pass a verbose string (e.g.
// "Get tasks failed: <gorm err>") via `respondCanvasError`. New
// handlers should use one of the explicit-code paths instead:
//
//   - sentinel error → respondCanvasErrorFromError (preferred)
//   - explicit code → respondCanvasErrorWithCode("msg", "CODE")
//
// Migration progress (P1-1 B1/B2/B3):
//   - canvas_api.go     ✓ project + asset + upload + extract-keyframe paths migrated
//   - canvas_version_export_api.go  ✓ all version handlers migrated
//   - canvas_chat_agent_api.go      ✓ optimize-prompt finalize path migrated
//   - canvas_generation_api.go      n/a — uses respondCanvasAIError (status+code explicit)
//   - canvas_shot_lock_api.go       partial — fallback prefix path remains
//
// The substring branches below are the SAFETY NET for any caller still
// going through `respondCanvasError(c, "X failed: ...")`. They are
// pinned by canvas_responses_test.go / canvas_responses_more_test.go
// so a refactor that "alphabetizes" the switch can't silently reclassify.
//
// Removal plan: once `grep "respondCanvasError(c," server/api/pro/tools/`
// shows zero verbose-message callers, the substring switch can collapse
// to the four exact-match cases (Unauthorized / Invalid project id /
// Invalid project uuid / Invalid version) plus the "invalid request"
// prefix and the default bucket. Until then, the branches below stay.
func deriveCanvasErrorCode(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case normalized == "unauthorized":
		return canvasAIErrorUnauthorized
	case strings.HasPrefix(normalized, "invalid request"):
		return canvasAIErrorInvalidReq
	case normalized == "invalid project id":
		return "INVALID_PROJECT_ID"
	case normalized == "invalid project uuid":
		return "INVALID_PROJECT_UUID"
	case normalized == "invalid version":
		return "INVALID_VERSION"
	case normalized == "project not found":
		return "PROJECT_NOT_FOUND"
	case normalized == "version not found":
		return "VERSION_NOT_FOUND"
	case normalized == "version is read-only":
		return "VERSION_READ_ONLY"
	case strings.Contains(normalized, "file too large"):
		return "FILE_TOO_LARGE"
	case strings.Contains(normalized, "invalid file type"):
		return "INVALID_FILE_TYPE"
	case strings.Contains(normalized, "failed to get uploaded file"):
		return "UPLOAD_FILE_MISSING"
	case strings.Contains(normalized, "upload asset failed"):
		return "UPLOAD_ASSET_FAILED"
	case strings.Contains(normalized, "get projects failed"):
		return "LIST_PROJECTS_FAILED"
	case strings.Contains(normalized, "get versions failed"):
		return "LIST_VERSIONS_FAILED"
	case strings.Contains(normalized, "get version failed"):
		return "GET_VERSION_FAILED"
	case strings.Contains(normalized, "get assets failed"):
		return "LIST_ASSETS_FAILED"
	case strings.Contains(normalized, "create project failed"):
		return "CREATE_PROJECT_FAILED"
	case strings.Contains(normalized, "create version failed"):
		return "CREATE_VERSION_FAILED"
	case strings.Contains(normalized, "create asset failed"):
		return "CREATE_ASSET_FAILED"
	case strings.Contains(normalized, "update project failed"):
		return "UPDATE_PROJECT_FAILED"
	case strings.Contains(normalized, "update version failed"):
		return "UPDATE_VERSION_FAILED"
	case strings.Contains(normalized, "delete project failed"):
		return "DELETE_PROJECT_FAILED"
	case strings.Contains(normalized, "failed to patch elements"):
		return "PATCH_ELEMENTS_FAILED"
	case strings.Contains(normalized, "videourl is required"):
		return "VIDEO_URL_REQUIRED"
	case strings.Contains(normalized, "videourl is invalid"):
		return "VIDEO_URL_INVALID"
	case strings.Contains(normalized, "videourl must use http or https"):
		return "VIDEO_URL_PROTOCOL_INVALID"
	case strings.Contains(normalized, "failed to optimize prompt"):
		return "OPTIMIZE_PROMPT_FAILED"
	case strings.Contains(normalized, "failed to save file"):
		return "SAVE_FILE_FAILED"
	case strings.Contains(normalized, "failed to create upload directory"):
		return "UPLOAD_DIR_CREATE_FAILED"
	case strings.Contains(normalized, "failed to open uploaded file"):
		return "UPLOAD_FILE_OPEN_FAILED"
	case strings.Contains(normalized, "failed to read uploaded file"):
		return "UPLOAD_FILE_READ_FAILED"
	case strings.Contains(normalized, "title cannot be empty"):
		return "TITLE_REQUIRED"
	default:
		return "CANVAS_OPERATION_FAILED"
	}
}

func respondCanvasError(c *gin.Context, message string) {
	respondCanvasErrorWithCode(c, message, deriveCanvasErrorCode(message))
}

// respondCanvasErrorWithCode lets handlers surface an explicit §12.1
// error code instead of the one deriveCanvasErrorCode would pick from
// the message. Used by the schema migrator (B5) and any future path
// that owns its own error taxonomy.
func respondCanvasErrorWithCode(c *gin.Context, message, errorCode string) {
	respondCanvasErrorWithCodeAndData(c, message, errorCode, nil)
}

// respondCanvasErrorWithCodeAndData is respondCanvasErrorWithCode
// plus a structured payload merged into `data`. Used by paths whose
// error response carries server state the client wants to render
// without a second roundtrip — currently the per-Shot lock handlers
// (M4-W12-01), which include `lockState` so the UI can show "held
// by user X since Y" inline with the conflict toast.
func respondCanvasErrorWithCodeAndData(c *gin.Context, message, errorCode string, extra gin.H) {
	reload := errorCode == canvasAIErrorUnauthorized
	data := gin.H{
		"success": false,
	}
	if reload {
		data["reload"] = true
	}
	if strings.TrimSpace(errorCode) != "" {
		data["errorCode"] = errorCode
	}
	for k, v := range extra {
		data[k] = v
	}
	body := gin.H{
		"code":    response.ERROR,
		"message": message,
		"data":    data,
		"error": gin.H{
			"message":     message,
			"userMessage": message,
		},
	}
	if strings.TrimSpace(errorCode) != "" {
		body["errorCode"] = errorCode
	}
	c.JSON(http.StatusOK, body)
}
