package globalasset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"strings"
	"time"

	"server/globals"
	"server/model"
	workagentModel "server/model/workagent"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	sourceTableCanvasProjectFile = "canvas_project_file"
	sourceTableGenerationObject  = "w_generation_object"
	sourceTableWorkAgentFile     = "w_workagent_thread_file"
	sourceTableReferenceUpload   = "reference_upload"
)

// SourceKind is the public-facing enum API callers pass to the
// LoadBySourceForAccess reader to resolve a tool-local id back
// to its w_global_asset row. The values are short, URL-safe
// stable strings — these become part of the FE-facing wire shape
// (e.g. `GET /global/assets/by-source?kind=workagent_file&id=42`)
// so the BE-internal table-name strings stay private. ResolveSource
// translates the public enum to the internal source_table value.
type SourceKind string

const (
	// SourceKindCanvasProjectFile resolves a canvas-attached file
	// (w_canvas_project_file row id) to its global_asset bridge.
	SourceKindCanvasProjectFile SourceKind = "canvas_project_file"

	// SourceKindGenerationObject resolves a w_generation_object
	// row id (image / video output from the generator path) to
	// its global_asset bridge.
	SourceKindGenerationObject SourceKind = "generation_object"

	// SourceKindWorkAgentFile resolves a w_workagent_thread_file
	// row id (workagent user upload or tool output) to its
	// global_asset bridge.
	SourceKindWorkAgentFile SourceKind = "workagent_file"

	// SourceKindReferenceUpload resolves a reference-upload (the
	// shared composer / @-mention image-upload path) to its
	// global_asset bridge.
	SourceKindReferenceUpload SourceKind = "reference_upload"
)

// resolveSourceTable maps the public SourceKind enum to the
// internal source_table column value. Unknown kinds return ""
// so callers can short-circuit on the boundary (the reader
// treats an empty source_table as "not found" instead of
// scanning every row).
func resolveSourceTable(kind SourceKind) string {
	switch kind {
	case SourceKindCanvasProjectFile:
		return sourceTableCanvasProjectFile
	case SourceKindGenerationObject:
		return sourceTableGenerationObject
	case SourceKindWorkAgentFile:
		return sourceTableWorkAgentFile
	case SourceKindReferenceUpload:
		return sourceTableReferenceUpload
	}
	return ""
}

// Repository owns the minimal global asset index. Source fact tables still own
// storage, cleanup, and task semantics; this repository only creates the
// cross-tool assetId bridge.
type Repository struct {
	db *gorm.DB
}

type ManagedUploadInput struct {
	UID         uint
	ProjectID   uint
	UploadID    string
	URL         string
	MimeType    string
	SizeBytes   int64
	ContentHash string
	Kind        string
}

type CanvasProjectFileInput struct {
	UID           int
	ProjectID     uint
	SourceItemKey string
	URL           string
	ThumbURL      string
	MimeType      string
	SizeBytes     int64
	Width         int
	Height        int
	ContentHash   string
	Kind          string
	VariantType   string
	Metadata      model.JSONMap
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func DefaultRepository() *Repository {
	return NewRepository(globals.GraDBs["system"])
}

func (r *Repository) LoadForOwner(assetID uint, uid int) (*model.GlobalAsset, error) {
	if r == nil || r.db == nil || assetID == 0 || uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var asset model.GlobalAsset
	if err := r.db.
		Where("id = ? AND uid = ? AND deleted_at IS NULL", assetID, uid).
		First(&asset).Error; err != nil {
		return nil, err
	}
	return &asset, nil
}

// LoadForAccess resolves an active asset for the current user. Direct owner
// access is accepted, and project-scoped assets are readable by explicit
// project members. Public/unlisted URL reads stay outside this helper because
// delivery/signing policy differs by surface.
func (r *Repository) LoadForAccess(assetID uint, uid int) (*model.GlobalAsset, error) {
	if r == nil || r.db == nil || assetID == 0 || uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var asset model.GlobalAsset
	err := r.accessQuery(uid).
		Where("w_global_asset.id = ?", assetID).
		First(&asset).Error
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

func (r *Repository) LoadByURLForAccess(rawURL string, uid int) (*model.GlobalAsset, error) {
	if r == nil || r.db == nil || uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	url := strings.TrimSpace(rawURL)
	if url == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var asset model.GlobalAsset
	err := r.accessQuery(uid).
		Where("w_global_asset.url = ? OR w_global_asset.thumb_url = ?", url, url).
		First(&asset).Error
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

// LoadBySourceForAccess resolves a tool-local source row id back
// to its w_global_asset bridge, gated by the same access policy
// as LoadForAccess (owner-direct OR project-member read).
//
// Use case: a FE caller already holds a tool-local id
// (thread_file.id from the conversation files list,
// generation_object.id from the gallery, canvas_project_file row
// id from a canvas project) and wants the global_asset_id +
// unified preview URL so cross-tool composition can route through
// one identifier. Pre-2026-05-16 this lookup required hitting
// each tool's own detail endpoint then reading the GlobalAssetID
// column from the returned row — two round-trips per asset.
//
// Returns gorm.ErrRecordNotFound for: unknown kind, zero sourceID,
// no matching bridge row, or cross-tenant access denial. The four
// failure modes return the SAME sentinel so an enumeration attacker
// can't distinguish "this asset doesn't exist" from "this asset
// exists but isn't yours". Same posture as the rest of the
// platform's *ForAccess methods.
func (r *Repository) LoadBySourceForAccess(uid int, kind SourceKind, sourceID uint) (*model.GlobalAsset, error) {
	if r == nil || r.db == nil || uid == 0 || sourceID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	sourceTable := resolveSourceTable(kind)
	if sourceTable == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var asset model.GlobalAsset
	err := r.accessQuery(uid).
		Where("w_global_asset.source_table = ? AND w_global_asset.source_id = ?", sourceTable, sourceID).
		Order("w_global_asset.id ASC").
		First(&asset).Error
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

func (r *Repository) ListForProject(uid int, projectID uint, limit, offset int) ([]model.GlobalAsset, error) {
	if r == nil || r.db == nil || uid == 0 || projectID == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.GlobalAsset
	if err := r.accessQuery(uid).
		Where("w_global_asset.project_id = ?", projectID).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list global assets for project: %w", err)
	}
	return rows, nil
}

func (r *Repository) accessQuery(uid int) *gorm.DB {
	memberProjectIDs := r.db.
		Model(&model.GlobalProjectMember{}).
		Select("project_id").
		Where("uid = ? AND deleted_at IS NULL AND role IN ?", uid, readableProjectRoles())

	return r.db.
		Model(&model.GlobalAsset{}).
		Where("w_global_asset.deleted_at IS NULL").
		Where("w_global_asset.status <> ?", model.GlobalAssetStatusDeleted).
		Where(
			"w_global_asset.uid = ? OR (w_global_asset.project_id IS NOT NULL AND w_global_asset.project_id IN (?))",
			uid,
			memberProjectIDs,
		)
}

func (r *Repository) CreateCanvasProjectFile(in CanvasProjectFileInput) (*model.GlobalAsset, error) {
	if r == nil || r.db == nil || in.UID == 0 || in.ProjectID == 0 || strings.TrimSpace(in.URL) == "" {
		return nil, nil
	}
	sourceItemKey := strings.TrimSpace(in.SourceItemKey)
	if sourceItemKey == "" {
		sourceItemKey = uuid.NewString()
	}
	variantType := strings.TrimSpace(in.VariantType)
	if variantType == "" {
		variantType = model.GlobalAssetVariantOriginal
	}
	projectID := in.ProjectID
	global := model.GlobalAsset{
		UID:           in.UID,
		ProjectID:     &projectID,
		UUID:          uuid.NewString(),
		Kind:          normalizeKind(in.MimeType, in.Kind),
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTableCanvasProjectFile,
		SourceID:      uint64(in.ProjectID),
		SourceItemKey: sourceItemKey,
		URL:           strings.TrimSpace(in.URL),
		ThumbURL:      strings.TrimSpace(in.ThumbURL),
		MimeType:      strings.TrimSpace(in.MimeType),
		SizeBytes:     in.SizeBytes,
		Width:         in.Width,
		Height:        in.Height,
		ContentHash:   normalizeContentHash(in.ContentHash),
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   variantType,
		Metadata:      cloneJSONMap(in.Metadata),
	}
	if global.ThumbURL == "" {
		global.ThumbURL = global.URL
	}
	if global.Metadata == nil {
		global.Metadata = model.JSONMap{}
	}
	global.Metadata["projectId"] = in.ProjectID
	global.Metadata["sourceItemKey"] = sourceItemKey
	return r.upsertBySource(&global)
}

func canvasProjectFileSourceKey(prefix string, projectID uint, value string) string {
	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" {
		trimmedPrefix = "item"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", trimmedPrefix, projectID, strings.TrimSpace(value))))
	return trimmedPrefix + ":" + hex.EncodeToString(sum[:])
}

func (r *Repository) CreateFromGenerationObject(object *model.GenerationObject) (*model.GlobalAsset, error) {
	if object == nil || object.Id == 0 || r == nil || r.db == nil {
		return nil, nil
	}
	global := model.GlobalAsset{
		UID:           object.UID,
		UUID:          uuid.NewString(),
		Kind:          normalizeKind(object.ContentType, object.AssetKind),
		Source:        model.GlobalAssetSourceGeneration,
		SourceTable:   sourceTableGenerationObject,
		SourceID:      uint64(object.Id),
		SourceItemKey: "object",
		URL:           strings.TrimSpace(object.PublicURL),
		ThumbURL:      "",
		MimeType:      strings.TrimSpace(object.ContentType),
		SizeBytes:     object.SizeBytes,
		Status:        globalAssetStatusFromGenerationObject(object.Status),
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   variantTypeFromAssetKind(object.AssetKind),
		Metadata: model.JSONMap{
			"taskId":    strings.TrimSpace(object.TaskID),
			"recordId":  object.RecordID,
			"toolId":    strings.TrimSpace(object.ToolID),
			"provider":  strings.TrimSpace(object.Provider),
			"bucket":    strings.TrimSpace(object.Bucket),
			"objectKey": strings.TrimSpace(object.ObjectKey),
			"sourceUrl": strings.TrimSpace(object.SourceURL),
			"etag":      strings.TrimSpace(object.ETag),
		},
	}
	row, err := r.upsertBySource(&global)
	if err != nil {
		return nil, err
	}
	if row != nil && row.Id != 0 && object.GlobalAssetID != row.Id {
		_ = r.db.Model(&model.GenerationObject{}).Where("id = ?", object.Id).Update("global_asset_id", row.Id).Error
	}
	return row, nil
}

func (r *Repository) CreateFromThreadFile(file *workagentModel.ThreadFile) (*model.GlobalAsset, error) {
	return r.CreateFromThreadFileForThread(file, nil)
}

func (r *Repository) MarkWorkAgentThreadFilesDeletedWithDB(db *gorm.DB, uid int, fileIDs []uint) error {
	if r == nil || db == nil || uid == 0 || len(fileIDs) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(fileIDs))
	for _, id := range fileIDs {
		if id != 0 {
			ids = append(ids, uint64(id))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return db.Model(&model.GlobalAsset{}).
		Where("uid = ? AND source_table = ? AND source_id IN ? AND deleted_at IS NULL", uid, sourceTableWorkAgentFile, ids).
		Updates(map[string]interface{}{
			"status":     model.GlobalAssetStatusDeleted,
			"deleted_at": now,
			"updated_at": now,
		}).Error
}

func (r *Repository) CreateFromThreadFileForThread(file *workagentModel.ThreadFile, thread *workagentModel.ChatThread) (*model.GlobalAsset, error) {
	if file == nil || file.Id == 0 || r == nil || r.db == nil {
		return nil, nil
	}
	var projectID *uint
	visibility := model.GlobalAssetVisibilityPrivate
	if thread != nil && thread.ProjectID > 0 {
		pid := uint(thread.ProjectID)
		projectID = &pid
		visibility = model.GlobalAssetVisibilityProject
	}
	global := model.GlobalAsset{
		UID:           file.UID,
		ProjectID:     projectID,
		UUID:          uuid.NewString(),
		Kind:          normalizeKind(file.MimeType, file.FileType),
		Source:        model.GlobalAssetSourceAgent,
		SourceTable:   sourceTableWorkAgentFile,
		SourceID:      uint64(file.Id),
		SourceItemKey: "file",
		URL:           fmt.Sprintf("/api/work-agent/file/%d/download", file.Id),
		ThumbURL:      "",
		MimeType:      strings.TrimSpace(file.MimeType),
		SizeBytes:     int64(file.FileSize),
		ContentHash:   normalizeContentHash(file.FileHash),
		Status:        model.GlobalAssetStatusActive,
		Visibility:    visibility,
		VariantType:   model.GlobalAssetVariantOriginal,
		Metadata: model.JSONMap{
			"threadId":    file.ThreadID,
			"messageId":   file.MessageID,
			"projectId":   uintValue(projectID),
			"fileName":    strings.TrimSpace(file.FileName),
			"displayName": strings.TrimSpace(file.DisplayName),
			"fileSource":  string(file.FileSource),
			"storagePath": strings.TrimSpace(file.FilePath),
		},
	}
	row, err := r.upsertBySource(&global)
	if err != nil {
		return nil, err
	}
	if row != nil && row.Id != 0 && file.GlobalAssetID != row.Id {
		_ = r.db.Model(&workagentModel.ThreadFile{}).Where("id = ?", file.Id).Update("global_asset_id", row.Id).Error
	}
	return row, nil
}

func (r *Repository) CreateManagedUpload(in ManagedUploadInput) (*model.GlobalAsset, error) {
	if r == nil || r.db == nil || in.UID == 0 || strings.TrimSpace(in.URL) == "" {
		return nil, nil
	}
	uploadID := strings.TrimSpace(in.UploadID)
	if uploadID == "" {
		uploadID = uuid.NewString()
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = normalizeKind(in.MimeType, "")
	}
	global := model.GlobalAsset{
		UID:           int(in.UID),
		UUID:          uuid.NewString(),
		Kind:          kind,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTableReferenceUpload,
		SourceID:      uint64(in.UID),
		SourceItemKey: uploadID,
		URL:           strings.TrimSpace(in.URL),
		MimeType:      strings.TrimSpace(in.MimeType),
		SizeBytes:     in.SizeBytes,
		ContentHash:   normalizeContentHash(in.ContentHash),
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   model.GlobalAssetVariantOriginal,
		Metadata: model.JSONMap{
			"uploadId": uploadID,
		},
	}
	if in.ProjectID != 0 {
		global.ProjectID = &in.ProjectID
		global.Visibility = model.GlobalAssetVisibilityProject
	}
	return r.upsertBySource(&global)
}

func (r *Repository) upsertBySource(in *model.GlobalAsset) (*model.GlobalAsset, error) {
	if in == nil || strings.TrimSpace(in.SourceTable) == "" || in.SourceID == 0 {
		return nil, nil
	}
	in.SourceTable = strings.TrimSpace(in.SourceTable)
	in.SourceItemKey = strings.TrimSpace(in.SourceItemKey)
	if in.SourceItemKey == "" {
		in.SourceItemKey = "default"
	}
	if strings.TrimSpace(in.UUID) == "" {
		in.UUID = uuid.NewString()
	}
	if strings.TrimSpace(in.VariantType) == "" {
		in.VariantType = model.GlobalAssetVariantOriginal
	}
	if in.Status == 0 {
		in.Status = model.GlobalAssetStatusActive
	}
	if in.Visibility == 0 {
		in.Visibility = model.GlobalAssetVisibilityPrivate
	}

	row := *in
	err := r.db.
		Where("source_table = ? AND source_id = ? AND source_item_key = ?", in.SourceTable, in.SourceID, in.SourceItemKey).
		Assign(map[string]interface{}{
			"uid":             in.UID,
			"team_id":         in.TeamID,
			"project_id":      in.ProjectID,
			"kind":            in.Kind,
			"source":          in.Source,
			"url":             in.URL,
			"thumb_url":       in.ThumbURL,
			"mime_type":       in.MimeType,
			"size_bytes":      in.SizeBytes,
			"width":           in.Width,
			"height":          in.Height,
			"duration_ms":     in.DurationMs,
			"content_hash":    in.ContentHash,
			"status":          in.Status,
			"visibility":      in.Visibility,
			"parent_asset_id": in.ParentAssetID,
			"variant_type":    in.VariantType,
			"metadata":        in.Metadata,
		}).
		FirstOrCreate(&row).Error
	if err != nil {
		return nil, fmt.Errorf("upsert global asset: %w", err)
	}
	return &row, nil
}

func cloneJSONMap(in model.JSONMap) model.JSONMap {
	if len(in) == 0 {
		return nil
	}
	out := make(model.JSONMap, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func readableProjectRoles() []string {
	return []string{
		model.GlobalProjectRoleOwner,
		model.GlobalProjectRoleEditor,
		model.GlobalProjectRoleViewer,
		model.GlobalProjectRoleCommenter,
	}
}

func normalizeKind(mimeType, fallback string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return model.GlobalAssetKindImage
	case strings.HasPrefix(mimeType, "video/"):
		return model.GlobalAssetKindVideo
	case strings.HasPrefix(mimeType, "audio/"):
		return model.GlobalAssetKindAudio
	}
	fallback = strings.ToLower(strings.TrimSpace(fallback))
	switch {
	case strings.Contains(fallback, "image"), strings.Contains(fallback, "thumbnail"):
		return model.GlobalAssetKindImage
	case strings.Contains(fallback, "video"):
		return model.GlobalAssetKindVideo
	case strings.Contains(fallback, "audio"):
		return model.GlobalAssetKindAudio
	case strings.Contains(fallback, "zip"), strings.Contains(fallback, "archive"):
		return model.GlobalAssetKindArchive
	default:
		return model.GlobalAssetKindDocument
	}
}

func variantTypeFromAssetKind(assetKind string) string {
	switch strings.ToLower(strings.TrimSpace(assetKind)) {
	case "thumbnail", "thumb":
		return model.GlobalAssetVariantThumb
	case "preview":
		return model.GlobalAssetVariantPreview
	case "poster":
		return model.GlobalAssetVariantPoster
	case "source":
		return model.GlobalAssetVariantSource
	default:
		return model.GlobalAssetVariantOriginal
	}
}

func globalAssetStatusFromGenerationObject(status int8) int8 {
	switch status {
	case model.GenerationObjectStatusDeleted,
		model.GenerationObjectStatusHidden,
		model.GenerationObjectStatusOrphan:
		return model.GlobalAssetStatusDeleted
	default:
		return model.GlobalAssetStatusActive
	}
}

func normalizeContentHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		return value[:64]
	}
	return value
}

func uintValue(v *uint) uint {
	if v == nil {
		return 0
	}
	return *v
}
