// Canvas project service (§13 M1-W1-01). The handler layer under
// server/api/pro/tools reaches in here to do the heavy lifting —
// CRUD + document patching — so canvas_api.go stays a thin mapping
// layer that only knows HTTP shape.
//
// Split in two layers so tests don't need a live DB:
//
//   Pure functions (BuildInitialCanvasDocument, ApplyElementPatches,
//   NormalizeProjectTitle, ElementCountFromDocument)
//       Unit-testable without touching MySQL. They encode the canvas
//       document contract and exist behind the exported functions as
//       the one source of truth.
//
//   DB-coupled functions (CreateProject, ListProjects, GetProject,
//   GetProjectByUUID, UpdateProject, PatchElements, DeleteProject)
//       Wrap the pure layer inside a GORM transaction. The handler
//       receives sentinel errors (ErrCanvasProjectNotFound etc.) and
//       classifies them into HTTP responses.

package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"server/model"
	workagentModel "server/model/workagent"
	assetLedgerService "server/service/assetledger"
	globalAssetService "server/service/globalasset"
	projectService "server/service/project"
	"server/utils"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sentinel errors — handlers map these to HTTP responses. Leak anything
// else directly into the error message (caller prefixed) so ops can
// still see the raw GORM failure in logs.
var (
	ErrCanvasProjectNotFound   = errors.New("canvas: project not found")
	ErrCanvasProjectNoVersions = errors.New("canvas: project has no versions")
	ErrCanvasVersionReadOnly   = errors.New("canvas: version is read-only")
	ErrCanvasInvalidVersion    = errors.New("canvas: invalid version")
	ErrCanvasElementsDocument  = errors.New("canvas: document does not contain a valid elements list")
	ErrCanvasTitleRequired     = errors.New("canvas: title cannot be empty")
	ErrCanvasProjectPrivate    = errors.New("canvas: project is private")
)

const (
	CanvasVisibilityPrivate  int8 = 0
	CanvasVisibilityUnlisted int8 = 1
	CanvasVisibilityPublic   int8 = 2
)

func validateProjectVisibility(visibility int8) error {
	switch visibility {
	case CanvasVisibilityPrivate, CanvasVisibilityUnlisted, CanvasVisibilityPublic:
		return nil
	default:
		return errors.New("canvas: invalid visibility")
	}
}

func isPublishedCanvasVisibility(visibility int8) bool {
	return visibility == CanvasVisibilityUnlisted || visibility == CanvasVisibilityPublic
}

func upsertCanvasShareSnapshot(tx *gorm.DB, project model.CanvasProject, thumbnailURL string) error {
	doc, err := NormalizeCanvasDocumentForStorage(project.Document)
	if err != nil {
		return err
	}
	snapshot := model.CanvasShareSnapshot{
		ProjectID:        project.Id,
		PublishedVersion: project.LatestVersion,
		ThumbnailURL:     thumbnailURL,
		Document:         doc,
		SchemaVersion:    schemaVersionForStorage(doc),
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"published_version",
			"thumbnail_url",
			"document",
			"schema_version",
			"updated_at",
		}),
	}).Create(&snapshot).Error
}

func applyShareSnapshotForRead(ctx context.Context, db *gorm.DB, project model.CanvasProject) (model.CanvasProject, bool) {
	var snapshot model.CanvasShareSnapshot
	if err := db.WithContext(ctx).
		Where("project_id = ?", project.Id).
		First(&snapshot).Error; err != nil {
		return project, false
	}
	if len(snapshot.Document) > 0 {
		project.Document = snapshot.Document
		project.SchemaVersion = snapshot.SchemaVersion
		project.ElementCount = ElementCountFromDocument(snapshot.Document)
	}
	if snapshot.PublishedVersion > 0 {
		project.LatestVersion = snapshot.PublishedVersion
	}
	if strings.TrimSpace(snapshot.ThumbnailURL) != "" {
		project.ThumbnailURL = snapshot.ThumbnailURL
	}
	project.UpdatedAt = snapshot.UpdatedAt
	return project, true
}

func attachShareSnapshotMetadata(ctx context.Context, db *gorm.DB, project model.CanvasProject) model.CanvasProject {
	var snapshot model.CanvasShareSnapshot
	if err := db.WithContext(ctx).
		Where("project_id = ?", project.Id).
		First(&snapshot).Error; err != nil {
		return project
	}
	publishedAt := snapshot.UpdatedAt
	project.PublishedAt = &publishedAt
	project.PublishedVersion = snapshot.PublishedVersion
	project.HasUnpublishedChanges = project.LatestVersion != snapshot.PublishedVersion || project.UpdatedAt.After(snapshot.UpdatedAt)
	return project
}

// CreateProjectInput is the service-layer DTO for project creation.
// Mirrors canvas_api.go createCanvasProjectRequest without the JSON tags.
type CreateProjectInput struct {
	Title             string
	Visibility        *int8
	ThumbnailURL      string
	Document          model.JSONMap
	SourceProjectUUID string
	// Stage 1.5: optional system-kind tag. 0 (the zero value + the
	// default for every canvas-API path) means user-created. The
	// workagent EnsurePersonalWorkspace path passes
	// model.GlobalProjectKindPersonalWorkspace to mark the row as
	// the user's protected default workspace. The HTTP-level
	// createCanvasProjectRequest deliberately does NOT expose this
	// — clients can't promote their own row to a system kind.
	SystemKind int8
}

// ListProjectsInput is the pagination envelope. Page/Limit are
// normalised inside ListProjects so callers can pass raw query values.
type ListProjectsInput struct {
	Page             int
	Limit            int
	IncludeThumbnail bool
	SkipTotal        bool
}

// UpdateProjectInput is the patch envelope. Nil pointers are left
// untouched; UpdateProject returns the current row when every field
// is nil so the handler stays unconditional.
type UpdateProjectInput struct {
	Title        *string
	Visibility   *int8
	ThumbnailURL *string
}

// PatchElementsInput carries the optimistic version gate + the raw
// element patches. A nil Version means "whatever the latest version
// currently is" — the handler's default. ExpectedProjectUpdatedAt is
// the etag-style sub-version revision token: when set, the patch
// rejects with ErrCanvasVersionConflict if the project row's
// updated_at has advanced past it (multi-tab last-writer-wins guard).
// Mirrors UpdateCanvasVersion.ExpectedProjectUpdatedAt so a client
// caching the same token can use either endpoint with the same gate.
type PatchElementsInput struct {
	Version                  *int
	ExpectedProjectUpdatedAt *time.Time
	Patches                  []map[string]interface{}
}

// PatchElementsResult is the service's summary of the patch operation.
// Patched=false means the patch was accepted but was a no-op (every
// field in every element matched the stored value).
type PatchElementsResult struct {
	Patched bool
	Version int
}

// NormalizeProjectTitle trims whitespace and falls back to "Untitled".
// Extracted so tests can pin the default without poking the handler.
func NormalizeProjectTitle(raw string) string {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "Untitled"
	}
	return title
}

// BuildInitialCanvasDocument returns the JSON shape every new project
// starts with. Kept as a function (not a var) because model.JSONMap
// values are mutable and we don't want tests sharing a live map.
func BuildInitialCanvasDocument() model.JSONMap {
	return model.JSONMap{
		"schemaVersion": SchemaVersionV2,
		"viewMode":      string(ViewModeFreeform),
		"elements":      []interface{}{},
		"viewport": map[string]interface{}{
			"x":     0,
			"y":     0,
			"scale": 1,
		},
		"selectedIds": []string{},
	}
}

func materializeInitialDocumentShots(ctx context.Context, tx *gorm.DB, uid int, projectID uint, doc model.JSONMap) (model.JSONMap, error) {
	rawShots, ok := doc["shots"]
	if !ok || rawShots == nil {
		return doc, nil
	}
	payload, err := json.Marshal(rawShots)
	if err != nil {
		return model.JSONMap{}, err
	}
	var shots []ShotLink
	if err := json.Unmarshal(payload, &shots); err != nil {
		return model.JSONMap{}, err
	}
	if len(shots) == 0 {
		return doc, nil
	}
	plan, err := SyncShots(ctx, tx, uid, projectID, shots)
	if err != nil {
		return model.JSONMap{}, err
	}
	idsByLocalCardID := make(map[string]int, len(plan.Entries))
	for _, entry := range plan.Entries {
		if entry.LocalCardID != "" && entry.ShotID > 0 {
			idsByLocalCardID[entry.LocalCardID] = int(entry.ShotID)
		}
	}
	for i := range shots {
		if shotID, ok := idsByLocalCardID[shots[i].LocalCardID]; ok {
			shots[i].ShotID = shotID
		}
	}
	doc["shots"] = shots
	return doc, nil
}

func collectDocumentStringValues(value interface{}, out map[string]struct{}) {
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			out[trimmed] = struct{}{}
		}
	case []interface{}:
		for _, item := range v {
			collectDocumentStringValues(item, out)
		}
	case []map[string]interface{}:
		for _, item := range v {
			collectDocumentStringValues(item, out)
		}
	case map[string]interface{}:
		for _, item := range v {
			collectDocumentStringValues(item, out)
		}
	case model.JSONMap:
		for _, item := range v {
			collectDocumentStringValues(item, out)
		}
	}
}

func copySharedProjectAssetsForDocument(ctx context.Context, tx *gorm.DB, uid int, targetProject model.CanvasProject, sourceProjectUUID string, doc model.JSONMap) error {
	sourceProjectUUID = strings.TrimSpace(sourceProjectUUID)
	if sourceProjectUUID == "" || len(doc) == 0 {
		return nil
	}
	targetProjectID := targetProject.Id
	if targetProjectID == 0 {
		return nil
	}

	var sourceProject model.CanvasProject
	if err := tx.WithContext(ctx).
		Select("id", "uid", "uuid", "visibility").
		Where("uuid = ? AND deleted_at IS NULL", sourceProjectUUID).
		First(&sourceProject).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCanvasProjectNotFound
		}
		return err
	}
	if !isPublishedCanvasVisibility(sourceProject.Visibility) {
		return ErrCanvasProjectPrivate
	}

	values := make(map[string]struct{})
	collectDocumentStringValues(doc, values)
	if len(values) == 0 {
		return nil
	}
	candidates := make([]string, 0, len(values)*2)
	for value := range values {
		candidates = append(candidates, value)
		if pathValue := canvasAssetURLPath(value); pathValue != "" && pathValue != value {
			candidates = append(candidates, pathValue)
		}
	}

	var sourceAssets []model.GlobalAsset
	if err := tx.WithContext(ctx).
		Where("project_id = ? AND deleted_at IS NULL AND status <> ?", sourceProject.Id, model.GlobalAssetStatusDeleted).
		Where("(url IN ? OR thumb_url IN ?)", candidates, candidates).
		Find(&sourceAssets).Error; err != nil {
		return err
	}
	copiedURLs := make(map[string]struct{}, len(sourceAssets))
	for _, sourceAsset := range sourceAssets {
		var existing model.GlobalAsset
		if err := tx.WithContext(ctx).
			Where("project_id = ? AND url = ? AND deleted_at IS NULL AND status <> ?", targetProjectID, sourceAsset.URL, model.GlobalAssetStatusDeleted).
			First(&existing).Error; err == nil {
			if err := assetLedgerService.New().UpsertCanvasProjectFileWithDB(tx, &existing, &targetProject); err != nil {
				return err
			}
			copiedURLs[sourceAsset.URL] = struct{}{}
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		metadata := cloneCanvasJSONMap(sourceAsset.Metadata)
		if metadata == nil {
			metadata = model.JSONMap{}
		}
		metadata["copiedFromProjectUuid"] = sourceProjectUUID
		metadata["copiedFromGlobalAssetId"] = sourceAsset.Id
		global, err := globalAssetService.NewRepository(tx).CreateCanvasProjectFile(globalAssetService.CanvasProjectFileInput{
			UID:           uid,
			ProjectID:     targetProjectID,
			SourceItemKey: canvasProjectFileSourceKey("shared-copy", targetProjectID, fmt.Sprintf("%d:%s", sourceAsset.Id, sourceAsset.URL)),
			URL:           sourceAsset.URL,
			ThumbURL:      sourceAsset.ThumbURL,
			MimeType:      sourceAsset.MimeType,
			SizeBytes:     sourceAsset.SizeBytes,
			Width:         sourceAsset.Width,
			Height:        sourceAsset.Height,
			Kind:          sourceAsset.Kind,
			VariantType:   model.GlobalAssetVariantOriginal,
			Metadata:      metadata,
		})
		if err != nil {
			return err
		}
		if global != nil {
			if err := assetLedgerService.New().UpsertCanvasProjectFileWithDB(tx, global, &targetProject); err != nil {
				return err
			}
			copiedURLs[global.URL] = struct{}{}
		}
		copiedURLs[sourceAsset.URL] = struct{}{}
	}

	for value := range values {
		if _, ok := copiedURLs[value]; ok {
			continue
		}
		normalizedURL, err := NormalizeCanvasReferenceURL(value, false)
		if err != nil || normalizedURL == "" {
			continue
		}
		pathValue := canvasAssetURLPath(normalizedURL)
		if !isSharedProjectDocumentAssetPath(pathValue, sourceProject.UID, sourceProject.UUID) {
			continue
		}
		var existing model.GlobalAsset
		if err := tx.WithContext(ctx).
			Where("project_id = ? AND url = ? AND deleted_at IS NULL AND status <> ?", targetProjectID, normalizedURL, model.GlobalAssetStatusDeleted).
			First(&existing).Error; err == nil {
			if err := assetLedgerService.New().UpsertCanvasProjectFileWithDB(tx, &existing, &targetProject); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		metadata := model.JSONMap{
			"copiedFromProjectUuid": sourceProjectUUID,
			"copiedWithoutAssetRow": true,
		}
		global, err := globalAssetService.NewRepository(tx).CreateCanvasProjectFile(globalAssetService.CanvasProjectFileInput{
			UID:           uid,
			ProjectID:     targetProjectID,
			SourceItemKey: canvasProjectFileSourceKey("shared-copy-url", targetProjectID, normalizedURL),
			URL:           normalizedURL,
			Kind:          "shared-copy",
			VariantType:   model.GlobalAssetVariantOriginal,
			Metadata:      metadata,
		})
		if err != nil {
			return err
		}
		if global != nil {
			if err := assetLedgerService.New().UpsertCanvasProjectFileWithDB(tx, global, &targetProject); err != nil {
				return err
			}
			copiedURLs[global.URL] = struct{}{}
		}
	}
	return nil
}

func isSharedProjectDocumentAssetPath(pathValue string, sourceUID int, sourceProjectUUID string) bool {
	if pathValue == "" || sourceUID <= 0 || sourceProjectUUID == "" {
		return false
	}
	canvasPrefix := canvasUploadURLPrefix(sourceUID, sourceProjectUUID)
	canvasCompatPrefix := canvasUploadLegacyURLPrefix(sourceUID, sourceProjectUUID)
	generationPrefix := fmt.Sprintf("/uploads/generations/uid/%d/", sourceUID)
	generationCompatPrefix := fmt.Sprintf("/generations/uid/%d/", sourceUID)
	return strings.HasPrefix(pathValue, canvasPrefix) ||
		strings.HasPrefix(pathValue, canvasCompatPrefix) ||
		strings.HasPrefix(pathValue, generationPrefix) ||
		strings.HasPrefix(pathValue, generationCompatPrefix)
}

// schemaVersionForStorage extracts the schemaVersion key for writing to
// the w_global_project.schema_version column. Falls back to 1 for legacy
// docs missing the key — mirrors EnsureAtVersion's "legacy = v1" rule
// (document_migrator.go) so the project row's schema_version column never
// disagrees with what EnsureSchemaVersion would stamp on the doc.
func schemaVersionForStorage(doc model.JSONMap) int {
	v, err := readSchemaVersion(doc)
	if err != nil || v == 0 {
		return 1
	}
	return v
}

// ElementCountFromDocument extracts the element count from a canvas
// document. Returns 0 for missing / malformed shapes — the caller is
// responsible for rejecting such documents upstream if it cares.
func ElementCountFromDocument(doc model.JSONMap) int {
	raw, ok := doc["elements"]
	if !ok {
		return 0
	}
	if arr, ok := raw.([]interface{}); ok {
		return len(arr)
	}
	if m, ok := raw.([]map[string]interface{}); ok {
		return len(m)
	}
	return 0
}

// ApplyElementPatches applies partial element updates keyed by `id`.
// Never mutates element IDs. Returns the next document, whether any
// meaningful change occurred, and an error when the document itself
// is malformed (so the caller can fail loudly instead of silently
// accepting garbage).
func ApplyElementPatches(doc model.JSONMap, patches []map[string]interface{}) (model.JSONMap, bool, error) {
	rawElements, ok := doc["elements"]
	if !ok {
		return nil, false, ErrCanvasElementsDocument
	}

	var elementsList []interface{}
	switch v := rawElements.(type) {
	case []interface{}:
		elementsList = append([]interface{}(nil), v...)
	case []map[string]interface{}:
		elementsList = make([]interface{}, 0, len(v))
		for _, item := range v {
			elementsList = append(elementsList, item)
		}
	default:
		return nil, false, ErrCanvasElementsDocument
	}

	updatesByID := make(map[string]map[string]interface{}, len(patches))
	for _, patch := range patches {
		idStr, _ := patch["id"].(string)
		if idStr == "" {
			continue
		}
		updatesByID[idStr] = patch
	}
	if len(updatesByID) == 0 {
		return doc, false, nil
	}

	changed := false
	for i, rawEl := range elementsList {
		elMap, ok := rawEl.(map[string]interface{})
		if !ok {
			continue
		}
		idStr, _ := elMap["id"].(string)
		update, exists := updatesByID[idStr]
		if !exists {
			continue
		}
		nextEl := make(map[string]interface{}, len(elMap))
		for k, v := range elMap {
			nextEl[k] = v
		}
		elementChanged := false
		for k, v := range update {
			if k == "id" {
				continue
			}
			if reflect.DeepEqual(nextEl[k], v) {
				continue
			}
			nextEl[k] = v
			elementChanged = true
		}
		if elementChanged {
			elementsList[i] = nextEl
			changed = true
		}
	}
	if !changed {
		return doc, false, nil
	}

	nextDoc := make(model.JSONMap, len(doc))
	for k, v := range doc {
		nextDoc[k] = v
	}
	nextDoc["elements"] = elementsList
	return nextDoc, true, nil
}

// CreateProject inserts a project + its initial version + syncs the
// asset ledger thumbnail, all inside a single transaction. Returns
// the persisted project with LatestVersion populated.
func CreateProject(ctx context.Context, db *gorm.DB, uid int, in CreateProjectInput) (model.CanvasProject, error) {
	if db == nil {
		return model.CanvasProject{}, errors.New("canvas: CreateProject requires a non-nil db")
	}
	if uid <= 0 {
		return model.CanvasProject{}, errors.New("canvas: CreateProject requires a positive uid")
	}

	visibility := int8(0)
	if in.Visibility != nil {
		visibility = *in.Visibility
	}
	if err := validateProjectVisibility(visibility); err != nil {
		return model.CanvasProject{}, err
	}
	thumbnailURL, err := NormalizeCanvasReferenceURL(in.ThumbnailURL, false)
	if err != nil {
		return model.CanvasProject{}, err
	}

	initialDoc := BuildInitialCanvasDocument()
	if in.Document != nil {
		normalizedDoc, err := NormalizeCanvasDocumentForStorage(in.Document)
		if err != nil {
			return model.CanvasProject{}, err
		}
		initialDoc = normalizedDoc
	}
	elementCount := ElementCountFromDocument(initialDoc)
	project := model.CanvasProject{
		UID:          uid,
		SystemKind:   in.SystemKind,
		UUID:         utils.GenerateUUID(),
		Title:        NormalizeProjectTitle(in.Title),
		Visibility:   visibility,
		ThumbnailURL: thumbnailURL,
		// M5-B-06 Stage 2 dual-write: live document on the project row.
		// M5-B-06 retire-step: init also writes a snapshot_no=1 row
		// (see below) — reverses Stage 2 decision #3 so the version-
		// history dialog can read every snapshot uniformly.
		Document:      initialDoc,
		SchemaVersion: SchemaVersionV2,
		ElementCount:  elementCount,
	}

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&project).Error; err != nil {
			return err
		}
		if err := projectService.NewRepository(tx).UpsertOwnerMember(project.Id, uid); err != nil {
			return err
		}
		if in.Document != nil {
			materializedDoc, err := materializeInitialDocumentShots(ctx, tx, uid, project.Id, initialDoc)
			if err != nil {
				return err
			}
			initialDoc = materializedDoc
			project.Document = materializedDoc
			elementCount = ElementCountFromDocument(materializedDoc)
			project.ElementCount = elementCount
			if err := copySharedProjectAssetsForDocument(ctx, tx, uid, project, in.SourceProjectUUID, materializedDoc); err != nil {
				return err
			}
		}

		// M5-B-06 retire-T3: the legacy version-row table (dropped by
		// migration 20260534) is no longer written. The init snapshot
		// below + project.document are the canonical state. retire-T2
		// already cut every reader off the version table.
		initSnapshot := model.CanvasSnapshot{
			ProjectID:     project.Id,
			SnapshotNo:    1,
			CreatedBy:     uid,
			Source:        "init",
			Message:       "Initial version",
			ElementCount:  elementCount,
			ThumbnailURL:  thumbnailURL,
			Document:      initialDoc,
			SchemaVersion: SchemaVersionV2,
		}
		if err := tx.Create(&initSnapshot).Error; err != nil {
			return err
		}

		project.LatestVersion = 1
		if err := tx.Model(&model.CanvasProject{}).
			Where("id = ? AND deleted_at IS NULL", project.Id).
			Updates(map[string]interface{}{
				"latest_version": 1,
				"document":       initialDoc,
				"element_count":  elementCount,
				"schema_version": SchemaVersionV2,
			}).Error; err != nil {
			return err
		}
		project.LatestVersion = 1
		if isPublishedCanvasVisibility(visibility) {
			if err := upsertCanvasShareSnapshot(tx, project, thumbnailURL); err != nil {
				return err
			}
		}
		return assetLedgerService.New().SyncCanvasProjectThumbnailWithDB(tx, &project)
	})
	if err != nil {
		return model.CanvasProject{}, err
	}
	return project, nil
}

// ListProjects paginates project rows for a user. Thumbnail is omitted
// by default because the column is a longtext that dwarfs every other
// field; includeThumbnail=true is reserved for the editor's open-modal.
func ListProjects(ctx context.Context, db *gorm.DB, uid int, in ListProjectsInput) (model.PaginatedResult[model.CanvasProject], error) {
	page := in.Page
	limit := in.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := db.WithContext(ctx).Model(&model.CanvasProject{}).
		Where("deleted_at IS NULL").
		Where(
			"uid = ? OR id IN (?)",
			uid,
			db.WithContext(ctx).
				Model(&model.GlobalProjectMember{}).
				Select("project_id").
				Where("uid = ? AND deleted_at IS NULL", uid),
		)

	var items []model.CanvasProject
	listQuery := query
	if !in.IncludeThumbnail {
		listQuery = listQuery.Select("id", "created_at", "updated_at", "uid", "uuid", "title", "visibility", "latest_version")
	}
	fetchLimit := limit
	if in.SkipTotal {
		fetchLimit = limit + 1
	}
	if err := listQuery.Order("updated_at DESC").
		Offset((page - 1) * limit).
		Limit(fetchLimit).
		Find(&items).Error; err != nil {
		return model.PaginatedResult[model.CanvasProject]{}, err
	}
	hasMore := false
	if in.SkipTotal && len(items) > limit {
		hasMore = true
		items = items[:limit]
	}
	total := int64((page-1)*limit + len(items))
	if !in.SkipTotal {
		if err := query.Count(&total).Error; err != nil {
			return model.PaginatedResult[model.CanvasProject]{}, err
		}
	} else if hasMore {
		total++
	}

	return model.PaginatedResult[model.CanvasProject]{
		Items: items,
		Total: total,
		Page:  page,
		Limit: limit,
	}, nil
}

// GetProject fetches one project by numeric id scoped to uid. Returns
// ErrCanvasProjectNotFound when the row is absent so the handler can
// pick a distinct HTTP shape.
func GetProject(ctx context.Context, db *gorm.DB, uid int, id uint) (model.CanvasProject, error) {
	access, err := projectService.NewRepository(db.WithContext(ctx)).ResolveAccess(id, uint(uid))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	if err != nil {
		return model.CanvasProject{}, err
	}
	if !access.CanView() {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	normalizedProject, normalizeErr := normalizeProjectDocumentForRead(*access.Project)
	if normalizeErr != nil {
		return model.CanvasProject{}, normalizeErr
	}
	return attachShareSnapshotMetadata(ctx, db, normalizedProject), nil
}

// GetProjectByUUID is GetProject's UUID-keyed sibling — used for
// shared-link resolution where the numeric id isn't in the URL.
func GetProjectByUUID(ctx context.Context, db *gorm.DB, uid int, uuid string) (model.CanvasProject, error) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	var project model.CanvasProject
	err := db.WithContext(ctx).
		Where("uuid = ? AND deleted_at IS NULL", uuid).
		First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	if err != nil {
		return model.CanvasProject{}, err
	}
	access, err := projectService.NewRepository(db.WithContext(ctx)).ResolveAccess(project.Id, uint(uid))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	if err != nil {
		return model.CanvasProject{}, err
	}
	if !access.CanView() {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	normalizedProject, normalizeErr := normalizeProjectDocumentForRead(project)
	if normalizeErr != nil {
		return model.CanvasProject{}, normalizeErr
	}
	return attachShareSnapshotMetadata(ctx, db, normalizedProject), nil
}

// GetSharedProjectByUUID resolves a share link for unlisted/public
// projects. Private projects are intentionally only available through
// owner-scoped GetProjectByUUID.
func GetSharedProjectByUUID(ctx context.Context, db *gorm.DB, uuid string) (model.CanvasProject, error) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	var project model.CanvasProject
	err := db.WithContext(ctx).
		Where("uuid = ? AND visibility IN ? AND deleted_at IS NULL", uuid, []int8{CanvasVisibilityUnlisted, CanvasVisibilityPublic}).
		First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	if err != nil {
		return model.CanvasProject{}, err
	}
	var ok bool
	project, ok = applyShareSnapshotForRead(ctx, db, project)
	if !ok {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}
	return normalizeProjectDocumentForRead(project)
}

// UpdateProject applies partial updates and returns the fresh row.
// A nil-everywhere input is treated as a read (preserving the handler's
// original behaviour — the page posts patches even when the user only
// moved focus). Validation: title cannot be explicitly set to empty.
func UpdateProject(ctx context.Context, db *gorm.DB, uid int, id uint, in UpdateProjectInput) (model.CanvasProject, error) {
	updates := map[string]interface{}{}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return model.CanvasProject{}, ErrCanvasTitleRequired
		}
		updates["title"] = title
	}
	if in.Visibility != nil {
		if err := validateProjectVisibility(*in.Visibility); err != nil {
			return model.CanvasProject{}, err
		}
		updates["visibility"] = *in.Visibility
	}
	if in.ThumbnailURL != nil {
		thumbnailURL, err := NormalizeCanvasReferenceURL(*in.ThumbnailURL, false)
		if err != nil {
			return model.CanvasProject{}, err
		}
		updates["thumbnail_url"] = thumbnailURL
	}

	if len(updates) == 0 {
		return GetProject(ctx, db, uid, id)
	}
	if err := requireProjectMutationAccess(ctx, db, uid, id, in.Visibility != nil); err != nil {
		return model.CanvasProject{}, err
	}

	if in.ThumbnailURL != nil {
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var project model.CanvasProject
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND deleted_at IS NULL", id).
				First(&project).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrCanvasProjectNotFound
				}
				return err
			}
			if err := tx.Model(&model.CanvasProject{}).
				Where("id = ? AND deleted_at IS NULL", id).
				Updates(updates).Error; err != nil {
				return err
			}
			// Settings saves update the live draft metadata only. Public
			// snapshots are refreshed exclusively by PublishProject so title /
			// thumbnail edits cannot accidentally publish an in-progress draft.
			return nil
		})
		if err != nil {
			return model.CanvasProject{}, err
		}
		var project model.CanvasProject
		if err := db.WithContext(ctx).
			Where("id = ? AND deleted_at IS NULL", id).
			First(&project).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.CanvasProject{}, ErrCanvasProjectNotFound
			}
			return model.CanvasProject{}, err
		}
		if err := assetLedgerService.New().SyncCanvasProjectThumbnailWithDB(db, &project); err != nil {
			return model.CanvasProject{}, err
		}
		project, err = normalizeProjectDocumentForRead(project)
		if err != nil {
			return model.CanvasProject{}, err
		}
		return attachShareSnapshotMetadata(ctx, db, project), nil
	}

	res := db.WithContext(ctx).Model(&model.CanvasProject{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(updates)
	if res.Error != nil {
		return model.CanvasProject{}, res.Error
	}
	if res.RowsAffected == 0 {
		return model.CanvasProject{}, ErrCanvasProjectNotFound
	}

	// Mirror the ThumbnailURL branch above (lines 848-855): the post-update
	// read MUST surface its error. Swallowing it lets a zero-value
	// CanvasProject{id:0, uid:0, Document:nil} flow into the ledger sync
	// when a replica-lag / connection blip flakes the read, which then
	// returns garbage to the caller.
	var project model.CanvasProject
	if err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.CanvasProject{}, ErrCanvasProjectNotFound
		}
		return model.CanvasProject{}, err
	}
	if err := assetLedgerService.New().SyncCanvasProjectThumbnailWithDB(db, &project); err != nil {
		return model.CanvasProject{}, err
	}
	project, normalizeErr := normalizeProjectDocumentForRead(project)
	if normalizeErr != nil {
		return model.CanvasProject{}, normalizeErr
	}
	return attachShareSnapshotMetadata(ctx, db, project), nil
}

// PublishProject updates the immutable public/share snapshot from the current
// live draft. Autosave intentionally does not call this; owners must make the
// publish step explicit so shared links do not expose draft edits.
func PublishProject(ctx context.Context, db *gorm.DB, uid int, id uint) (model.CanvasProject, error) {
	var project model.CanvasProject
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireProjectManageAccess(ctx, tx, uid, id); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).
			First(&project).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCanvasProjectNotFound
			}
			return err
		}
		if !isPublishedCanvasVisibility(project.Visibility) {
			return ErrCanvasProjectPrivate
		}
		return upsertCanvasShareSnapshot(tx, project, project.ThumbnailURL)
	})
	if err != nil {
		return model.CanvasProject{}, err
	}
	return GetProject(ctx, db, uid, id)
}

// PatchElements does a row-locked partial update on the latest version
// of a project's canvas document. The lock keeps version numbers
// monotonic under concurrent patches. Returns Patched=false when the
// patch was a semantic no-op.
//
// Contract: PatchElements is a *sub-snapshot* operation. It updates
// `document`, `element_count`, and `updated_at` on the latest version
// row, but it MUST NOT touch `source` or `message`. Those fields
// describe the snapshot's intent (the user-initiated save action that
// produced the row); a low-level element patch is not a save action,
// and overwriting them breaks the version-history dialog's label
// resolution. Pinned by a regression test in
// canvas_project_service_db_test.go.
func PatchElements(ctx context.Context, db *gorm.DB, uid int, projectID uint, in PatchElementsInput) (PatchElementsResult, error) {
	if len(in.Patches) == 0 {
		return PatchElementsResult{Patched: false}, nil
	}

	var result PatchElementsResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireProjectEditAccess(ctx, tx, uid, projectID); err != nil {
			return err
		}
		var project model.CanvasProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", projectID).
			First(&project).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCanvasProjectNotFound
			}
			return err
		}

		if project.LatestVersion == 0 {
			return ErrCanvasProjectNoVersions
		}

		// M5-B-06 retire-T3: live state is on the project row. Validate
		// the optimistic-lock version number against project.LatestVersion;
		// patches always apply to the live document. The legacy version-
		// row table (dropped by 20260534) is no longer read or written here.
		if in.Version != nil {
			if *in.Version <= 0 {
				return ErrCanvasInvalidVersion
			}
			if *in.Version != project.LatestVersion {
				return ErrCanvasVersionReadOnly
			}
		}
		// Shared tolerance with UpdateCanvasVersion — see
		// optimisticLockTolerance in canvas_version_service.go for the
		// MySQL second-rounding rationale.
		if in.ExpectedProjectUpdatedAt != nil && project.UpdatedAt.After(in.ExpectedProjectUpdatedAt.UTC().Add(optimisticLockTolerance)) {
			return ErrCanvasVersionConflict
		}

		currentDoc, err := NormalizeCanvasDocumentForStorage(project.Document)
		if err != nil {
			return err
		}
		doc, changed, err := ApplyElementPatches(currentDoc, in.Patches)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		doc, err = NormalizeCanvasDocumentForStorage(doc)
		if err != nil {
			return err
		}

		// Project row is the only writer of live state post-retire.
		// Source/message live on the snapshot row and are not touched
		// by element patches (sub-snapshot operations).
		if err := tx.Model(&model.CanvasProject{}).
			Where("id = ? AND deleted_at IS NULL", projectID).
			Updates(map[string]interface{}{
				"updated_at":     time.Now(),
				"document":       doc,
				"schema_version": schemaVersionForStorage(doc),
				"element_count":  ElementCountFromDocument(doc),
			}).Error; err != nil {
			return err
		}
		result = PatchElementsResult{
			Patched: true,
			Version: project.LatestVersion,
		}
		return nil
	})
	if err != nil {
		return PatchElementsResult{}, err
	}
	return result, nil
}

func normalizeProjectDocumentForRead(project model.CanvasProject) (model.CanvasProject, error) {
	doc, err := NormalizeCanvasDocumentForStorage(project.Document)
	if err != nil {
		return model.CanvasProject{}, err
	}
	project.Document = doc
	project.SchemaVersion = schemaVersionForStorage(doc)
	project.ElementCount = ElementCountFromDocument(doc)
	return project, nil
}

func requireProjectMutationAccess(ctx context.Context, db *gorm.DB, uid int, projectID uint, manage bool) error {
	if manage {
		return requireProjectManageAccess(ctx, db, uid, projectID)
	}
	return requireProjectEditAccess(ctx, db, uid, projectID)
}

func requireProjectEditAccess(ctx context.Context, db *gorm.DB, uid int, projectID uint) error {
	access, err := projectService.NewRepository(db.WithContext(ctx)).ResolveAccess(projectID, uint(uid))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrCanvasProjectNotFound
	}
	if err != nil {
		return err
	}
	if !access.CanEdit() {
		return ErrCanvasProjectNotFound
	}
	return nil
}

func requireProjectManageAccess(ctx context.Context, db *gorm.DB, uid int, projectID uint) error {
	access, err := projectService.NewRepository(db.WithContext(ctx)).ResolveAccess(projectID, uint(uid))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrCanvasProjectNotFound
	}
	if err != nil {
		return err
	}
	if !access.CanManage() {
		return ErrCanvasProjectNotFound
	}
	return nil
}

// DeleteProjectOptions controls Stage 1.5 system-kind handling. The
// default zero value refuses to delete any project with system_kind
// > 0 (Personal Workspace and any future system kinds). Only the
// account-close cascade (PurgeUserCanvasData) sets AllowSystemKind=
// true — that path is the user explicitly tearing down their entire
// footprint, where keeping the workspace alive would be wrong.
type DeleteProjectOptions struct {
	AllowSystemKind bool
}

// ErrSystemKindProjectUndeletable is returned when a non-account-
// close caller tries to delete a project flagged with system_kind >
// 0. The HTTP layer maps this to a stable 409 + error code so the FE
// can render a localized "this workspace can't be deleted" message
// without parsing the error string.
var ErrSystemKindProjectUndeletable = errors.New("canvas: system-managed project cannot be deleted")

// DeleteProject soft-deletes the project row, its assets, and clears
// asset-ledger bindings — all inside one transaction so a partial
// failure leaves nothing orphaned. Snapshots are scoped through the
// project row's deleted_at, so they fall out of every read path
// without an explicit per-snapshot update.
//
// Stage 1.5: now refuses any project with system_kind > 0
// (Personal Workspace today). Use DeleteProjectWithOptions with
// AllowSystemKind=true when the caller is the account-close
// cascade.
func DeleteProject(ctx context.Context, db *gorm.DB, uid int, id uint) error {
	return DeleteProjectWithOptions(ctx, db, uid, id, DeleteProjectOptions{})
}

// DeleteProjectWithOptions is the underlying delete implementation
// that accepts caller-specified policy. The default-zero opts
// behave identically to the pre-Stage-1.5 DeleteProject signature
// — every existing call site is unaffected. Account-close uses
// this directly with AllowSystemKind=true.
func DeleteProjectWithOptions(ctx context.Context, db *gorm.DB, uid int, id uint, opts DeleteProjectOptions) error {
	now := time.Now()
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireProjectManageAccess(ctx, tx, uid, id); err != nil {
			return err
		}

		// Stage 1.5 guard: refuse system-managed projects unless
		// the caller explicitly opt-ed in. The probe runs after
		// the ownership check so a non-owner attacker can't even
		// learn whether a given id is system-managed — they hit
		// the access-deny path first and never see this error.
		if !opts.AllowSystemKind {
			var sysKind int8
			if err := tx.Model(&model.CanvasProject{}).
				Where("id = ? AND deleted_at IS NULL", id).
				Select("system_kind").
				Scan(&sysKind).Error; err != nil {
				return err
			}
			if sysKind != model.GlobalProjectKindUser {
				return ErrSystemKindProjectUndeletable
			}
		}
		res := tx.Model(&model.CanvasProject{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Update("deleted_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrCanvasProjectNotFound
		}

		if err := tx.Model(&model.GlobalAsset{}).
			Where("project_id = ? AND deleted_at IS NULL", id).
			Updates(map[string]interface{}{
				"status":     model.GlobalAssetStatusDeleted,
				"deleted_at": now,
			}).Error; err != nil {
			return err
		}

		// M5-B-06 cleanup: w_canvas_snapshot has no deleted_at column
		// (immutable + hard-delete-only by design). Cascade the project
		// delete by hard-deleting the snapshot rows in the same TX so a
		// partial failure leaves nothing orphaned.
		if err := tx.Where("project_id = ?", id).
			Delete(&model.CanvasSnapshot{}).Error; err != nil {
			return err
		}

		// w_canvas_share_snapshot is unique-keyed by project_id and has
		// no deleted_at column. Without this cascade the row would
		// linger forever and block re-creating a snapshot if the same
		// project_id ever got reused.
		if err := tx.Where("project_id = ?", id).
			Delete(&model.CanvasShareSnapshot{}).Error; err != nil {
			return err
		}

		// w_canvas_task_binding rows tie outstanding generation tasks
		// back to this project (canvas_task_binding.go) — without this
		// cascade, useCanvasTaskRecovery would rehydrate pollers
		// against the deleted project after a refresh. Hard-delete by
		// design (the binding rows have no deleted_at column).
		if err := tx.Where("project_id = ?", id).
			Delete(&model.CanvasTaskBinding{}).Error; err != nil {
			return err
		}

		var threadIDs []uint
		if err := tx.Model(&workagentModel.ChatThread{}).
			Where("project_id = ? AND agent_type = ?", id, "canvas").
			Pluck("id", &threadIDs).Error; err != nil {
			return err
		}
		if len(threadIDs) > 0 {
			var threadFileIDs []uint
			if err := tx.Model(&workagentModel.ThreadFile{}).
				Where("thread_id IN ?", threadIDs).
				Pluck("id", &threadFileIDs).Error; err != nil {
				return err
			}
			if len(threadFileIDs) > 0 {
				if err := tx.Model(&model.GlobalAsset{}).
					Where("source_table = ? AND source_id IN ? AND deleted_at IS NULL", workagentModel.ThreadFile{}.TableName(), threadFileIDs).
					Updates(map[string]interface{}{
						"status":     model.GlobalAssetStatusDeleted,
						"deleted_at": now,
					}).Error; err != nil {
					return err
				}
				if err := tx.Where("thread_id IN ?", threadIDs).
					Delete(&workagentModel.ThreadFile{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("source IN ? AND thread_id IN ?", []string{"thread_upload", "thread_output"}, threadIDs).
				Delete(&model.UserAssetLedger{}).Error; err != nil {
				return err
			}
			if err := tx.Where("thread_id IN ?", threadIDs).
				Delete(&workagentModel.ChatMessage{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ? AND project_id = ? AND agent_type = ?", threadIDs, id, "canvas").
				Delete(&workagentModel.ChatThread{}).Error; err != nil {
				return err
			}
		}

		if err := assetLedgerService.New().DeleteCanvasByProjectIDWithDB(tx, uid, id); err != nil {
			return err
		}
		return assetLedgerService.New().DeleteCanvasThumbnailsByProjectIDWithDB(tx, uid, id)
	})
	return err
}
