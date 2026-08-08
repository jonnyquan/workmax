package workagent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

// ArtifactView is the P1 adapter shape over existing thread files.
// It is intentionally read-only and schema-less: no new table, no
// migration, no lifecycle writes. P1 callers can consume artifacts
// now while the current source of truth remains w_workagent_thread_file.
type ArtifactView struct {
	ID                        string                             `json:"id"`
	FileID                    uint                               `json:"fileId"`
	ThreadID                  uint                               `json:"threadId"`
	Name                      string                             `json:"name"`
	DisplayName               string                             `json:"displayName"`
	ArtifactType              string                             `json:"artifactType"`
	OutputType                string                             `json:"outputType"`
	PreviewType               string                             `json:"previewType"`
	Preview                   ArtifactPreviewCapability          `json:"preview"`
	ExportTargets             []string                           `json:"exportTargets"`
	Version                   int                                `json:"version"`
	Status                    string                             `json:"status"`
	ReviewState               string                             `json:"reviewState"`
	ReviewSource              string                             `json:"reviewSource,omitempty"`
	ReviewSummary             string                             `json:"reviewSummary,omitempty"`
	ReviewDetails             *ArtifactReviewDetails             `json:"reviewDetails,omitempty"`
	ComparisonSource          string                             `json:"comparisonSource,omitempty"`
	ComparisonSummary         string                             `json:"comparisonSummary,omitempty"`
	ComparisonDecision        string                             `json:"comparisonDecision,omitempty"`
	Source                    workagentModel.FileSource          `json:"source"`
	FilePath                  string                             `json:"filePath"`
	FileSize                  uint64                             `json:"fileSize"`
	MimeType                  string                             `json:"mimeType"`
	Description               string                             `json:"description,omitempty"`
	FileHash                  string                             `json:"fileHash,omitempty"`
	DownloadURL               string                             `json:"downloadUrl,omitempty"`
	PreviewURL                string                             `json:"previewUrl,omitempty"`
	ParentArtifactID          string                             `json:"parentArtifactId,omitempty"`
	ArtifactRelation          string                             `json:"artifactRelation,omitempty"`
	DesignSystemBasename      string                             `json:"designSystemBasename,omitempty"`
	DesignSystemTitle         string                             `json:"designSystemTitle,omitempty"`
	DesignSystemDerivedFrom   string                             `json:"designSystemDerivedFrom,omitempty"`
	HTMLValidation            *ArtifactHTMLValidationResult      `json:"htmlValidation,omitempty"`
	HTMLExportPlan            *ArtifactHTMLExportPlan            `json:"htmlExportPlan,omitempty"`
	HTMLBrowserValidationPlan *ArtifactHTMLBrowserValidationPlan `json:"htmlBrowserValidationPlan,omitempty"`
	CreatedAt                 time.Time                          `json:"createdAt"`
	UpdatedAt                 time.Time                          `json:"updatedAt"`
}

type ArtifactReviewDetails struct {
	Source       string                                    `json:"source,omitempty"`
	Decision     string                                    `json:"decision,omitempty"`
	OverallScore float64                                   `json:"overallScore,omitempty"`
	Verdict      string                                    `json:"verdict,omitempty"`
	TopFixes     []string                                  `json:"topFixes,omitempty"`
	Dimensions   map[string]ArtifactReviewDimensionDetails `json:"dimensions,omitempty"`
}

type ArtifactReviewDimensionDetails struct {
	Score int      `json:"score"`
	Notes string   `json:"notes,omitempty"`
	Fixes []string `json:"fixes,omitempty"`
}

// ArtifactPreviewCapability is the P1 preview registry entry attached
// to each artifact. It tells clients whether the artifact can be
// rendered inline, whether that renderer needs a sandbox, and what
// size budget applies before falling back to download.
type ArtifactPreviewCapability struct {
	Type            string `json:"type"`
	Mode            string `json:"mode"`
	Inline          bool   `json:"inline"`
	RequiresSandbox bool   `json:"requiresSandbox"`
	DownloadOnly    bool   `json:"downloadOnly"`
	MaxBytes        int64  `json:"maxBytes"`
}

// ListArtifactViewsByThread maps thread files into artifact views.
// It preserves FileRepository's uid/thread scoping and ordering.
func ListArtifactViewsByThread(repo *FileRepository, uid int, threadID uint) ([]ArtifactView, error) {
	if repo == nil {
		repo = DefaultFileRepository()
	}
	rows, err := repo.ListRawByThread(uid, threadID, nil)
	if err != nil {
		return nil, fmt.Errorf("list artifact views: %w", err)
	}
	fileIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		fileIDs = append(fileIDs, row.Id)
	}
	registryByFileID, err := NewArtifactRegistryRepository(repo.db).ListByThreadFileIDs(uid, threadID, fileIDs)
	if err != nil {
		return nil, fmt.Errorf("list artifact registry: %w", err)
	}
	views := make([]ArtifactView, 0, len(rows))
	for _, row := range rows {
		if registry, ok := registryByFileID[row.Id]; ok {
			views = append(views, ArtifactViewFromRegistryAndThreadFile(registry, row))
			continue
		}
		views = append(views, ArtifactViewFromThreadFile(row))
	}
	assignArtifactVersions(views)
	annotateArtifactViewsWithSelectedDesignSystem(repo.db, threadID, views)
	return views, nil
}

func annotateArtifactViewsWithSelectedDesignSystem(db *gorm.DB, threadID uint, views []ArtifactView) {
	if len(views) == 0 || db == nil || threadID == 0 {
		return
	}
	ref := selectedDesignSystemRef(db, threadID)
	if ref.Basename == "" {
		return
	}
	for i := range views {
		if strings.TrimSpace(views[i].DesignSystemBasename) == "" {
			views[i].DesignSystemBasename = ref.Basename
			views[i].DesignSystemTitle = ref.Title
			views[i].DesignSystemDerivedFrom = ref.DerivedFrom
		}
	}
}

type artifactDesignSystemRef struct {
	Basename    string
	Title       string
	DerivedFrom string
}

func selectedDesignSystemRef(db *gorm.DB, threadID uint) artifactDesignSystemRef {
	msg, err := NewMessageRepository(db).FindMostRecentByMetadataKind(threadID, "visual_direction_selected")
	if err != nil || msg == nil {
		return artifactDesignSystemRef{}
	}
	var meta struct {
		Kind                 string `json:"kind"`
		DesignSystemBasename string `json:"design_system_basename"`
		DesignSystemTitle    string `json:"design_system_title"`
		DesignSystemDerived  string `json:"design_system_derived_from"`
	}
	if err := json.Unmarshal([]byte(msg.Metadata), &meta); err != nil {
		return artifactDesignSystemRef{}
	}
	if meta.Kind != "visual_direction_selected" || strings.TrimSpace(meta.DesignSystemBasename) == "" {
		return artifactDesignSystemRef{}
	}
	return artifactDesignSystemRef{
		Basename:    strings.TrimSpace(meta.DesignSystemBasename),
		Title:       strings.TrimSpace(meta.DesignSystemTitle),
		DerivedFrom: strings.TrimSpace(meta.DesignSystemDerived),
	}
}

// ArtifactViewFromThreadFile is pure mapping logic so API tests and
// future registry code can pin behavior without needing the database.
func ArtifactViewFromThreadFile(file workagentModel.ThreadFile) ArtifactView {
	source := file.FileSource
	if source == "" {
		source = workagentModel.FileSourceUpload
	}
	outputType := inferOutputType(file)
	previewType := inferPreviewType(outputType)
	displayName := file.DisplayName
	if displayName == "" {
		displayName = file.FileName
	}
	return ArtifactView{
		ID:            "thread-file-" + strconv.FormatUint(uint64(file.Id), 10),
		FileID:        file.Id,
		ThreadID:      file.ThreadID,
		Name:          file.FileName,
		DisplayName:   displayName,
		ArtifactType:  inferArtifactType(source, outputType),
		OutputType:    outputType,
		PreviewType:   previewType,
		Preview:       inferPreviewCapability(previewType),
		ExportTargets: inferExportTargets(outputType),
		Version:       1,
		Status:        inferArtifactStatus(source),
		ReviewState:   "none",
		Source:        source,
		FilePath:      file.FilePath,
		FileSize:      file.FileSize,
		MimeType:      file.MimeType,
		Description:   file.Description,
		FileHash:      file.FileHash,
		CreatedAt:     file.CreatedAt,
		UpdatedAt:     file.UpdatedAt,
	}
}

func inferPreviewCapability(previewType string) ArtifactPreviewCapability {
	switch previewType {
	case "image":
		return ArtifactPreviewCapability{Type: previewType, Mode: "image", Inline: true, MaxBytes: 25 * 1024 * 1024}
	case "video":
		return ArtifactPreviewCapability{Type: previewType, Mode: "video", Inline: true, MaxBytes: 250 * 1024 * 1024}
	case "html":
		return ArtifactPreviewCapability{Type: previewType, Mode: "html-sandbox", Inline: true, RequiresSandbox: true, MaxBytes: 5 * 1024 * 1024}
	case "pdf":
		return ArtifactPreviewCapability{Type: previewType, Mode: "pdf", Inline: true, MaxBytes: 50 * 1024 * 1024}
	case "deck":
		return ArtifactPreviewCapability{Type: previewType, Mode: "pdf-companion-or-download", Inline: true, MaxBytes: 50 * 1024 * 1024}
	case "markdown":
		return ArtifactPreviewCapability{Type: previewType, Mode: "text", Inline: true, MaxBytes: 2 * 1024 * 1024}
	default:
		return ArtifactPreviewCapability{Type: "unknown", Mode: "download", Inline: false, DownloadOnly: true}
	}
}

func assignArtifactVersions(views []ArtifactView) {
	type groupState struct {
		count    int
		previous string
	}
	groups := map[string]groupState{}
	for i := len(views) - 1; i >= 0; i-- {
		if !strings.HasPrefix(views[i].ID, "thread-file-") {
			continue
		}
		key := artifactVersionKey(views[i])
		state := groups[key]
		state.count++
		views[i].Version = state.count
		if state.previous != "" {
			views[i].ParentArtifactID = state.previous
		}
		state.previous = views[i].ID
		groups[key] = state
	}
}

func artifactVersionKey(view ArtifactView) string {
	name := strings.TrimSpace(strings.ToLower(view.DisplayName))
	if name == "" {
		name = strings.TrimSpace(strings.ToLower(view.Name))
	}
	return strings.Join([]string{
		string(view.Source),
		view.ArtifactType,
		name,
	}, "|")
}

func inferArtifactStatus(source workagentModel.FileSource) string {
	switch source {
	case workagentModel.FileSourceOutput:
		return "draft"
	case workagentModel.FileSourceSystem:
		return "system"
	default:
		return "reference"
	}
}

func inferArtifactType(source workagentModel.FileSource, outputType string) string {
	if source == workagentModel.FileSourceUpload {
		return "reference"
	}
	switch outputType {
	case "pptx":
		return "deck"
	case "pdf":
		return "document"
	case "html":
		return "html"
	case "mp4", "mov", "webm":
		return "video"
	case "png", "jpg", "jpeg", "gif", "webp", "svg":
		return "image"
	case "md", "markdown", "txt":
		return "document"
	default:
		return "file"
	}
}

func inferOutputType(file workagentModel.ThreadFile) string {
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(file.FileName)), "."); ext != "" {
		return normalizeOutputType(ext)
	}
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(file.FilePath)), "."); ext != "" {
		return normalizeOutputType(ext)
	}
	if file.FileType != "" {
		return normalizeOutputType(file.FileType)
	}
	if file.MimeType != "" {
		return outputTypeFromMime(file.MimeType)
	}
	return "unknown"
}

func normalizeOutputType(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	v = strings.TrimPrefix(v, ".")
	switch v {
	case "jpeg":
		return "jpg"
	case "markdown":
		return "md"
	case "text/plain":
		return "txt"
	case "application/pdf":
		return "pdf"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "pptx"
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/svg+xml":
		return "svg"
	case "text/html":
		return "html"
	case "video/mp4":
		return "mp4"
	default:
		if strings.Contains(v, "/") {
			return outputTypeFromMime(v)
		}
		return v
	}
}

func outputTypeFromMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf":
		return "pdf"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "pptx"
	case "text/html":
		return "html"
	case "text/markdown", "text/x-markdown":
		return "md"
	case "text/plain":
		return "txt"
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/svg+xml":
		return "svg"
	case "video/mp4":
		return "mp4"
	case "video/quicktime":
		return "mov"
	default:
		return "unknown"
	}
}

func inferPreviewType(outputType string) string {
	switch outputType {
	case "png", "jpg", "jpeg", "gif", "webp", "svg":
		return "image"
	case "mp4", "mov", "webm":
		return "video"
	case "pptx":
		return "deck"
	case "pdf":
		return "pdf"
	case "html":
		return "html"
	case "md", "txt":
		return "markdown"
	default:
		return "unknown"
	}
}

func inferExportTargets(outputType string) []string {
	switch outputType {
	case "pptx":
		return []string{"pptx", "pdf", "zip"}
	case "html":
		return []string{"html", "png", "pdf", "mp4", "gif", "zip"}
	case "pdf":
		return []string{"pdf", "zip"}
	case "png", "jpg", "jpeg", "gif", "webp", "svg":
		return []string{outputType, "zip"}
	case "mp4", "mov", "webm":
		return []string{outputType, "zip"}
	case "md", "txt":
		return []string{outputType, "pdf", "zip"}
	default:
		return []string{"zip"}
	}
}
