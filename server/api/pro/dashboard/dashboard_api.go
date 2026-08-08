package dashboard

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	workagentApi "server/api/pro/tools/workagent"
	"server/globals"
	"server/model"
	"server/model/common/response"
	workagentModel "server/model/workagent"
	assetLedgerService "server/service/assetledger"
	storageService "server/service/storage"
	"server/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type DashboardApi struct{}

const dashboardLargeAssetThreshold int64 = 10 * 1024 * 1024

type dashboardSpaceStorageSummary struct {
	TotalAssets           int64 `json:"totalAssets"`
	TotalBytes            int64 `json:"totalBytes"`
	ManagedObjectAssets   int64 `json:"managedObjectAssets"`
	ManagedObjectBytes    int64 `json:"managedObjectBytes"`
	LocalFileAssets       int64 `json:"localFileAssets"`
	LocalFileBytes        int64 `json:"localFileBytes"`
	ExternalRefAssets     int64 `json:"externalRefAssets"`
	GeneratedAssets       int64 `json:"generatedAssets"`
	GeneratedBytes        int64 `json:"generatedBytes"`
	HiddenGeneratedAssets int64 `json:"hiddenGeneratedAssets"`
	HiddenGeneratedBytes  int64 `json:"hiddenGeneratedBytes"`
	CanvasThumbnailAssets int64 `json:"canvasThumbnailAssets"`
	CanvasThumbnailBytes  int64 `json:"canvasThumbnailBytes"`
	InputRefAssets        int64 `json:"inputRefAssets"`
	InputRefBytes         int64 `json:"inputRefBytes"`
	CanvasAssets          int64 `json:"canvasAssets"`
	CanvasBytes           int64 `json:"canvasBytes"`
	ThreadFileAssets      int64 `json:"threadFileAssets"`
	ThreadFileBytes       int64 `json:"threadFileBytes"`
	ReferenceUploadAssets int64 `json:"referenceUploadAssets"`
	ReferenceUploadBytes  int64 `json:"referenceUploadBytes"`
}

type dashboardSpaceStorageAssetListItem struct {
	ContainerKey     string    `json:"containerKey"`
	RowID            string    `json:"rowId"`
	Source           string    `json:"source"`
	ID               uint      `json:"id"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	Title            string    `json:"title"`
	ContainerTitle   string    `json:"containerTitle"`
	ContainerUUID    string    `json:"containerUUID"`
	Kind             string    `json:"kind"`
	MimeType         string    `json:"mimeType"`
	SizeBytes        int64     `json:"sizeBytes"`
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	URL              string    `json:"url"`
	ThumbURL         string    `json:"thumbUrl"`
	PreviewURL       string    `json:"previewUrl"`
	ProjectID        uint      `json:"projectId"`
	ProjectTitle     string    `json:"projectTitle"`
	ProjectUUID      string    `json:"projectUUID"`
	ThreadID         uint      `json:"threadId"`
	ThreadName       string    `json:"threadName"`
	ThreadUUID       string    `json:"threadUUID"`
	ToolID           string    `json:"toolId"`
	RecordID         uint      `json:"recordId"`
	TaskID           string    `json:"taskId"`
	ObjectKey        string    `json:"objectKey"`
	StoragePath      string    `json:"storagePath"`
	IsAttached       bool      `json:"isAttached"`
	HasManagedObject bool      `json:"hasManagedObject"`
	CanDelete        bool      `json:"canDelete"`
	DeleteKind       string    `json:"deleteKind"`
	IsHidden         bool      `json:"isHidden"`
}

type dashboardSpaceStorageAssetListResponse struct {
	Items []dashboardSpaceStorageAssetListItem `json:"items"`
	Total int64                                `json:"total"`
	Page  int                                  `json:"page"`
	Limit int                                  `json:"limit"`
}

type dashboardSpaceStorageContainerListItem struct {
	ContainerKey     string    `json:"containerKey"`
	Source           string    `json:"source"`
	ContainerKind    string    `json:"containerKind"`
	ContainerTitle   string    `json:"containerTitle"`
	ContainerUUID    string    `json:"containerUUID"`
	AssetCount       int64     `json:"assetCount"`
	HiddenAssetCount int64     `json:"hiddenAssetCount"`
	TotalBytes       int64     `json:"totalBytes"`
	DeletableCount   int64     `json:"deletableCount"`
	LatestCreated    time.Time `json:"latestCreated"`
	PreviewURL       string    `json:"previewUrl"`
}

type dashboardSpaceStorageContainerListResponse struct {
	Items []dashboardSpaceStorageContainerListItem `json:"items"`
	Total int64                                    `json:"total"`
	Page  int                                      `json:"page"`
	Limit int                                      `json:"limit"`
}

type dashboardSpaceStorageAssetRow struct {
	ContainerKey     string    `gorm:"column:container_key"`
	RowID            string    `gorm:"column:row_id"`
	Source           string    `gorm:"column:source"`
	ID               uint      `gorm:"column:id"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
	Title            string    `gorm:"column:title"`
	ContainerTitle   string    `gorm:"column:container_title"`
	ContainerUUID    string    `gorm:"column:container_uuid"`
	Kind             string    `gorm:"column:kind"`
	MimeType         string    `gorm:"column:mime_type"`
	SizeBytes        int64     `gorm:"column:size_bytes"`
	Width            int       `gorm:"column:width"`
	Height           int       `gorm:"column:height"`
	URL              string    `gorm:"column:url"`
	ThumbURL         string    `gorm:"column:thumb_url"`
	PreviewURL       string    `gorm:"column:preview_url"`
	ProjectID        uint      `gorm:"column:project_id"`
	ProjectTitle     string    `gorm:"column:project_title"`
	ProjectUUID      string    `gorm:"column:project_uuid"`
	ThreadID         uint      `gorm:"column:thread_id"`
	ThreadName       string    `gorm:"column:thread_name"`
	ThreadUUID       string    `gorm:"column:thread_uuid"`
	ToolID           string    `gorm:"column:tool_id"`
	RecordID         uint      `gorm:"column:record_id"`
	TaskID           string    `gorm:"column:task_id"`
	ObjectKey        string    `gorm:"column:object_key"`
	StoragePath      string    `gorm:"column:storage_path"`
	IsAttached       bool      `gorm:"column:is_attached"`
	HasManagedObject bool      `gorm:"column:has_managed_object"`
	CanDelete        bool      `gorm:"column:can_delete"`
	DeleteKind       string    `gorm:"column:delete_kind"`
	IsHidden         bool      `gorm:"column:is_hidden"`
}

type dashboardSpaceStorageContainerRow struct {
	ContainerKey     string    `gorm:"column:container_key"`
	Source           string    `gorm:"column:source"`
	ContainerKind    string    `gorm:"column:container_kind"`
	ContainerTitle   string    `gorm:"column:container_title"`
	ContainerUUID    string    `gorm:"column:container_uuid"`
	AssetCount       int64     `gorm:"column:asset_count"`
	HiddenAssetCount int64     `gorm:"column:hidden_asset_count"`
	TotalBytes       int64     `gorm:"column:total_bytes"`
	DeletableCount   int64     `gorm:"column:deletable_count"`
	LatestCreated    time.Time `gorm:"column:latest_created_at"`
	PreviewURL       string    `gorm:"column:preview_url"`
}

type deleteDashboardSpaceStorageAssetItem struct {
	Source string `json:"source"`
	ID     uint   `json:"id"`
}

type deleteDashboardSpaceStorageAssetsRequest struct {
	Items []deleteDashboardSpaceStorageAssetItem `json:"items"`
}

type updateDashboardGeneratedAssetVisibilityRequest struct {
	Hidden bool `json:"hidden"`
}

type updateDashboardGeneratedContainerVisibilityRequest struct {
	ContainerKey           string `json:"containerKey"`
	Hidden                 bool   `json:"hidden"`
	SizeFilter             string `json:"size"`
	HiddenFilter           string `json:"hiddenFilter"`
	IncludeHiddenGenerated bool   `json:"includeHiddenGenerated"`
	SearchQuery            string `json:"query"`
}

func (n *DashboardApi) GetSpaceStorageSummary(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Unauthorized"})
		return
	}

	db := globals.GraDBs["system"]
	summary := dashboardSpaceStorageSummary{}

	// Coalesce 7 source-bucketed COUNT/SUM aggregates into one
	// conditional-aggregation pass over w_user_asset_ledger. Each
	// CASE branch counts/sums only the rows that match its bucket,
	// so a single index scan over (uid) replaces 7 separate scans.
	// The managed/local/external-ref totals below stay as their own
	// queries because they aggregate over DIFFERENT row sets (deduped
	// by object_key, or filtered by storage_path).
	if err := db.Model(&model.UserAssetLedger{}).
		Where("uid = ?", int(uid)).
		Select(`
			SUM(CASE WHEN source = 'generated' AND visibility_status = ? THEN 1 ELSE 0 END) AS generated_assets,
			SUM(CASE WHEN source = 'generated' AND visibility_status = ? THEN size_bytes ELSE 0 END) AS generated_bytes,
			SUM(CASE WHEN source = 'generated' AND visibility_status = ? THEN 1 ELSE 0 END) AS hidden_generated_assets,
			SUM(CASE WHEN source = 'generated' AND visibility_status = ? THEN size_bytes ELSE 0 END) AS hidden_generated_bytes,
			SUM(CASE WHEN source IN ('canvas_project_thumbnail', 'canvas_snapshot_thumbnail') THEN 1 ELSE 0 END) AS canvas_thumbnail_assets,
			SUM(CASE WHEN source IN ('canvas_project_thumbnail', 'canvas_snapshot_thumbnail') THEN size_bytes ELSE 0 END) AS canvas_thumbnail_bytes,
			SUM(CASE WHEN source = 'generation_input' THEN 1 ELSE 0 END) AS input_ref_assets,
			SUM(CASE WHEN source = 'generation_input' THEN size_bytes ELSE 0 END) AS input_ref_bytes,
			SUM(CASE WHEN source = 'canvas' THEN 1 ELSE 0 END) AS canvas_assets,
			SUM(CASE WHEN source = 'canvas' THEN size_bytes ELSE 0 END) AS canvas_bytes,
			SUM(CASE WHEN source IN ('thread_upload', 'thread_output') THEN 1 ELSE 0 END) AS thread_file_assets,
			SUM(CASE WHEN source IN ('thread_upload', 'thread_output') THEN size_bytes ELSE 0 END) AS thread_file_bytes,
			SUM(CASE WHEN source = 'reference_upload' THEN 1 ELSE 0 END) AS reference_upload_assets,
			SUM(CASE WHEN source = 'reference_upload' THEN size_bytes ELSE 0 END) AS reference_upload_bytes
		`,
			model.UserAssetLedgerVisibilityVisible,
			model.UserAssetLedgerVisibilityVisible,
			model.UserAssetLedgerVisibilityHidden,
			model.UserAssetLedgerVisibilityHidden,
		).
		Scan(&summary).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load asset bucket summary"})
		return
	}
	if err := db.Raw(`
SELECT
	COUNT(*) AS managed_object_assets,
	COALESCE(SUM(objects.size_bytes), 0) AS managed_object_bytes
FROM (
	SELECT object_key, MAX(size_bytes) AS size_bytes
	FROM w_user_asset_ledger
	WHERE uid = ? AND object_key <> ''
	GROUP BY object_key
) AS objects
`, int(uid)).Scan(&summary).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load managed storage summary"})
		return
	}
	if err := db.Raw(`
SELECT
	COUNT(*) AS local_file_assets,
	COALESCE(SUM(items.size_bytes), 0) AS local_file_bytes
FROM (
	SELECT CONCAT(source, ':', source_id, ':', item_key) AS row_id, size_bytes
	FROM w_user_asset_ledger
	WHERE uid = ?
		AND has_managed_object = 0
		AND (
			storage_path COLLATE utf8mb4_unicode_ci LIKE '/uploads/%'
			OR storage_path COLLATE utf8mb4_unicode_ci LIKE 'uid/%'
			OR storage_path COLLATE utf8mb4_unicode_ci LIKE 'agent_workspace/%'
		)
) AS items
`, int(uid)).Scan(&summary).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load local file summary"})
		return
	}
	if err := db.Raw(`
SELECT
	COUNT(*) AS external_ref_assets
FROM (
	SELECT CONCAT(source, ':', source_id, ':', item_key) AS row_id
	FROM w_user_asset_ledger
	WHERE uid = ?
		AND has_managed_object = 0
		AND url <> ''
		AND NOT (
			storage_path COLLATE utf8mb4_unicode_ci LIKE '/uploads/%'
			OR storage_path COLLATE utf8mb4_unicode_ci LIKE 'uid/%'
			OR storage_path COLLATE utf8mb4_unicode_ci LIKE 'agent_workspace/%'
		)
) AS external_refs
`, int(uid)).Scan(&summary).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load external reference summary"})
		return
	}

	summary.TotalAssets = summary.GeneratedAssets + summary.HiddenGeneratedAssets + summary.CanvasThumbnailAssets + summary.InputRefAssets + summary.CanvasAssets + summary.ThreadFileAssets + summary.ReferenceUploadAssets
	summary.TotalBytes = summary.GeneratedBytes + summary.HiddenGeneratedBytes + summary.CanvasThumbnailBytes + summary.InputRefBytes + summary.CanvasBytes + summary.ThreadFileBytes + summary.ReferenceUploadBytes
	response.OkWithData(summary, c)
}

func (n *DashboardApi) ListSpaceStorageAssets(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "24"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}

	sourceFilter := normalizeDashboardStorageSourceFilter(c.DefaultQuery("source", "all"))
	storageFilter := normalizeDashboardStorageManagedFilter(c.DefaultQuery("storage", "all"))
	sizeFilter := normalizeDashboardStorageSizeFilter(c.DefaultQuery("size", "all"))
	hiddenFilter := normalizeDashboardStorageHiddenFilter(c.DefaultQuery("hidden", "visible"))
	includeHiddenGenerated := parseDashboardStorageBoolQuery(c.Query("includeHiddenGenerated"))
	containerKeyFilter := normalizeDashboardStorageOptionalString(c.DefaultQuery("containerKey", ""))
	searchQuery := normalizeDashboardStorageOptionalString(c.DefaultQuery("query", ""))
	sortBy := strings.ToLower(strings.TrimSpace(c.DefaultQuery("sort", "newest")))
	orderBy := "u.created_at DESC"
	switch sortBy {
	case "oldest":
		orderBy = "u.created_at ASC"
	case "size_desc":
		orderBy = "u.size_bytes DESC, u.created_at DESC"
	case "size_asc":
		orderBy = "u.size_bytes ASC, u.created_at DESC"
	}

	db := globals.GraDBs["system"]
	unionSQL := buildDashboardStorageUnionSQL()
	params := []interface{}{int(uid)}

	whereClauses := []string{"1 = 1"}
	if sourceFilter != "all" {
		switch sourceFilter {
		case "thread":
			whereClauses = append(whereClauses, "u.source IN ('thread_upload', 'thread_output')")
		case "canvas":
			whereClauses = append(whereClauses, "u.source IN ('canvas', 'canvas_project_thumbnail', 'canvas_snapshot_thumbnail')")
		case "input":
			whereClauses = append(whereClauses, "u.source = 'generation_input'")
		default:
			whereClauses = append(whereClauses, "u.source = ?")
			params = append(params, sourceFilter)
		}
	}
	if includeHiddenGenerated {
		switch hiddenFilter {
		case "hidden":
			whereClauses = append(whereClauses, "u.is_hidden = 1")
		case "all":
			// keep both visible and hidden generated assets in the result
		default:
			whereClauses = append(whereClauses, "u.is_hidden = 0")
		}
	} else {
		whereClauses = append(whereClauses, "u.is_hidden = 0")
	}
	if containerKeyFilter != "" {
		whereClauses = append(whereClauses, "u.container_key = ?")
		params = append(params, containerKeyFilter)
	}
	if storageFilter == "managed" {
		whereClauses = append(whereClauses, "u.has_managed_object = 1")
	} else if storageFilter == "local" {
		whereClauses = append(whereClauses, "u.has_managed_object = 0 AND (u.storage_path COLLATE utf8mb4_unicode_ci LIKE '/uploads/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'uid/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'agent_workspace/%')")
	} else if storageFilter == "external" {
		whereClauses = append(whereClauses, "u.has_managed_object = 0 AND u.url <> '' AND NOT (u.storage_path COLLATE utf8mb4_unicode_ci LIKE '/uploads/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'uid/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'agent_workspace/%')")
	}
	if sizeFilter == "large" {
		whereClauses = append(whereClauses, "u.size_bytes >= ?")
		params = append(params, dashboardLargeAssetThreshold)
	}
	if searchQuery != "" {
		like := "%" + searchQuery + "%"
		whereClauses = append(whereClauses, "(u.title COLLATE utf8mb4_unicode_ci LIKE ? OR u.container_title COLLATE utf8mb4_unicode_ci LIKE ? OR u.kind COLLATE utf8mb4_unicode_ci LIKE ? OR u.mime_type COLLATE utf8mb4_unicode_ci LIKE ? OR u.tool_id COLLATE utf8mb4_unicode_ci LIKE ?)")
		params = append(params, like, like, like, like, like)
	}

	whereSQL := strings.Join(whereClauses, " AND ")
	countSQL := fmt.Sprintf("SELECT COUNT(*) AS total FROM (%s) AS u WHERE %s", unionSQL, whereSQL)

	var total int64
	if err := db.Raw(countSQL, params...).Scan(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load storage assets"})
		return
	}

	listParams := append(append([]interface{}{}, params...), limit, (page-1)*limit)
	listSQL := fmt.Sprintf("SELECT * FROM (%s) AS u WHERE %s ORDER BY %s LIMIT ? OFFSET ?", unionSQL, whereSQL, orderBy)

	var rows []dashboardSpaceStorageAssetRow
	if err := db.Raw(listSQL, listParams...).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load storage assets"})
		return
	}

	items := make([]dashboardSpaceStorageAssetListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardSpaceStorageAssetListItem{
			ContainerKey:     row.ContainerKey,
			RowID:            row.RowID,
			Source:           row.Source,
			ID:               row.ID,
			CreatedAt:        row.CreatedAt,
			UpdatedAt:        row.UpdatedAt,
			Title:            strings.TrimSpace(row.Title),
			ContainerTitle:   strings.TrimSpace(row.ContainerTitle),
			ContainerUUID:    strings.TrimSpace(row.ContainerUUID),
			Kind:             strings.TrimSpace(row.Kind),
			MimeType:         strings.TrimSpace(row.MimeType),
			SizeBytes:        row.SizeBytes,
			Width:            row.Width,
			Height:           row.Height,
			URL:              strings.TrimSpace(row.URL),
			ThumbURL:         strings.TrimSpace(row.ThumbURL),
			PreviewURL:       strings.TrimSpace(row.PreviewURL),
			ProjectID:        row.ProjectID,
			ProjectTitle:     strings.TrimSpace(row.ProjectTitle),
			ProjectUUID:      strings.TrimSpace(row.ProjectUUID),
			ThreadID:         row.ThreadID,
			ThreadName:       strings.TrimSpace(row.ThreadName),
			ThreadUUID:       strings.TrimSpace(row.ThreadUUID),
			ToolID:           strings.TrimSpace(row.ToolID),
			RecordID:         row.RecordID,
			TaskID:           strings.TrimSpace(row.TaskID),
			ObjectKey:        strings.TrimSpace(row.ObjectKey),
			StoragePath:      strings.TrimSpace(row.StoragePath),
			IsAttached:       row.IsAttached,
			HasManagedObject: row.HasManagedObject,
			CanDelete:        row.CanDelete,
			DeleteKind:       strings.TrimSpace(row.DeleteKind),
			IsHidden:         row.IsHidden,
		})
	}

	response.OkWithData(dashboardSpaceStorageAssetListResponse{
		Items: items,
		Total: total,
		Page:  page,
		Limit: limit,
	}, c)
}

func (n *DashboardApi) ListSpaceStorageContainers(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "24"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}

	sourceFilter := normalizeDashboardStorageSourceFilter(c.DefaultQuery("source", "all"))
	storageFilter := normalizeDashboardStorageManagedFilter(c.DefaultQuery("storage", "all"))
	sizeFilter := normalizeDashboardStorageSizeFilter(c.DefaultQuery("size", "all"))
	hiddenFilter := normalizeDashboardStorageHiddenFilter(c.DefaultQuery("hidden", "visible"))
	includeHiddenGenerated := parseDashboardStorageBoolQuery(c.Query("includeHiddenGenerated"))
	containerKeyFilter := normalizeDashboardStorageOptionalString(c.DefaultQuery("containerKey", ""))
	searchQuery := normalizeDashboardStorageOptionalString(c.DefaultQuery("query", ""))
	sortBy := strings.ToLower(strings.TrimSpace(c.DefaultQuery("sort", "newest")))
	orderBy := "c.latest_created_at DESC"
	switch sortBy {
	case "oldest":
		orderBy = "c.latest_created_at ASC"
	case "size_desc":
		orderBy = "c.total_bytes DESC, c.latest_created_at DESC"
	case "size_asc":
		orderBy = "c.total_bytes ASC, c.latest_created_at DESC"
	}

	unionSQL := buildDashboardStorageUnionSQL()
	params := []interface{}{int(uid)}
	whereClauses := []string{"1 = 1"}
	if sourceFilter != "all" {
		switch sourceFilter {
		case "thread":
			whereClauses = append(whereClauses, "u.source IN ('thread_upload', 'thread_output')")
		case "canvas":
			whereClauses = append(whereClauses, "u.source IN ('canvas', 'canvas_project_thumbnail', 'canvas_snapshot_thumbnail')")
		case "input":
			whereClauses = append(whereClauses, "u.source = 'generation_input'")
		default:
			whereClauses = append(whereClauses, "u.source = ?")
			params = append(params, sourceFilter)
		}
	}
	if includeHiddenGenerated {
		switch hiddenFilter {
		case "hidden":
			whereClauses = append(whereClauses, "u.is_hidden = 1")
		case "all":
			// keep both visible and hidden generated assets in the result
		default:
			whereClauses = append(whereClauses, "u.is_hidden = 0")
		}
	} else {
		whereClauses = append(whereClauses, "u.is_hidden = 0")
	}
	if containerKeyFilter != "" {
		whereClauses = append(whereClauses, "u.container_key = ?")
		params = append(params, containerKeyFilter)
	}
	if storageFilter == "managed" {
		whereClauses = append(whereClauses, "u.has_managed_object = 1")
	} else if storageFilter == "local" {
		whereClauses = append(whereClauses, "u.has_managed_object = 0 AND (u.storage_path COLLATE utf8mb4_unicode_ci LIKE '/uploads/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'uid/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'agent_workspace/%')")
	} else if storageFilter == "external" {
		whereClauses = append(whereClauses, "u.has_managed_object = 0 AND u.url <> '' AND NOT (u.storage_path COLLATE utf8mb4_unicode_ci LIKE '/uploads/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'uid/%' OR u.storage_path COLLATE utf8mb4_unicode_ci LIKE 'agent_workspace/%')")
	}
	if sizeFilter == "large" {
		whereClauses = append(whereClauses, "u.size_bytes >= ?")
		params = append(params, dashboardLargeAssetThreshold)
	}
	if searchQuery != "" {
		like := "%" + searchQuery + "%"
		whereClauses = append(whereClauses, "(u.title COLLATE utf8mb4_unicode_ci LIKE ? OR u.container_title COLLATE utf8mb4_unicode_ci LIKE ? OR u.kind COLLATE utf8mb4_unicode_ci LIKE ? OR u.mime_type COLLATE utf8mb4_unicode_ci LIKE ? OR u.tool_id COLLATE utf8mb4_unicode_ci LIKE ?)")
		params = append(params, like, like, like, like, like)
	}
	whereSQL := strings.Join(whereClauses, " AND ")

	groupedSQL := fmt.Sprintf(`
SELECT
	u.container_key AS container_key,
	MIN(u.source) AS source,
	CASE
		WHEN MIN(u.source) = 'generated' THEN 'tool'
		WHEN MIN(u.source) = 'generation_input' THEN 'record'
		WHEN MIN(u.source) IN ('canvas', 'canvas_project_thumbnail', 'canvas_snapshot_thumbnail') THEN 'project'
		ELSE 'thread'
	END AS container_kind,
	COALESCE(NULLIF(MAX(u.container_title), ''), NULLIF(MAX(u.title), ''), 'asset-container') AS container_title,
	MAX(u.container_uuid) AS container_uuid,
	COUNT(*) AS asset_count,
	COALESCE(SUM(CASE WHEN u.is_hidden THEN 1 ELSE 0 END), 0) AS hidden_asset_count,
	COALESCE(SUM(u.size_bytes), 0) AS total_bytes,
	COALESCE(SUM(CASE WHEN u.can_delete THEN 1 ELSE 0 END), 0) AS deletable_count,
	MAX(u.created_at) AS latest_created_at,
	SUBSTRING_INDEX(MAX(CASE WHEN NULLIF(u.preview_url, '') IS NOT NULL THEN CONCAT(DATE_FORMAT(u.created_at, '%%Y%%m%%d%%H%%i%%s'), '|', u.preview_url) ELSE '' END), '|', -1) AS preview_url
FROM (%s) AS u
WHERE %s
GROUP BY u.container_key
`, unionSQL, whereSQL)

	db := globals.GraDBs["system"]
	countSQL := fmt.Sprintf("SELECT COUNT(*) AS total FROM (%s) AS c", groupedSQL)
	var total int64
	if err := db.Raw(countSQL, params...).Scan(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load storage containers"})
		return
	}

	listParams := append(append([]interface{}{}, params...), limit, (page-1)*limit)
	listSQL := fmt.Sprintf("SELECT * FROM (%s) AS c ORDER BY %s LIMIT ? OFFSET ?", groupedSQL, orderBy)
	var rows []dashboardSpaceStorageContainerRow
	if err := db.Raw(listSQL, listParams...).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to load storage containers"})
		return
	}

	items := make([]dashboardSpaceStorageContainerListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardSpaceStorageContainerListItem{
			ContainerKey:     strings.TrimSpace(row.ContainerKey),
			Source:           strings.TrimSpace(row.Source),
			ContainerKind:    strings.TrimSpace(row.ContainerKind),
			ContainerTitle:   strings.TrimSpace(row.ContainerTitle),
			ContainerUUID:    strings.TrimSpace(row.ContainerUUID),
			AssetCount:       row.AssetCount,
			HiddenAssetCount: row.HiddenAssetCount,
			TotalBytes:       row.TotalBytes,
			DeletableCount:   row.DeletableCount,
			LatestCreated:    row.LatestCreated,
			PreviewURL:       strings.TrimSpace(row.PreviewURL),
		})
	}

	response.OkWithData(dashboardSpaceStorageContainerListResponse{
		Items: items,
		Total: total,
		Page:  page,
		Limit: limit,
	}, c)
}

func (n *DashboardApi) DeleteSpaceStorageAsset(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Unauthorized"})
		return
	}

	source := normalizeDashboardStorageSourceFilter(c.Param("source"))
	assetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || assetID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "Invalid asset id"})
		return
	}

	if err := deleteDashboardStorageAssetBySource(c, int(uid), source, uint(assetID)); err != nil {
		respondDashboardStorageDeleteError(c, err)
		return
	}

	response.Ok(c)
}

func (n *DashboardApi) DeleteSpaceStorageAssets(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Unauthorized"})
		return
	}

	var req deleteDashboardSpaceStorageAssetsRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "Invalid assets"})
		return
	}
	if len(req.Items) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "Too many assets"})
		return
	}

	for _, item := range req.Items {
		if item.ID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "Invalid asset id"})
			return
		}
		if err := deleteDashboardStorageAssetBySource(c, int(uid), normalizeDashboardStorageSourceFilter(item.Source), item.ID); err != nil {
			respondDashboardStorageDeleteError(c, err)
			return
		}
	}

	response.Ok(c)
}

func (n *DashboardApi) UpdateGeneratedAssetVisibility(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Unauthorized"})
		return
	}

	assetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || assetID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "Invalid asset id"})
		return
	}

	var req updateDashboardGeneratedAssetVisibilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "Invalid request"})
		return
	}

	targetStatus := model.GenerationObjectStatusActive
	allowedCurrent := []int8{model.GenerationObjectStatusActive, model.GenerationObjectStatusHidden}
	if req.Hidden {
		targetStatus = model.GenerationObjectStatusHidden
	}

	db := globals.GraDBs["system"]
	result := db.
		Model(&model.GenerationObject{}).
		Where("id = ? AND uid = ? AND status IN ?", uint(assetID), int(uid), allowedCurrent).
		Update("status", targetStatus)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to update generated asset visibility"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "Generated asset not found"})
		return
	}
	if err := assetLedgerService.New().UpdateGeneratedVisibilityByIDWithDB(db, int(uid), uint(assetID), req.Hidden); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to update generated asset visibility"})
		return
	}
	if err := syncGeneratedGlobalAssetStatus(db, int(uid), []uint{uint(assetID)}, targetStatus); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to update generated global asset visibility"})
		return
	}

	response.Ok(c)
}

func (n *DashboardApi) UpdateGeneratedContainerVisibility(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Unauthorized"})
		return
	}

	var req updateDashboardGeneratedContainerVisibilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "Invalid request"})
		return
	}

	toolID, err := parseGeneratedContainerToolID(req.ContainerKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}

	targetStatus := model.GenerationObjectStatusActive
	allowedCurrent := []int8{model.GenerationObjectStatusActive, model.GenerationObjectStatusHidden}
	if req.Hidden {
		targetStatus = model.GenerationObjectStatusHidden
	}
	sizeFilter := normalizeDashboardStorageSizeFilter(req.SizeFilter)
	hiddenFilter := normalizeDashboardStorageHiddenFilter(req.HiddenFilter)
	searchQuery := strings.TrimSpace(req.SearchQuery)

	db := globals.GraDBs["system"]
	query := db.
		Model(&model.GenerationObject{}).
		Where("uid = ? AND status IN ?", int(uid), allowedCurrent)
	if toolID == "" {
		query = query.Where("tool_id = ''")
	} else {
		query = query.Where("tool_id = ?", toolID)
	}
	if req.IncludeHiddenGenerated {
		switch hiddenFilter {
		case "hidden":
			query = query.Where("status = ?", model.GenerationObjectStatusHidden)
		case "all":
			// keep both visible and hidden generated assets in scope
		default:
			query = query.Where("status = ?", model.GenerationObjectStatusActive)
		}
	} else {
		query = query.Where("status = ?", model.GenerationObjectStatusActive)
	}
	if sizeFilter == "large" {
		query = query.Where("size_bytes >= ?", dashboardLargeAssetThreshold)
	}
	if searchQuery != "" {
		like := "%" + searchQuery + "%"
		query = query.Where(
			"(CONCAT(COALESCE(NULLIF(tool_id, ''), 'generator'), ':', COALESCE(NULLIF(asset_kind, ''), 'asset')) COLLATE utf8mb4_unicode_ci LIKE ? OR COALESCE(NULLIF(tool_id, ''), 'generator') COLLATE utf8mb4_unicode_ci LIKE ? OR asset_kind COLLATE utf8mb4_unicode_ci LIKE ? OR content_type COLLATE utf8mb4_unicode_ci LIKE ? OR tool_id COLLATE utf8mb4_unicode_ci LIKE ?)",
			like, like, like, like, like,
		)
	}

	var objectIDs []uint
	if err := query.Session(&gorm.Session{}).Pluck("id", &objectIDs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to update generated container visibility"})
		return
	}
	result := query.Update("status", targetStatus)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to update generated container visibility"})
		return
	}
	visibility := model.UserAssetLedgerVisibilityVisible
	if req.Hidden {
		visibility = model.UserAssetLedgerVisibilityHidden
	}
	ledgerQuery := db.Model(&model.UserAssetLedger{}).
		Where("uid = ? AND source = ?", int(uid), "generated")
	if toolID == "" {
		ledgerQuery = ledgerQuery.Where("tool_id = ''")
	} else {
		ledgerQuery = ledgerQuery.Where("tool_id = ?", toolID)
	}
	if req.IncludeHiddenGenerated {
		switch hiddenFilter {
		case "hidden":
			ledgerQuery = ledgerQuery.Where("visibility_status = ?", model.UserAssetLedgerVisibilityHidden)
		case "all":
		default:
			ledgerQuery = ledgerQuery.Where("visibility_status = ?", model.UserAssetLedgerVisibilityVisible)
		}
	} else {
		ledgerQuery = ledgerQuery.Where("visibility_status = ?", model.UserAssetLedgerVisibilityVisible)
	}
	if sizeFilter == "large" {
		ledgerQuery = ledgerQuery.Where("size_bytes >= ?", dashboardLargeAssetThreshold)
	}
	if searchQuery != "" {
		like := "%" + searchQuery + "%"
		ledgerQuery = ledgerQuery.Where(
			"(title COLLATE utf8mb4_unicode_ci LIKE ? OR container_title COLLATE utf8mb4_unicode_ci LIKE ? OR kind COLLATE utf8mb4_unicode_ci LIKE ? OR mime_type COLLATE utf8mb4_unicode_ci LIKE ? OR tool_id COLLATE utf8mb4_unicode_ci LIKE ?)",
			like, like, like, like, like,
		)
	}
	if err := ledgerQuery.Update("visibility_status", visibility).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to update generated container visibility"})
		return
	}
	if err := syncGeneratedGlobalAssetStatus(db, int(uid), objectIDs, targetStatus); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "Failed to update generated global asset visibility"})
		return
	}

	response.OkWithData(gin.H{"affected": result.RowsAffected}, c)
}

func buildDashboardStorageUnionSQL() string {
	return `
SELECT
	l.container_key AS container_key,
	CASE
		WHEN l.item_key <> '' THEN CONCAT(l.source, ':', l.source_id, ':', l.item_key)
		ELSE CONCAT(l.source, ':', l.source_id)
	END AS row_id,
	l.source AS source,
	l.source_id AS id,
	l.created_at AS created_at,
	l.updated_at AS updated_at,
	l.title AS title,
	l.container_title AS container_title,
	l.container_uuid AS container_uuid,
	l.kind AS kind,
	l.mime_type AS mime_type,
	l.size_bytes AS size_bytes,
	l.width AS width,
	l.height AS height,
	l.url AS url,
	l.thumb_url AS thumb_url,
	l.preview_url AS preview_url,
	l.project_id AS project_id,
	l.project_title AS project_title,
	l.project_uuid AS project_uuid,
	l.thread_id AS thread_id,
	l.thread_name AS thread_name,
	l.thread_uuid AS thread_uuid,
	l.tool_id AS tool_id,
	l.record_id AS record_id,
	l.task_id AS task_id,
	l.object_key AS object_key,
	l.storage_path AS storage_path,
	l.is_attached AS is_attached,
	l.has_managed_object AS has_managed_object,
	CASE
		WHEN l.source = 'canvas' AND l.project_id = 0 THEN 1
		WHEN l.source IN ('thread_upload', 'thread_output') THEN 1
		WHEN l.source = 'reference_upload' THEN 1
		ELSE 0
	END AS can_delete,
	CASE
		WHEN l.source = 'canvas' AND l.project_id = 0 THEN 'canvas'
		WHEN l.source IN ('thread_upload', 'thread_output') THEN 'thread'
		WHEN l.source = 'reference_upload' THEN 'reference_upload'
		ELSE ''
	END AS delete_kind,
	CASE WHEN l.visibility_status = 2 THEN 1 ELSE 0 END AS is_hidden
FROM w_user_asset_ledger AS l
WHERE l.uid = ?
`
}

func normalizeDashboardStorageSourceFilter(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "generated", "canvas", "thread", "thread_upload", "thread_output", "reference_upload", "canvas_project_thumbnail", "canvas_snapshot_thumbnail", "input", "generation_input":
		return trimmed
	default:
		return "all"
	}
}

func normalizeDashboardStorageManagedFilter(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "managed", "local", "external":
		return trimmed
	default:
		return "all"
	}
}

func normalizeDashboardStorageSizeFilter(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "large":
		return trimmed
	default:
		return "all"
	}
}

func normalizeDashboardStorageHiddenFilter(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "all", "hidden":
		return trimmed
	default:
		return "visible"
	}
}

func parseDashboardStorageBoolQuery(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	return trimmed == "1" || trimmed == "true" || trimmed == "yes" || trimmed == "on"
}

func normalizeDashboardStorageOptionalString(value string) string {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if trimmed == "" || lower == "undefined" || lower == "null" {
		return ""
	}
	return trimmed
}

func parseGeneratedContainerToolID(containerKey string) (string, error) {
	trimmed := strings.TrimSpace(containerKey)
	if !strings.HasPrefix(trimmed, "generated:") {
		return "", fmt.Errorf("invalid generated container key")
	}
	toolID := strings.TrimPrefix(trimmed, "generated:")
	if toolID == "" {
		return "", fmt.Errorf("invalid generated container key")
	}
	if toolID == "generator" {
		return "", nil
	}
	return toolID, nil
}

func syncGeneratedGlobalAssetStatus(db *gorm.DB, uid int, objectIDs []uint, generationStatus int8) error {
	if db == nil || uid == 0 || len(objectIDs) == 0 {
		return nil
	}
	globalStatus := model.GlobalAssetStatusActive
	if generationStatus == model.GenerationObjectStatusHidden ||
		generationStatus == model.GenerationObjectStatusDeleted ||
		generationStatus == model.GenerationObjectStatusOrphan {
		globalStatus = model.GlobalAssetStatusDeleted
	}
	return db.Model(&model.GlobalAsset{}).
		Where("uid = ? AND source_table = ? AND source_id IN ?", uid, "w_generation_object", objectIDs).
		Update("status", globalStatus).Error
}

func deleteDashboardStorageAssetBySource(c *gin.Context, uid int, source string, assetID uint) error {
	switch source {
	case "canvas":
		return deleteDashboardCanvasAsset(c, uid, assetID)
	case "thread", "thread_upload", "thread_output":
		return deleteDashboardThreadFile(uid, assetID)
	case "reference_upload":
		return deleteDashboardReferenceUpload(c, uid, assetID)
	default:
		return fmt.Errorf("bad_request:unsupported source")
	}
}

func deleteDashboardCanvasAsset(c *gin.Context, uid int, assetID uint) error {
	db := globals.GraDBs["system"]
	var asset model.GlobalAsset
	if err := db.Where("id = ? AND uid = ? AND source_table = ? AND deleted_at IS NULL", assetID, uid, "canvas_project_file").First(&asset).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("not_found:asset not found")
		}
		return fmt.Errorf("internal:failed to load asset")
	}
	if asset.ProjectID != nil {
		return fmt.Errorf("conflict:attached assets cannot be deleted from dashboard storage")
	}

	if err := deleteDashboardSpaceStorageGlobalAssetBinary(c, &asset); err != nil {
		return err
	}

	now := time.Now()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.GlobalAsset{}).
			Where("id = ? AND uid = ? AND source_table = ? AND deleted_at IS NULL", assetID, uid, "canvas_project_file").
			Updates(map[string]interface{}{
				"status":     model.GlobalAssetStatusDeleted,
				"deleted_at": now,
			}).Error; err != nil {
			return fmt.Errorf("internal:failed to delete global asset")
		}
		if err := assetLedgerService.New().DeleteCanvasByIDWithDB(tx, uid, assetID); err != nil {
			return fmt.Errorf("internal:failed to delete asset ledger")
		}
		return nil
	})
}

func deleteDashboardReferenceUpload(c *gin.Context, uid int, assetID uint) error {
	db := globals.GraDBs["system"]
	var asset model.GlobalAsset
	if err := db.Where("id = ? AND uid = ? AND source_table = ? AND deleted_at IS NULL", assetID, uid, "reference_upload").First(&asset).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("not_found:asset not found")
		}
		return fmt.Errorf("internal:failed to load asset")
	}
	if asset.ProjectID != nil {
		return fmt.Errorf("conflict:attached assets cannot be deleted from dashboard storage")
	}
	if err := deleteDashboardSpaceStorageGlobalAssetBinary(c, &asset); err != nil {
		return err
	}

	now := time.Now()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.GlobalAsset{}).
			Where("id = ? AND uid = ? AND source_table = ? AND deleted_at IS NULL", assetID, uid, "reference_upload").
			Updates(map[string]interface{}{
				"status":     model.GlobalAssetStatusDeleted,
				"deleted_at": now,
			}).Error; err != nil {
			return fmt.Errorf("internal:failed to delete reference upload")
		}
		if err := assetLedgerService.New().DeleteReferenceUploadByGlobalAssetIDWithDB(tx, uid, assetID); err != nil {
			return fmt.Errorf("internal:failed to delete asset ledger")
		}
		return nil
	})
}

func deleteDashboardThreadFile(uid int, fileID uint) error {
	db := globals.GraDBs["system"]
	var threadFile workagentModel.ThreadFile
	if err := db.Where("id = ? AND uid = ?", fileID, uid).First(&threadFile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("not_found:file not found")
		}
		return fmt.Errorf("internal:failed to load file")
	}

	filePath := workagentApi.ResolveWorkspaceFilePath(threadFile.FilePath)
	if err := db.Delete(&threadFile).Error; err != nil {
		return fmt.Errorf("internal:failed to delete file")
	}
	if err := assetLedgerService.New().DeleteThreadByIDWithDB(db, uid, mapDashboardThreadLedgerSource(threadFile.FileSource), threadFile.Id); err != nil {
		return fmt.Errorf("internal:failed to delete file ledger")
	}
	if filePath != "" {
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			globals.Warn(fmt.Sprintf("[Dashboard] Failed to remove thread file %s: %v", filePath, err))
		}
	}
	if err := updateDashboardThreadStatistics(threadFile.ThreadID); err != nil {
		globals.Warn(fmt.Sprintf("[Dashboard] Failed to update thread %d stats after file delete: %v", threadFile.ThreadID, err))
	}
	return nil
}

func updateDashboardThreadStatistics(threadID uint) error {
	var fileCount int64
	if err := globals.GraDBs["system"].Table("w_workagent_thread_file").Where("thread_id = ?", threadID).Count(&fileCount).Error; err != nil {
		return err
	}
	return globals.GraDBs["system"].Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Updates(map[string]interface{}{
			"file_count": fileCount,
			"updated_at": time.Now(),
		}).Error
}

func respondDashboardStorageDeleteError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "bad_request:"):
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": strings.TrimPrefix(message, "bad_request:")})
	case strings.HasPrefix(message, "not_found:"):
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": strings.TrimPrefix(message, "not_found:")})
	case strings.HasPrefix(message, "conflict:"):
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": strings.TrimPrefix(message, "conflict:")})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": strings.TrimPrefix(message, "internal:")})
	}
}

func deleteDashboardSpaceStorageGlobalAssetBinary(c *gin.Context, asset *model.GlobalAsset) error {
	if asset == nil {
		return nil
	}

	if objectKey := extractStringFromJSONMap(asset.Metadata, "objectKey"); objectKey != "" {
		provider := extractStringFromJSONMap(asset.Metadata, "storageProvider")
		bucket := extractStringFromJSONMap(asset.Metadata, "bucket")
		store, ok, storeErr := storageService.NewObjectStoreForProviderBucket(globals.GraConf.Generator.Storage, provider, bucket)
		if storeErr != nil {
			return fmt.Errorf("internal:failed to initialize asset storage")
		}
		if ok && store != nil {
			if err := store.Delete(c.Request.Context(), objectKey); err != nil {
				return fmt.Errorf("internal:failed to delete stored asset")
			}
		}
		return nil
	}

	if err := deleteLocalDashboardSpaceAssetFile(asset.URL); err != nil {
		return fmt.Errorf("internal:failed to delete local asset file")
	}
	if asset.ThumbURL != "" && asset.ThumbURL != asset.URL {
		if err := deleteLocalDashboardSpaceAssetFile(asset.ThumbURL); err != nil {
			return fmt.Errorf("internal:failed to delete local asset thumbnail")
		}
	}
	return nil
}

func extractStringFromJSONMap(data model.JSONMap, key string) string {
	if data == nil {
		return ""
	}
	raw, ok := data[key]
	if !ok || raw == nil {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func deleteLocalDashboardSpaceAssetFile(fileURL string) error {
	normalized := strings.TrimSpace(fileURL)
	if normalized == "" || !strings.HasPrefix(normalized, "/uploads/") {
		return nil
	}

	localPath := filepath.Clean(strings.TrimPrefix(normalized, "/"))
	if localPath == "." || localPath == "" || !strings.HasPrefix(localPath, "uploads"+string(filepath.Separator)) {
		return nil
	}

	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func mapDashboardThreadLedgerSource(source workagentModel.FileSource) string {
	if source == workagentModel.FileSourceOutput {
		return "thread_output"
	}
	return "thread_upload"
}
