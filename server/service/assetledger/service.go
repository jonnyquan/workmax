package assetledger

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"server/model"
	workagentModel "server/model/workagent"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct{}

func New() *Service {
	return &Service{}
}

func (s *Service) UpsertGeneratedObjectWithDB(db *gorm.DB, object *model.GenerationObject) error {
	if db == nil || object == nil || object.Id == 0 || object.UID == 0 {
		return nil
	}
	if object.Status != model.GenerationObjectStatusActive && object.Status != model.GenerationObjectStatusHidden {
		return s.DeleteBySourceWithDB(db, object.UID, "generated", uint64(object.Id))
	}

	previewURL := ""
	if isImageLike(object.AssetKind, object.ContentType) {
		previewURL = strings.TrimSpace(object.PublicURL)
	}
	visibility := model.UserAssetLedgerVisibilityVisible
	if object.Status == model.GenerationObjectStatusHidden {
		visibility = model.UserAssetLedgerVisibilityHidden
	}
	hasManagedObject := strings.TrimSpace(object.ObjectKey) != "" && !isLocalGenerationObject(object)

	entry := model.UserAssetLedger{
		UID:              object.UID,
		Source:           "generated",
		SourceID:         uint64(object.Id),
		GlobalAssetID:    object.GlobalAssetID,
		ItemKey:          "",
		VisibilityStatus: visibility,
		ContainerType:    "tool",
		ContainerKey:     buildGeneratedContainerKey(object.ToolID),
		ContainerTitle:   buildGeneratedContainerTitle(object.ToolID),
		Title:            buildGeneratedAssetTitle(object.ToolID, object.AssetKind),
		Kind:             strings.TrimSpace(object.AssetKind),
		MimeType:         strings.TrimSpace(object.ContentType),
		SizeBytes:        object.SizeBytes,
		URL:              strings.TrimSpace(object.PublicURL),
		ThumbURL:         previewURL,
		PreviewURL:       previewURL,
		ToolID:           strings.TrimSpace(object.ToolID),
		RecordID:         object.RecordID,
		TaskID:           strings.TrimSpace(object.TaskID),
		ObjectKey:        strings.TrimSpace(object.ObjectKey),
		StoragePath:      firstNonEmpty(strings.TrimSpace(object.ObjectKey), strings.TrimSpace(object.PublicURL)),
		IsAttached:       true,
		HasManagedObject: hasManagedObject,
	}
	return upsert(db, &entry)
}

func (s *Service) UpdateGeneratedRecordIDByTaskWithDB(db *gorm.DB, uid int, taskID string, recordID uint) error {
	if db == nil || strings.TrimSpace(taskID) == "" || recordID == 0 {
		return nil
	}
	query := db.Model(&model.UserAssetLedger{}).Where("source = ? AND task_id = ?", "generated", strings.TrimSpace(taskID))
	if uid > 0 {
		query = query.Where("uid = ?", uid)
	}
	return query.Update("record_id", recordID).Error
}

func (s *Service) DeleteGeneratedByTaskIDsWithDB(db *gorm.DB, uid int, taskIDs []string) error {
	if db == nil || len(taskIDs) == 0 {
		return nil
	}
	query := db.Where("source = ? AND task_id IN ?", "generated", taskIDs)
	if uid > 0 {
		query = query.Where("uid = ?", uid)
	}
	return query.Delete(&model.UserAssetLedger{}).Error
}

func (s *Service) DeleteGeneratedByRecordIDsWithDB(db *gorm.DB, uid int, recordIDs []uint) error {
	if db == nil || len(recordIDs) == 0 {
		return nil
	}
	query := db.Where("source = ? AND record_id IN ?", "generated", recordIDs)
	if uid > 0 {
		query = query.Where("uid = ?", uid)
	}
	return query.Delete(&model.UserAssetLedger{}).Error
}

func (s *Service) UpdateGeneratedVisibilityByIDWithDB(db *gorm.DB, uid int, sourceID uint, hidden bool) error {
	if db == nil || uid == 0 || sourceID == 0 {
		return nil
	}
	visibility := model.UserAssetLedgerVisibilityVisible
	if hidden {
		visibility = model.UserAssetLedgerVisibilityHidden
	}
	return db.Model(&model.UserAssetLedger{}).
		Where("uid = ? AND source = ? AND source_id = ?", uid, "generated", uint64(sourceID)).
		Update("visibility_status", visibility).Error
}

func (s *Service) UpsertCanvasProjectFileWithDB(db *gorm.DB, asset *model.GlobalAsset, project *model.CanvasProject) error {
	if db == nil || asset == nil || asset.Id == 0 || asset.UID == 0 {
		return nil
	}
	containerKey := fmt.Sprintf("canvas:%d", asset.Id)
	containerTitle := "canvas-asset"
	containerUUID := ""
	projectID := uint(0)
	projectTitle := ""
	projectUUID := ""
	isAttached := false

	if project != nil && project.Id != 0 {
		projectID = project.Id
		projectTitle = strings.TrimSpace(project.Title)
		projectUUID = strings.TrimSpace(project.UUID)
		containerKey = "canvas:" + firstNonEmpty(projectUUID, fmt.Sprintf("project-%d", project.Id))
		containerTitle = projectTitle
		containerUUID = projectUUID
		isAttached = true
	}

	url := strings.TrimSpace(asset.URL)
	thumbURL := strings.TrimSpace(asset.ThumbURL)
	previewURL := ""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.MimeType)), "image/") {
		previewURL = firstNonEmpty(thumbURL, url)
	} else if thumbURL != "" {
		previewURL = thumbURL
	}

	objectKey := jsonMapStringValue(asset.Metadata, "objectKey")
	entry := model.UserAssetLedger{
		UID:              asset.UID,
		Source:           "canvas",
		SourceID:         uint64(asset.Id),
		GlobalAssetID:    asset.Id,
		ItemKey:          "",
		VisibilityStatus: model.UserAssetLedgerVisibilityVisible,
		ContainerType:    "project",
		ContainerKey:     containerKey,
		ContainerTitle:   firstNonEmpty(containerTitle, projectTitle, "canvas-project"),
		ContainerUUID:    containerUUID,
		Title:            firstNonEmpty(jsonMapStringValue(asset.Metadata, "originalName"), jsonMapStringValue(asset.Metadata, "title"), projectTitle, strings.TrimSpace(asset.Kind), "canvas-asset"),
		Kind:             strings.TrimSpace(asset.Kind),
		MimeType:         strings.TrimSpace(asset.MimeType),
		SizeBytes:        asset.SizeBytes,
		Width:            asset.Width,
		Height:           asset.Height,
		URL:              url,
		ThumbURL:         thumbURL,
		PreviewURL:       previewURL,
		ProjectID:        projectID,
		ProjectTitle:     projectTitle,
		ProjectUUID:      projectUUID,
		ObjectKey:        objectKey,
		StoragePath:      firstNonEmpty(objectKey, url),
		IsAttached:       isAttached,
		HasManagedObject: objectKey != "",
	}
	return upsert(db, &entry)
}

func (s *Service) DeleteCanvasByIDWithDB(db *gorm.DB, uid int, assetID uint) error {
	return s.DeleteBySourceWithDB(db, uid, "canvas", uint64(assetID))
}

func (s *Service) DeleteCanvasByProjectIDWithDB(db *gorm.DB, uid int, projectID uint) error {
	if db == nil || projectID == 0 {
		return nil
	}
	return db.Where("source = ? AND project_id = ?", "canvas", projectID).
		Delete(&model.UserAssetLedger{}).Error
}

func (s *Service) SyncCanvasProjectThumbnailWithDB(db *gorm.DB, project *model.CanvasProject) error {
	if db == nil || project == nil || project.Id == 0 || project.UID == 0 {
		return nil
	}
	if strings.TrimSpace(project.ThumbnailURL) == "" {
		return s.DeleteBySourceWithDB(db, project.UID, "canvas_project_thumbnail", uint64(project.Id))
	}
	object := findGenerationObjectByURLWithDB(db, project.UID, strings.TrimSpace(project.ThumbnailURL), []string{"thumbnail", "image"})
	mimeType, sizeBytes, objectKey, managed := generationObjectFields(object)
	entry := model.UserAssetLedger{
		UID:              project.UID,
		Source:           "canvas_project_thumbnail",
		SourceID:         uint64(project.Id),
		ItemKey:          "",
		VisibilityStatus: model.UserAssetLedgerVisibilityVisible,
		ContainerType:    "project",
		ContainerKey:     "canvas_project_thumbnail:" + firstNonEmpty(strings.TrimSpace(project.UUID), fmt.Sprintf("project-%d", project.Id)),
		ContainerTitle:   firstNonEmpty(strings.TrimSpace(project.Title), "canvas-project"),
		ContainerUUID:    strings.TrimSpace(project.UUID),
		Title:            "project-thumbnail",
		Kind:             "thumbnail",
		MimeType:         mimeType,
		SizeBytes:        sizeBytes,
		URL:              strings.TrimSpace(project.ThumbnailURL),
		ThumbURL:         strings.TrimSpace(project.ThumbnailURL),
		PreviewURL:       strings.TrimSpace(project.ThumbnailURL),
		ProjectID:        project.Id,
		ProjectTitle:     strings.TrimSpace(project.Title),
		ProjectUUID:      strings.TrimSpace(project.UUID),
		ToolID:           "canvas",
		ObjectKey:        objectKey,
		StoragePath:      firstNonEmpty(objectKey, strings.TrimSpace(project.ThumbnailURL)),
		IsAttached:       true,
		HasManagedObject: managed,
	}
	return upsert(db, &entry)
}

func (s *Service) SyncCanvasSnapshotThumbnailWithDB(db *gorm.DB, version *model.CanvasVersion, project *model.CanvasProject) error {
	if db == nil || version == nil || version.Id == 0 || project == nil || project.Id == 0 || project.UID == 0 {
		return nil
	}
	if strings.TrimSpace(version.ThumbnailURL) == "" {
		return s.DeleteBySourceWithDB(db, project.UID, "canvas_snapshot_thumbnail", uint64(version.Id))
	}
	object := findGenerationObjectByURLWithDB(db, project.UID, strings.TrimSpace(version.ThumbnailURL), []string{"thumbnail", "image"})
	mimeType, sizeBytes, objectKey, managed := generationObjectFields(object)
	entry := model.UserAssetLedger{
		UID:              project.UID,
		Source:           "canvas_snapshot_thumbnail",
		SourceID:         uint64(version.Id),
		ItemKey:          "",
		VisibilityStatus: model.UserAssetLedgerVisibilityVisible,
		ContainerType:    "project",
		ContainerKey:     fmt.Sprintf("canvas_snapshot_thumbnail:%s:%d", firstNonEmpty(strings.TrimSpace(project.UUID), fmt.Sprintf("project-%d", project.Id)), version.Version),
		ContainerTitle:   firstNonEmpty(strings.TrimSpace(project.Title), "canvas-project"),
		ContainerUUID:    strings.TrimSpace(project.UUID),
		Title:            fmt.Sprintf("version-thumbnail:v%d", version.Version),
		Kind:             "thumbnail",
		MimeType:         mimeType,
		SizeBytes:        sizeBytes,
		URL:              strings.TrimSpace(version.ThumbnailURL),
		ThumbURL:         strings.TrimSpace(version.ThumbnailURL),
		PreviewURL:       strings.TrimSpace(version.ThumbnailURL),
		ProjectID:        project.Id,
		ProjectTitle:     strings.TrimSpace(project.Title),
		ProjectUUID:      strings.TrimSpace(project.UUID),
		ToolID:           "canvas",
		ObjectKey:        objectKey,
		StoragePath:      firstNonEmpty(objectKey, strings.TrimSpace(version.ThumbnailURL)),
		IsAttached:       true,
		HasManagedObject: managed,
	}
	return upsert(db, &entry)
}

func (s *Service) DeleteCanvasThumbnailsByProjectIDWithDB(db *gorm.DB, uid int, projectID uint) error {
	if db == nil || projectID == 0 {
		return nil
	}
	return db.Where("source IN ? AND project_id = ?", []string{"canvas_project_thumbnail", "canvas_snapshot_thumbnail"}, projectID).
		Delete(&model.UserAssetLedger{}).Error
}

func (s *Service) UpsertThreadFileWithDB(db *gorm.DB, file *workagentModel.ThreadFile, thread *workagentModel.ChatThread) error {
	if db == nil || file == nil || file.Id == 0 || file.UID == 0 {
		return nil
	}
	source := "thread_upload"
	if file.FileSource == workagentModel.FileSourceOutput {
		source = "thread_output"
	}
	threadUUID := ""
	threadName := ""
	projectID := uint(0)
	projectTitle := ""
	projectUUID := ""
	if thread != nil {
		threadUUID = strings.TrimSpace(thread.UUID)
		threadName = strings.TrimSpace(thread.Name)
		if thread.ProjectID > 0 {
			projectID = thread.ProjectID
			var project model.CanvasProject
			if err := db.Where("id = ?", thread.ProjectID).First(&project).Error; err == nil {
				projectTitle = strings.TrimSpace(project.Title)
				projectUUID = strings.TrimSpace(project.UUID)
			}
		}
	}
	previewURL := ""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.MimeType)), "image/") {
		normalized := strings.TrimSpace(filepath.ToSlash(file.FilePath))
		if normalized != "" {
			previewURL = "/agent_workspace/" + strings.TrimPrefix(normalized, "/")
		}
	}
	entry := model.UserAssetLedger{
		UID:              file.UID,
		Source:           source,
		SourceID:         uint64(file.Id),
		GlobalAssetID:    file.GlobalAssetID,
		ItemKey:          "",
		VisibilityStatus: model.UserAssetLedgerVisibilityVisible,
		ContainerType:    "thread",
		ContainerKey:     buildThreadContainerKey(source, threadUUID, file.ThreadID),
		ContainerTitle:   firstNonEmpty(threadName, "work-thread"),
		ContainerUUID:    threadUUID,
		Title:            firstNonEmpty(strings.TrimSpace(file.DisplayName), strings.TrimSpace(file.FileName), "thread-file"),
		Kind:             firstNonEmpty(strings.TrimSpace(file.FileType), strings.TrimSpace(string(file.FileSource)), "file"),
		MimeType:         strings.TrimSpace(file.MimeType),
		SizeBytes:        int64(file.FileSize),
		URL:              fmt.Sprintf("/api/work-agent/file/%d/download", file.Id),
		ThumbURL:         "",
		PreviewURL:       previewURL,
		ThreadID:         file.ThreadID,
		ThreadName:       threadName,
		ThreadUUID:       threadUUID,
		ProjectID:        projectID,
		ProjectTitle:     projectTitle,
		ProjectUUID:      projectUUID,
		StoragePath:      strings.TrimSpace(filepath.ToSlash(file.FilePath)),
		IsAttached:       true,
		HasManagedObject: false,
	}
	return upsert(db, &entry)
}

func (s *Service) DeleteThreadByIDWithDB(db *gorm.DB, uid int, source string, sourceID uint) error {
	return s.DeleteBySourceWithDB(db, uid, source, uint64(sourceID))
}

func (s *Service) DeleteThreadByThreadIDWithDB(db *gorm.DB, uid int, threadID uint) error {
	if db == nil || uid == 0 || threadID == 0 {
		return nil
	}
	return db.Where("uid = ? AND source IN ? AND thread_id = ?", uid, []string{"thread_upload", "thread_output"}, threadID).
		Delete(&model.UserAssetLedger{}).Error
}

func (s *Service) UpsertReferenceUploadWithDB(db *gorm.DB, asset *model.GlobalAsset) error {
	if db == nil || asset == nil || asset.Id == 0 || asset.UID == 0 {
		return nil
	}
	url := strings.TrimSpace(asset.URL)
	thumbURL := strings.TrimSpace(asset.ThumbURL)
	previewURL := ""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.MimeType)), "image/") {
		previewURL = firstNonEmpty(thumbURL, url)
	}
	entry := model.UserAssetLedger{
		UID:              asset.UID,
		Source:           "reference_upload",
		SourceID:         uint64(asset.Id),
		GlobalAssetID:    asset.Id,
		ItemKey:          "",
		VisibilityStatus: model.UserAssetLedgerVisibilityVisible,
		ContainerType:    "upload",
		ContainerKey:     "reference_upload",
		ContainerTitle:   "reference uploads",
		Title:            firstNonEmpty(jsonMapStringValue(asset.Metadata, "uploadId"), strings.TrimSpace(asset.Kind), "reference-upload"),
		Kind:             strings.TrimSpace(asset.Kind),
		MimeType:         strings.TrimSpace(asset.MimeType),
		SizeBytes:        asset.SizeBytes,
		Width:            asset.Width,
		Height:           asset.Height,
		URL:              url,
		ThumbURL:         thumbURL,
		PreviewURL:       previewURL,
		ObjectKey:        jsonMapStringValue(asset.Metadata, "objectKey"),
		StoragePath:      firstNonEmpty(jsonMapStringValue(asset.Metadata, "objectKey"), url),
		IsAttached:       false,
		HasManagedObject: jsonMapStringValue(asset.Metadata, "objectKey") != "",
	}
	return upsert(db, &entry)
}

func (s *Service) DeleteReferenceUploadByGlobalAssetIDWithDB(db *gorm.DB, uid int, assetID uint) error {
	return s.DeleteBySourceWithDB(db, uid, "reference_upload", uint64(assetID))
}

func (s *Service) DeleteBySourceWithDB(db *gorm.DB, uid int, source string, sourceID uint64) error {
	if db == nil || uid == 0 || strings.TrimSpace(source) == "" || sourceID == 0 {
		return nil
	}
	return db.Where("uid = ? AND source = ? AND source_id = ?", uid, strings.TrimSpace(source), sourceID).
		Delete(&model.UserAssetLedger{}).Error
}

func (s *Service) SyncGenerationInputsWithDB(db *gorm.DB, record *model.GenerationRecord) error {
	if db == nil || record == nil || record.Id == 0 || record.UID == 0 {
		return nil
	}
	refs := collectGenerationInputRefs(record)
	if len(refs) == 0 {
		return s.DeleteBySourceWithDB(db, record.UID, "generation_input", uint64(record.Id))
	}
	if err := s.DeleteBySourceWithDB(db, record.UID, "generation_input", uint64(record.Id)); err != nil {
		return err
	}
	for _, ref := range refs {
		object := findGenerationObjectByURLWithDB(db, record.UID, ref.URL, []string{"reference", "image", "thumbnail"})
		mimeType, sizeBytes, objectKey, managed := generationObjectFields(object)
		if ref.MimeType != "" {
			mimeType = ref.MimeType
		}
		if ref.SizeBytes > 0 {
			sizeBytes = ref.SizeBytes
		}
		previewURL := ""
		if mimeType == "" || strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			previewURL = ref.URL
		}
		entry := model.UserAssetLedger{
			UID:              record.UID,
			Source:           "generation_input",
			SourceID:         uint64(record.Id),
			GlobalAssetID:    generationObjectGlobalAssetID(object),
			ItemKey:          ref.ItemKey,
			VisibilityStatus: model.UserAssetLedgerVisibilityVisible,
			ContainerType:    "record",
			ContainerKey:     fmt.Sprintf("generation_input:%d", record.Id),
			ContainerTitle:   fmt.Sprintf("%s #%d", firstNonEmpty(strings.TrimSpace(record.ToolID), "generator"), record.Id),
			Title:            ref.Label,
			Kind:             ref.Kind,
			MimeType:         mimeType,
			SizeBytes:        sizeBytes,
			URL:              ref.URL,
			ThumbURL:         previewURL,
			PreviewURL:       previewURL,
			ToolID:           strings.TrimSpace(record.ToolID),
			RecordID:         record.Id,
			ObjectKey:        objectKey,
			StoragePath:      firstNonEmpty(objectKey, ref.URL),
			IsAttached:       true,
			HasManagedObject: managed,
		}
		if err := upsert(db, &entry); err != nil {
			return err
		}
	}
	return nil
}

func upsert(db *gorm.DB, entry *model.UserAssetLedger) error {
	if db == nil || entry == nil {
		return nil
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "uid"},
			{Name: "source"},
			{Name: "source_id"},
			{Name: "item_key"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"visibility_status",
			"global_asset_id",
			"container_type",
			"container_key",
			"container_title",
			"container_uuid",
			"title",
			"kind",
			"mime_type",
			"size_bytes",
			"width",
			"height",
			"url",
			"thumb_url",
			"preview_url",
			"project_id",
			"project_title",
			"project_uuid",
			"thread_id",
			"thread_name",
			"thread_uuid",
			"tool_id",
			"record_id",
			"task_id",
			"object_key",
			"storage_path",
			"is_attached",
			"has_managed_object",
			"updated_at",
		}),
	}).Create(entry).Error
}

func buildGeneratedContainerKey(toolID string) string {
	return "generated:" + firstNonEmpty(strings.TrimSpace(toolID), "generator")
}

func buildGeneratedContainerTitle(toolID string) string {
	return firstNonEmpty(strings.TrimSpace(toolID), "generator")
}

func buildGeneratedAssetTitle(toolID, assetKind string) string {
	return firstNonEmpty(strings.TrimSpace(toolID), "generator") + ":" + firstNonEmpty(strings.TrimSpace(assetKind), "asset")
}

func buildThreadContainerKey(source, threadUUID string, threadID uint) string {
	return fmt.Sprintf("%s:%s", source, firstNonEmpty(strings.TrimSpace(threadUUID), fmt.Sprintf("thread-%d", threadID)))
}

type generationInputRef struct {
	ItemKey   string
	Label     string
	Kind      string
	URL       string
	MimeType  string
	SizeBytes int64
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func jsonMapStringValue(m model.JSONMap, key string) string {
	if m == nil {
		return ""
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

// collectGenerationInputRefs 以确定顺序收集记录的输入引用, 去重后给每条分配 {label}:{ordinal} 语义键.
// 顺序: inputFiles.face/source/primaryFace → inputFiles.referenceImages[] → params.referenceImages[].
// 去重键为 (kind, URL), 首次出现胜出; ordinal 按 label 维度递增, 保证同一 record 内唯一.
func collectGenerationInputRefs(record *model.GenerationRecord) []generationInputRef {
	if record == nil {
		return nil
	}
	inputFiles := parseJSONStringMap(record.InputFiles)
	params := parseJSONStringMap(record.Params)

	type rawRef struct {
		label     string
		kind      string
		url       string
		mimeType  string
		sizeBytes int64
	}

	raws := make([]rawRef, 0, 8)
	appendFixed := func(label, url string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		raws = append(raws, rawRef{label: label, kind: "input-image", url: url})
	}
	appendFixed("faceImageUrl", jsonMapStringValue(inputFiles, "faceImageUrl"))
	appendFixed("sourceImageUrl", jsonMapStringValue(inputFiles, "sourceImageUrl"))
	appendFixed("primaryFaceImageUrl", jsonMapStringValue(inputFiles, "primaryFaceImageUrl"))

	appendRefs := func(list []model.ReferenceImageParam) {
		for _, r := range list {
			url := strings.TrimSpace(r.URL)
			if url == "" {
				continue
			}
			raws = append(raws, rawRef{
				label:     firstNonEmpty(strings.TrimSpace(r.ID), "referenceImage"),
				kind:      "input-reference",
				url:       url,
				mimeType:  strings.TrimSpace(r.MimeType),
				sizeBytes: r.FileSize,
			})
		}
	}
	appendRefs(extractReferenceImageParams(inputFiles["referenceImages"]))
	appendRefs(extractReferenceImageParams(params["referenceImages"]))

	seen := make(map[string]bool, len(raws))
	labelOrdinal := make(map[string]int, len(raws))
	refs := make([]generationInputRef, 0, len(raws))
	for _, r := range raws {
		dedupKey := r.kind + "|" + r.url
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		ordinal := labelOrdinal[r.label]
		labelOrdinal[r.label] = ordinal + 1
		refs = append(refs, generationInputRef{
			ItemKey:   fmt.Sprintf("%s:%03d", r.label, ordinal),
			Label:     r.label,
			Kind:      r.kind,
			URL:       r.url,
			MimeType:  r.mimeType,
			SizeBytes: r.sizeBytes,
		})
	}
	return refs
}

func parseJSONStringMap(raw string) map[string]interface{} {
	if strings.TrimSpace(raw) == "" {
		return map[string]interface{}{}
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil || data == nil {
		return map[string]interface{}{}
	}
	return data
}

func extractReferenceImageParams(raw interface{}) []model.ReferenceImageParam {
	if raw == nil {
		return nil
	}
	buf, err := json.Marshal(raw)
	if err != nil || len(buf) == 0 {
		return nil
	}
	var refs []model.ReferenceImageParam
	if err := json.Unmarshal(buf, &refs); err != nil {
		return nil
	}
	filtered := make([]model.ReferenceImageParam, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.URL) == "" {
			continue
		}
		filtered = append(filtered, ref)
	}
	return filtered
}

func isImageLike(assetKind, mimeType string) bool {
	assetKind = strings.ToLower(strings.TrimSpace(assetKind))
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	return assetKind == "image" || assetKind == "thumbnail" || strings.HasPrefix(mimeType, "image/")
}

func findGenerationObjectByURLWithDB(db *gorm.DB, uid int, rawURL string, assetKinds []string) *model.GenerationObject {
	if db == nil || uid == 0 || strings.TrimSpace(rawURL) == "" {
		return nil
	}
	query := db.Model(&model.GenerationObject{}).
		Where("uid = ? AND status IN ?", uid, []int8{model.GenerationObjectStatusActive, model.GenerationObjectStatusHidden}).
		Where("(public_url COLLATE utf8mb4_unicode_ci = ? COLLATE utf8mb4_unicode_ci OR source_url COLLATE utf8mb4_unicode_ci = ? COLLATE utf8mb4_unicode_ci)", strings.TrimSpace(rawURL), strings.TrimSpace(rawURL))
	if len(assetKinds) > 0 {
		query = query.Where("asset_kind IN ?", assetKinds)
	}
	var object model.GenerationObject
	if err := query.Order("id DESC").First(&object).Error; err != nil {
		return nil
	}
	return &object
}

func generationObjectFields(object *model.GenerationObject) (mimeType string, sizeBytes int64, objectKey string, managed bool) {
	if object == nil {
		return "", 0, "", false
	}
	managed = strings.TrimSpace(object.ObjectKey) != "" && !isLocalGenerationObject(object)
	return strings.TrimSpace(object.ContentType), object.SizeBytes, strings.TrimSpace(object.ObjectKey), managed
}

func generationObjectGlobalAssetID(object *model.GenerationObject) uint {
	if object == nil {
		return 0
	}
	return object.GlobalAssetID
}

func isLocalGenerationObject(object *model.GenerationObject) bool {
	if object == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(object.Provider), "local")
}
