// Auth-gated static handler for ./uploads. The original
// Router.Static("/uploads", "./uploads") in core/server.go served every
// per-user asset to anyone with a URL — DB-side ownership in
// CreateAsset/ListAssets was bypassed at the byte layer.
//
// This handler keeps the legacy behavior for non-canvas paths
// (templates, generations, agent_workspace, etc.) but enforces a check
// on /uploads/canvas/uid/{uid}/{projectUuid}/{filename}:
//
//   - owner (JWT uid matches path uid)            → serve
//   - non-owner, project visibility ≥ unlisted    → serve (shared link)
//   - non-owner, project private (or not found)   → 403
//
// The non-owner path costs one indexed DB read per request. Browsers
// cache aggressively (Last-Modified/ETag headers from http.FileServer)
// so revalidation is the common steady state, not full reads.

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	"server/model"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// uploadsFileServer is a singleton FileServer rooted at ./uploads.
// http.StripPrefix removes "/uploads" so the FileServer sees clean
// relative paths. Initialized lazily so package-level init order doesn't
// trip a CWD-not-yet-set ordering bug under tests.
var uploadsFileServer = http.StripPrefix("/uploads", http.FileServer(http.Dir("./uploads")))

// ServeUploadedFile is the replacement for Router.Static("/uploads",
// "./uploads"). Register as the GET/HEAD handler for /uploads/*filepath.
func ServeUploadedFile(c *gin.Context) {
	raw := c.Param("filepath")
	cleaned := strings.TrimPrefix(raw, "/")
	if strings.HasPrefix(cleaned, "canvas/uid/") {
		requesterUID := int(utils.GetUserID(c))
		if !canvasAssetAccessAllowed(c.Request.Context(), globals.GraDBs["system"], requesterUID, cleaned) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	} else if isCanvasBoundManagedUploadPath(cleaned) {
		requesterUID := int(utils.GetUserID(c))
		if !canvasBoundManagedUploadAccessAllowed(c.Request.Context(), globals.GraDBs["system"], requesterUID, cleaned) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}
	uploadsFileServer.ServeHTTP(c.Writer, c.Request)
}

// canvasAssetAccessAllowed authorizes a request for a canvas-asset byte
// fetch under canvas/uid/{uid}/{projectUuid}/{filename}. Owner
// short-circuits the DB lookup; non-owner requires the project to be
// visibility ≥ unlisted (resolved via GetSharedProjectByUUID, which
// already encodes the public-read predicate). Pure function — DB +
// requester injected — so the predicate is unit-testable without a
// gin.Context or globals fixture.
//
// `cleaned` is the URL path with the leading "/uploads/" stripped and
// no leading slash, i.e. "canvas/uid/42/uuid/file.png".
func canvasAssetAccessAllowed(ctx context.Context, db *gorm.DB, requesterUID int, cleaned string) bool {
	// Layout: canvas / uid / {uid} / {projectUuid} / {...filename}
	parts := strings.SplitN(cleaned, "/", 5)
	if len(parts) < 5 {
		return false
	}
	pathUID, err := strconv.Atoi(parts[2])
	if err != nil || pathUID <= 0 {
		return false
	}
	projectUUID := strings.TrimSpace(parts[3])
	if projectUUID == "" {
		return false
	}

	if requesterUID > 0 && requesterUID == pathUID {
		return true
	}

	if db == nil {
		return false
	}
	var project model.CanvasProject
	if err := db.WithContext(ctx).
		Where("uuid = ? AND deleted_at IS NULL", projectUUID).
		First(&project).Error; err != nil {
		return false
	}
	pathValue := "/uploads/" + strings.TrimPrefix(cleaned, "/")
	boundToProject := canvasAssetPathBoundToProject(ctx, db, project.Id, pathValue)
	if requesterUID > 0 && boundToProject && canvasRequesterCanViewProject(ctx, db, project.Id, requesterUID) {
		return true
	}
	var snapshot model.CanvasShareSnapshot
	if err := db.WithContext(ctx).
		Where("project_id = ?", project.Id).
		First(&snapshot).Error; err != nil {
		return false
	}
	// Path-uid sanity: refuse to serve another owner's bytes even if
	// SOMEHOW the URL/projectUuid pair de-synced (defense in depth —
	// stops a malicious client constructing a URL with a victim's uid
	// and an unrelated public project's uuid). Also require the exact
	// requested asset to be present in the published snapshot; project
	// visibility alone must not expose draft-only canvas files.
	return (project.UID == pathUID || boundToProject) &&
		(project.Visibility == canvasService.CanvasVisibilityUnlisted || project.Visibility == canvasService.CanvasVisibilityPublic) &&
		canvasShareDocumentReferencesPath(snapshot.Document, "/uploads/"+cleaned)
}

func canvasAssetPathBoundToProject(ctx context.Context, db *gorm.DB, projectID uint, pathValue string) bool {
	if db == nil || projectID == 0 {
		return false
	}
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return false
	}
	var globalCount int64
	if err := db.WithContext(ctx).
		Model(&model.GlobalAsset{}).
		Where("project_id = ? AND deleted_at IS NULL", projectID).
		Where("status <> ?", model.GlobalAssetStatusDeleted).
		Where("(url = ? OR thumb_url = ?)", pathValue, pathValue).
		Count(&globalCount).Error; err != nil {
		return false
	}
	if globalCount > 0 {
		return true
	}
	return false
}

func canvasRequesterCanViewProject(ctx context.Context, db *gorm.DB, projectID uint, requesterUID int) bool {
	if db == nil || projectID == 0 || requesterUID <= 0 {
		return false
	}
	ok, err := projectService.NewRepository(db.WithContext(ctx)).CanViewProject(projectID, uint(requesterUID))
	return err == nil && ok
}

func isCanvasBoundManagedUploadPath(cleaned string) bool {
	return strings.HasPrefix(cleaned, "references/uid/") ||
		strings.HasPrefix(cleaned, "generations/uid/") ||
		strings.HasPrefix(cleaned, "generations/reference-images/uid/")
}

func uidFromManagedUploadPath(cleaned string) (int, bool) {
	parts := strings.Split(cleaned, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "uid" {
			continue
		}
		uid, err := strconv.Atoi(parts[i+1])
		if err != nil || uid <= 0 {
			return 0, false
		}
		return uid, true
	}
	return 0, false
}

func canvasBoundManagedUploadAccessAllowed(ctx context.Context, db *gorm.DB, requesterUID int, cleaned string) bool {
	pathUID, ok := uidFromManagedUploadPath(cleaned)
	if !ok {
		return false
	}
	if requesterUID > 0 && requesterUID == pathUID {
		return true
	}
	if db == nil {
		return true
	}

	pathValue := "/uploads/" + strings.TrimPrefix(cleaned, "/")
	type row struct {
		UID         int
		SourceTable string
		ProjectUID  int
		ProjectID   uint64
		Visibility  int8
		SnapshotID  *uint
	}
	var globalRows []row
	if err := db.WithContext(ctx).
		Table(model.GlobalAsset{}.TableName()+" AS a").
		Select("a.uid AS uid, a.source_table AS source_table, p.id AS project_id, p.uid AS project_uid, p.visibility AS visibility, s.id AS snapshot_id").
		Joins("LEFT JOIN "+model.CanvasProject{}.TableName()+" AS p ON p.id = a.project_id AND p.deleted_at IS NULL").
		Joins("LEFT JOIN "+model.CanvasShareSnapshot{}.TableName()+" AS s ON s.project_id = p.id").
		Where("a.deleted_at IS NULL").
		Where("a.status <> ?", model.GlobalAssetStatusDeleted).
		Where("(a.url = ? OR a.thumb_url = ?)", pathValue, pathValue).
		Find(&globalRows).Error; err != nil {
		return false
	}
	// If the URL is not bound to a Canvas asset, keep legacy upload
	// behavior for generator/history URLs that predate Canvas privacy.
	if len(globalRows) == 0 {
		return true
	}
	for _, row := range globalRows {
		if requesterUID > 0 && requesterUID == row.UID {
			return true
		}
		if row.ProjectID == 0 {
			// New reference uploads are first-class private global assets.
			// Other unprojected rows under /uploads/generations/uid/... may
			// be legacy history/share URLs, so keep their historical static
			// behavior rather than breaking published previews.
			if row.SourceTable != "reference_upload" {
				return true
			}
			continue
		}
		if requesterUID > 0 && requesterUID == row.ProjectUID {
			return true
		}
		if requesterUID > 0 && canvasRequesterCanViewProject(ctx, db, uint(row.ProjectID), requesterUID) {
			return true
		}
		if row.SnapshotID == nil ||
			(row.Visibility != canvasService.CanvasVisibilityUnlisted && row.Visibility != canvasService.CanvasVisibilityPublic) {
			continue
		}
		var snapshot model.CanvasShareSnapshot
		if err := db.WithContext(ctx).
			Where("project_id = ?", row.ProjectID).
			First(&snapshot).Error; err != nil {
			continue
		}
		if canvasShareDocumentReferencesPath(snapshot.Document, pathValue) {
			return true
		}
	}
	return false
}

func canvasShareDocumentReferencesPath(document any, pathValue string) bool {
	pathValue = strings.TrimSpace(pathValue)
	if document == nil || pathValue == "" {
		return false
	}
	var decoded any
	if raw, ok := document.(string); ok {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return false
		}
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			return false
		}
	} else if raw, ok := document.([]byte); ok {
		if len(raw) == 0 {
			return false
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return false
		}
	} else {
		decoded = document
	}
	return jsonValueReferencesPath(decoded, pathValue)
}

func jsonValueReferencesPath(value any, pathValue string) bool {
	switch v := value.(type) {
	case string:
		s := strings.TrimSpace(v)
		return s == pathValue || strings.Contains(s, pathValue)
	case model.JSONMap:
		for _, item := range v {
			if jsonValueReferencesPath(item, pathValue) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if jsonValueReferencesPath(item, pathValue) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if jsonValueReferencesPath(item, pathValue) {
				return true
			}
		}
	}
	return false
}
