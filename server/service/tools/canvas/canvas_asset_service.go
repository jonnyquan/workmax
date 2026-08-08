// Canvas asset service (§13 M1-W1-02). Companion to canvas_project_service.go —
// handlers under server/api/pro/tools delegate asset CRUD and upload
// orchestration here so canvas_api.go stays a thin HTTP→service bridge.
//
// Split in three layers:
//
//   AssetStorage interface
//       Abstracts the filesystem write so the upload path can plug in
//       an S3 (or any other object-store) backend later without
//       touching the handler. LocalAssetStorage is the only concrete
//       impl today, mirroring the original handler's
//       uploads/canvas/uid/{uid}/{uuid}/{filename} layout.
//
//   Pure helpers (NormalizeAssetKind, ClassifyUploadedContent,
//   NormalizeKeyframeRequest, AllowedUploadContentTypes)
//       Unit-testable without touching MySQL or the filesystem. These
//       pin the upload contract (allowed MIME types, timestamp
//       clamping, keyframe URL validation).
//
//   DB-coupled wrappers (CreateAsset, UploadAsset, ListAssets)
//       Run inside a GORM transaction with AssetLedger sync. Upload
//       writes via AssetStorage before the TX opens and invokes the
//       returned cleanup fn on rollback so we never leak files.

package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"server/globals"
	"server/model"
	assetLedgerService "server/service/assetledger"
	globalAssetService "server/service/globalasset"
	projectService "server/service/project"
	"server/utils/safehttp"

	"gorm.io/gorm"
)

// Sentinel errors — handlers map these to HTTP responses. Everything
// else leaks through with a caller-prefixed message so ops can still
// correlate via logs.
var (
	ErrCanvasAssetProjectNotFound = errors.New("canvas: asset project not found")
	ErrCanvasAssetTooLarge        = errors.New("canvas: uploaded file too large")
	ErrCanvasAssetUnsupportedType = errors.New("canvas: unsupported upload content-type")
	ErrCanvasAssetEmptyFile       = errors.New("canvas: uploaded file is empty")
	ErrCanvasInvalidVideoURL      = errors.New("canvas: videoUrl must be an http(s) URL")
	ErrCanvasAssetInvalidInput    = errors.New("canvas: invalid asset input")
	ErrCanvasKeyframeExtraction   = errors.New("canvas: keyframe extraction failed")
	ErrCanvasKeyframeBusy         = errors.New("canvas: keyframe extraction busy")
)

const (
	maxCanvasAssetURLLength         = 2048
	maxCanvasAssetMimeTypeLength    = 128
	maxCanvasAssetKindLength        = 64
	maxCanvasAssetDimension         = 32768
	maxCanvasAssetSizeBytes         = 2 << 30
	maxCanvasAssetMetadataBytes     = 64 << 10
	maxCanvasAssetOriginalNameBytes = 255
	canvasKeyframeExtractTimeout    = 45 * time.Second
	canvasKeyframeConcurrencyPerUID = 2
)

var (
	canvasAssetFilenameSeq   uint64
	canvasKeyframeSemaphores sync.Map
)

func acquireCanvasKeyframeSlot(uid int) (func(), bool) {
	val, _ := canvasKeyframeSemaphores.LoadOrStore(uid, make(chan struct{}, canvasKeyframeConcurrencyPerUID))
	sem := val.(chan struct{})
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	default:
		return nil, false
	}
}

// AssetStorage abstracts where uploaded bytes end up. The handler
// writes first, then the service opens a DB transaction; on TX failure
// the returned cleanup fn runs so rolled-back uploads don't leave
// orphaned files on disk.
type AssetStorage interface {
	Store(ctx context.Context, uid int, projectUUID, filename string, data []byte) (StoredAsset, error)
}

// StoredAsset is the outcome of AssetStorage.Store.
type StoredAsset struct {
	URL     string
	Cleanup func()
}

// LocalAssetStorage writes under {Root}/canvas/uid/{uid}/{uuid}/. If
// Root is empty it defaults to "uploads" to match the original handler.
type LocalAssetStorage struct {
	Root string
}

// NewLocalAssetStorage returns the default LocalAssetStorage rooted at
// "uploads" — same layout the handler wrote to before the extraction.
func NewLocalAssetStorage() LocalAssetStorage {
	return LocalAssetStorage{Root: "uploads"}
}

// Store implements AssetStorage.
func (s LocalAssetStorage) Store(_ context.Context, uid int, projectUUID, filename string, data []byte) (StoredAsset, error) {
	root := s.Root
	if root == "" {
		root = "uploads"
	}
	dir := filepath.Join(root, "canvas", "uid", strconv.Itoa(uid), projectUUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return StoredAsset{}, fmt.Errorf("create upload dir: %w", err)
	}
	dst := filepath.Join(dir, filename)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return StoredAsset{}, fmt.Errorf("write upload file: %w", err)
	}
	return StoredAsset{
		URL:     "/" + filepath.ToSlash(dst),
		Cleanup: func() { _ = os.Remove(dst) },
	}, nil
}

// AllowedUploadContentTypes enumerates MIME types the canvas upload
// endpoint accepts alongside the file extension used for generated
// filenames. Kept as a function (not a var) so callers can't mutate
// the canonical map from under each other.
func AllowedUploadContentTypes() map[string]string {
	return map[string]string{
		"image/png":       ".png",
		"image/jpeg":      ".jpg",
		"image/webp":      ".webp",
		"image/gif":       ".gif",
		"video/mp4":       ".mp4",
		"application/pdf": ".pdf",
	}
}

// NormalizeAssetKind defaults blank kinds to "upload". Mirrors the
// handler's original default so existing canvas documents keep their
// element → asset linkage after the extraction.
func NormalizeAssetKind(raw string) string {
	k := strings.TrimSpace(raw)
	if k == "" {
		return "upload"
	}
	return k
}

// ClassifyUploadedContent returns the sniffed MIME type and matching
// file extension. A non-generic client declaration must match the
// sniffed bytes so multipart Content-Type cannot smuggle arbitrary data
// into a trusted canvas asset.
func sniffCanvasUploadContentType(sniffBytes []byte) string {
	if len(sniffBytes) >= 12 && string(sniffBytes[4:8]) == "ftyp" {
		return "video/mp4"
	}
	return http.DetectContentType(sniffBytes)
}

func normalizeDeclaredContentType(headerContentType string) string {
	ct := strings.TrimSpace(headerContentType)
	if ct == "" {
		return ""
	}
	parsed, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed))
}

func ClassifyUploadedContent(headerContentType string, sniffBytes []byte) (contentType, extension string, ok bool) {
	sniffed := sniffCanvasUploadContentType(sniffBytes)
	ext, allowed := AllowedUploadContentTypes()[sniffed]
	if !allowed {
		return sniffed, "", false
	}
	declared := normalizeDeclaredContentType(headerContentType)
	if declared != "" && !strings.EqualFold(declared, "application/octet-stream") && declared != sniffed {
		return sniffed, "", false
	}
	return sniffed, ext, true
}

// GenerateAssetFilename builds the handler's legacy asset_{numeric}.{ext}
// filename. A per-process sequence suffix avoids same-nanosecond collisions
// without changing the browser-visible asset_*.ext contract.
func GenerateAssetFilename(extension string) string {
	seq := atomic.AddUint64(&canvasAssetFilenameSeq, 1) % 1000000
	return fmt.Sprintf("asset_%d%06d%s", time.Now().UnixNano(), seq, extension)
}

// NormalizedKeyframe is the validated output of NormalizeKeyframeRequest.
type NormalizedKeyframe struct {
	VideoURL        string
	Timestamp       float64
	SourceElementID string
	SourceTaskID    string
	SourceTitle     string
}

// NormalizeKeyframeRequest validates the videoUrl and clamps timestamp
// into [0.1, 120]. NaN/Inf timestamps collapse to 2.5 — matching the
// handler's defensive default for malformed clients.
func NormalizeKeyframeRequest(rawVideoURL string, rawTimestamp float64) (NormalizedKeyframe, error) {
	videoURL := strings.TrimSpace(rawVideoURL)
	if videoURL == "" {
		return NormalizedKeyframe{}, ErrCanvasInvalidVideoURL
	}
	if strings.HasPrefix(videoURL, "/") && !strings.HasPrefix(videoURL, "//") {
		parsed, err := url.Parse(videoURL)
		if err != nil || parsed.Path == "" || hasUnsafeCanvasURLPath(parsed.Path) {
			return NormalizedKeyframe{}, ErrCanvasInvalidVideoURL
		}
	} else {
		parsed, err := safehttp.ValidateURL(videoURL)
		if err != nil || parsed == nil || parsed.Hostname() == "" {
			return NormalizedKeyframe{}, ErrCanvasInvalidVideoURL
		}
	}

	ts := rawTimestamp
	if math.IsNaN(ts) || math.IsInf(ts, 0) {
		ts = 2.5
	}
	if ts < 0.1 {
		ts = 0.1
	}
	if ts > 120 {
		ts = 120
	}
	return NormalizedKeyframe{VideoURL: videoURL, Timestamp: ts}, nil
}

func hasUnsafeCanvasURLPath(pathValue string) bool {
	if pathValue == "" || strings.Contains(pathValue, "\x00") {
		return true
	}
	cleaned := filepath.Clean(pathValue)
	return strings.Contains(cleaned, "..")
}

// ExtractKeyframeAsset performs deterministic media extraction for Canvas
// keyframes. This deliberately avoids the AI image generator path: a storyboard
// keyframe must be an exact frame from the source video, not a generated
// interpretation of a URL.
func ExtractKeyframeAsset(ctx context.Context, db *gorm.DB, storage AssetStorage, uid int, projectID uint, in NormalizedKeyframe) (ProjectFileAsset, error) {
	release, ok := acquireCanvasKeyframeSlot(uid)
	if !ok {
		return ProjectFileAsset{}, ErrCanvasKeyframeBusy
	}
	defer release()

	inputPath, err := localMediaPathForCanvasURL(in.VideoURL)
	if err != nil {
		return ProjectFileAsset{}, err
	}
	if stat, err := os.Stat(inputPath); err != nil || stat.IsDir() {
		parsed, parseErr := url.Parse(strings.TrimSpace(in.VideoURL))
		if parseErr != nil || !parsed.IsAbs() || stat != nil && stat.IsDir() {
			return ProjectFileAsset{}, ErrCanvasInvalidVideoURL
		}
		// Object-storage/CDN deployments may not have the generation file on
		// local disk. The URL was already safehttp-validated and host-gated by
		// the API layer, so ffmpeg can read it directly as a deterministic media
		// source instead of falling back to AI generation.
		inputPath = in.VideoURL
	}

	tmp, err := os.CreateTemp("", "canvas-keyframe-*.png")
	if err != nil {
		return ProjectFileAsset{}, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	extractCtx, cancel := context.WithTimeout(ctx, canvasKeyframeExtractTimeout)
	defer cancel()
	cmd := exec.CommandContext(
		extractCtx,
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-ss", strconv.FormatFloat(in.Timestamp, 'f', 3, 64),
		"-i", inputPath,
		"-frames:v", "1",
		"-f", "image2",
		"-vcodec", "png",
		"-y",
		tmpPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = output
		return ProjectFileAsset{}, ErrCanvasKeyframeExtraction
	}

	frameBytes, err := os.ReadFile(tmpPath)
	if err != nil {
		return ProjectFileAsset{}, err
	}
	if len(frameBytes) == 0 {
		return ProjectFileAsset{}, ErrCanvasKeyframeExtraction
	}

	title := buildKeyframeAssetTitle(in.Timestamp, in.SourceTitle)
	metadata := model.JSONMap{
		"source":       "canvas",
		"operation":    "extract-keyframe",
		"sourceUrl":    in.VideoURL,
		"timestamp":    in.Timestamp,
		"sourceFormat": "video",
		"title":        title,
	}
	if strings.TrimSpace(in.SourceElementID) != "" {
		metadata["sourceElementId"] = strings.TrimSpace(in.SourceElementID)
		metadata["elementId"] = strings.TrimSpace(in.SourceElementID)
	}
	if strings.TrimSpace(in.SourceTaskID) != "" {
		metadata["sourceTaskId"] = strings.TrimSpace(in.SourceTaskID)
	}
	if strings.TrimSpace(in.SourceTitle) != "" {
		metadata["sourceTitle"] = strings.TrimSpace(in.SourceTitle)
		metadata["prompt"] = fmt.Sprintf("Extracted keyframe at %.2fs from %s", in.Timestamp, strings.TrimSpace(in.SourceTitle))
	}

	return UploadAsset(ctx, db, storage, uid, projectID, UploadAssetInput{
		Kind:              "extracted-keyframe",
		OriginalName:      fmt.Sprintf("keyframe-%.2fs.png", in.Timestamp),
		HeaderContentType: "image/png",
		FileBytes:         frameBytes,
		MaxSize:           16 * 1024 * 1024,
		Metadata:          metadata,
	})
}

func buildKeyframeAssetTitle(timestamp float64, sourceTitle string) string {
	trimmed := strings.TrimSpace(sourceTitle)
	if trimmed == "" {
		return fmt.Sprintf("Keyframe %.2fs", timestamp)
	}
	if len([]rune(trimmed)) > 80 {
		runes := []rune(trimmed)
		trimmed = string(runes[:80])
	}
	return fmt.Sprintf("Keyframe %.2fs · %s", timestamp, trimmed)
}

func localMediaPathForCanvasURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrCanvasInvalidVideoURL
	}
	pathValue := parsed.Path
	if pathValue == "" && !parsed.IsAbs() {
		pathValue = raw
	}
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" || strings.Contains(pathValue, "\x00") {
		return "", ErrCanvasInvalidVideoURL
	}

	var rel string
	switch {
	case strings.HasPrefix(pathValue, "/uploads/"):
		rel = strings.TrimPrefix(pathValue, "/")
	case strings.HasPrefix(pathValue, "/generations/"):
		rel = filepath.Join("uploads", strings.TrimPrefix(pathValue, "/"))
	case strings.HasPrefix(pathValue, "/canvas/"):
		rel = filepath.Join("uploads", strings.TrimPrefix(pathValue, "/"))
	default:
		return "", ErrCanvasInvalidVideoURL
	}
	clean := filepath.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", ErrCanvasInvalidVideoURL
	}
	return clean, nil
}

// CreateAssetInput is the service-layer DTO for CreateAsset.
type CreateAssetInput struct {
	Kind      string
	MimeType  string
	SizeBytes int64
	Width     int
	Height    int
	URL       string
	ThumbURL  string
	Metadata  model.JSONMap
}

func NormalizeCreateAssetInput(in CreateAssetInput) (CreateAssetInput, error) {
	out := in
	out.Kind = NormalizeAssetKind(in.Kind)
	out.MimeType = strings.TrimSpace(in.MimeType)
	out.URL = strings.TrimSpace(in.URL)
	out.ThumbURL = strings.TrimSpace(in.ThumbURL)

	if len(out.Kind) > maxCanvasAssetKindLength || hasControlChars(out.Kind) {
		return CreateAssetInput{}, ErrCanvasAssetInvalidInput
	}
	if !isAllowedCanvasAssetMimeType(out.MimeType) {
		return CreateAssetInput{}, ErrCanvasAssetInvalidInput
	}
	if out.SizeBytes < 0 || out.SizeBytes > maxCanvasAssetSizeBytes {
		return CreateAssetInput{}, ErrCanvasAssetInvalidInput
	}
	if out.Width < 0 || out.Width > maxCanvasAssetDimension || out.Height < 0 || out.Height > maxCanvasAssetDimension {
		return CreateAssetInput{}, ErrCanvasAssetInvalidInput
	}
	if _, err := validateCanvasAssetReferenceURL(out.URL, true); err != nil {
		return CreateAssetInput{}, err
	}
	if out.ThumbURL != "" {
		if _, err := validateCanvasAssetReferenceURL(out.ThumbURL, false); err != nil {
			return CreateAssetInput{}, err
		}
	}
	if out.Metadata != nil {
		data, err := json.Marshal(out.Metadata)
		if err != nil || len(data) > maxCanvasAssetMetadataBytes {
			return CreateAssetInput{}, ErrCanvasAssetInvalidInput
		}
	}
	return out, nil
}

func NormalizeCanvasReferenceURL(rawURL string, required bool) (string, error) {
	value := strings.TrimSpace(rawURL)
	if _, err := validateCanvasAssetReferenceURL(value, required); err != nil {
		return "", err
	}
	return value, nil
}

// NormalizeOwnedCanvasReferenceURL is the stricter variant used by Canvas
// generation/editing requests. It accepts only assets that are already part of
// the caller's Canvas project (or a same-project local upload path), so provider
// calls stay reproducible and cannot borrow another user's media by URL.
func NormalizeOwnedCanvasReferenceURL(ctx context.Context, db *gorm.DB, uid int, projectID uint, rawURL string, required bool) (string, error) {
	value, err := NormalizeCanvasReferenceURL(rawURL, required)
	if err != nil || value == "" {
		return value, err
	}
	if uid <= 0 || projectID == 0 {
		return "", ErrCanvasAssetInvalidInput
	}
	if err := validateOwnedCanvasAssetReferenceURL(ctx, db, uid, projectID, value); err != nil {
		return "", err
	}
	return value, nil
}

func validateOwnedCanvasAssetReferenceURL(ctx context.Context, db *gorm.DB, uid int, projectID uint, value string) error {
	candidates := canvasAssetURLCandidates(value)
	tx := db.WithContext(ctx)

	project, err := loadCanvasAssetProjectForAccess(ctx, db, uid, projectID, false)
	if err != nil {
		return ErrCanvasAssetInvalidInput
	}

	var count int64
	if err := tx.Model(&model.GlobalAsset{}).
		Where("deleted_at IS NULL AND status <> ?", model.GlobalAssetStatusDeleted).
		Where("project_id = ?", projectID).
		Where("(url IN ? OR thumb_url IN ?)", candidates, candidates).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	pathValue := canvasAssetURLPath(value)
	if pathValue == "" {
		return ErrCanvasAssetInvalidInput
	}

	if isCanvasProjectAssetPath(pathValue, project.UUID) {
		return nil
	}
	return ErrCanvasAssetInvalidInput
}

func canvasAssetURLCandidates(value string) []string {
	candidates := []string{value}
	if pathValue := canvasAssetURLPath(value); pathValue != "" && pathValue != value {
		candidates = append(candidates, pathValue)
	}
	return candidates
}

func canvasAssetURLPath(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		return parsed.EscapedPath()
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value
	}
	return ""
}

// validateCanvasAssetReferenceURL accepts site-relative paths (which are
// served by our own app, no SSRF risk) and otherwise delegates to
// safehttp.ValidateURL. The previous implementation only checked literal
// IPs, so a hostname resolving to 127.0.0.1 (e.g. localtest.me) or to a
// private RFC1918 address would slip through. safehttp performs the DNS
// resolution and rejects unsafe address classes.
func validateCanvasAssetReferenceURL(rawURL string, required bool) (*url.URL, error) {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		if required {
			return nil, ErrCanvasAssetInvalidInput
		}
		return nil, nil
	}
	if len(value) > maxCanvasAssetURLLength || hasControlChars(value) {
		return nil, ErrCanvasAssetInvalidInput
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return &url.URL{Path: value}, nil
	}

	parsed, err := safehttp.ValidateURL(value)
	if err != nil {
		return nil, ErrCanvasAssetInvalidInput
	}
	return parsed, nil
}

func isAllowedCanvasAssetMimeType(mimeType string) bool {
	if mimeType == "" {
		return true
	}
	if len(mimeType) > maxCanvasAssetMimeTypeLength || hasControlChars(mimeType) {
		return false
	}
	return strings.HasPrefix(mimeType, "image/") ||
		strings.HasPrefix(mimeType, "video/") ||
		mimeType == "application/pdf"
}

func hasControlChars(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return r < 32 || r == 127
	})
}

// CreateAsset persists an asset referenced by URL (no file upload).
// Returns ErrCanvasAssetProjectNotFound if the project is missing or
// owned by someone else.
func CreateAsset(ctx context.Context, db *gorm.DB, uid int, projectID uint, in CreateAssetInput) (ProjectFileAsset, error) {
	tx := db.WithContext(ctx)
	normalized, err := NormalizeCreateAssetInput(in)
	if err != nil {
		return ProjectFileAsset{}, err
	}

	project, err := loadCanvasAssetProjectForAccess(ctx, db, uid, projectID, true)
	if err != nil {
		return ProjectFileAsset{}, err
	}
	if err := validateManagedCanvasCreateAssetURL(normalized.URL, uid, project); err != nil {
		return ProjectFileAsset{}, err
	}
	if err := ensureManagedCanvasCreateAssetExists(ctx, tx, normalized.URL, uid, projectID); err != nil {
		return ProjectFileAsset{}, err
	}
	if normalized.ThumbURL != "" {
		if err := validateManagedCanvasCreateAssetURL(normalized.ThumbURL, uid, project); err != nil {
			return ProjectFileAsset{}, err
		}
		if err := ensureManagedCanvasCreateAssetExists(ctx, tx, normalized.ThumbURL, uid, projectID); err != nil {
			return ProjectFileAsset{}, err
		}
	}

	var global *model.GlobalAsset

	if err := tx.Transaction(func(ttx *gorm.DB) error {
		var existing model.GlobalAsset
		if err := ttx.
			Where("project_id = ? AND deleted_at IS NULL AND status <> ?", projectID, model.GlobalAssetStatusDeleted).
			Where("(url = ? OR thumb_url = ?)", normalized.URL, normalized.URL).
			Order("updated_at DESC").
			First(&existing).Error; err == nil {
			global = &existing
			return assetLedgerService.New().UpsertCanvasProjectFileWithDB(ttx, global, &project)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var err error
		global, err = globalAssetService.NewRepository(ttx).CreateCanvasProjectFile(globalAssetInputFromCanvasCreate(uid, projectID, normalized))
		if err != nil {
			return err
		}
		if global == nil || global.Id == 0 {
			return ErrCanvasAssetInvalidInput
		}
		return assetLedgerService.New().UpsertCanvasProjectFileWithDB(ttx, global, &project)
	}); err != nil {
		return ProjectFileAsset{}, err
	}
	return projectFileAssetFromGlobal(global), nil
}

func validateManagedCanvasCreateAssetURL(value string, uid int, project model.CanvasProject) error {
	if isAbsoluteCanvasAssetURL(value) && !isAllowedManagedCanvasAssetHost(value) {
		return ErrCanvasAssetInvalidInput
	}
	pathValue := canvasAssetURLPath(value)
	if pathValue == "" {
		return ErrCanvasAssetInvalidInput
	}
	if isManagedCanvasAssetPath(pathValue, uid, project.UID, project.UUID) {
		return nil
	}
	return ErrCanvasAssetInvalidInput
}

func ensureManagedCanvasCreateAssetExists(ctx context.Context, db *gorm.DB, value string, uid int, projectID uint) error {
	candidates := []string{strings.TrimSpace(value)}
	if pathValue := canvasAssetURLPath(value); pathValue != "" && pathValue != strings.TrimSpace(value) {
		candidates = append(candidates, pathValue)
	}
	if projectID > 0 {
		var globalCount int64
		if err := db.WithContext(ctx).
			Model(&model.GlobalAsset{}).
			Where("deleted_at IS NULL AND status <> ?", model.GlobalAssetStatusDeleted).
			Where("project_id = ?", projectID).
			Where("(url IN ? OR thumb_url IN ?)", candidates, candidates).
			Count(&globalCount).Error; err != nil {
			return err
		}
		if globalCount > 0 {
			return nil
		}
	}

	if pathValue := canvasAssetURLPath(value); pathValue != "" {
		if localPath, err := localMediaPathForCanvasURL(pathValue); err == nil {
			if stat, statErr := os.Stat(localPath); statErr == nil && !stat.IsDir() {
				return nil
			}
		}
	}

	var count int64
	if err := db.WithContext(ctx).
		Model(&model.GenerationObject{}).
		Where("uid = ? AND status IN ?", uid, []int8{model.GenerationObjectStatusActive, model.GenerationObjectStatusHidden}).
		Where("(public_url IN ? OR source_url IN ?)", candidates, candidates).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return ErrCanvasAssetInvalidInput
}

func isManagedCanvasAssetPath(pathValue string, uid int, projectOwnerUID int, projectUUID string) bool {
	uidSegment := fmt.Sprintf("/uid/%d/", uid)
	ownerUIDSegment := fmt.Sprintf("/uid/%d/", projectOwnerUID)
	return isCanvasProjectAssetPath(pathValue, projectUUID) ||
		strings.HasPrefix(pathValue, canvasUploadURLPrefix(uid, projectUUID)) ||
		strings.HasPrefix(pathValue, canvasUploadURLPrefix(projectOwnerUID, projectUUID)) ||
		strings.HasPrefix(pathValue, "/uploads/generations"+uidSegment) ||
		strings.HasPrefix(pathValue, "/uploads/generations"+ownerUIDSegment) ||
		strings.HasPrefix(pathValue, "/generations"+uidSegment) ||
		strings.HasPrefix(pathValue, "/generations"+ownerUIDSegment) ||
		strings.HasPrefix(pathValue, "/uploads/references"+uidSegment)
}

func isCanvasProjectAssetPath(pathValue string, projectUUID string) bool {
	projectUUID = strings.TrimSpace(projectUUID)
	if pathValue == "" || projectUUID == "" {
		return false
	}
	return (strings.HasPrefix(pathValue, canvasUploadURLRoot) || strings.HasPrefix(pathValue, canvasUploadLegacyURLRoot)) &&
		strings.Contains(pathValue, "/"+projectUUID+"/")
}

func isAbsoluteCanvasAssetURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.IsAbs()
}

func isAllowedManagedCanvasAssetHost(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	for _, rawBase := range []string{
		globals.GraConf.System.BackendURL,
		globals.GraConf.System.FrontendURL,
		globals.GraConf.Generator.Storage.Local.URLPrefix,
		globals.GraConf.Generator.Storage.R2.CDNUrl,
		globals.GraConf.Generator.Storage.R2.PublicURL,
	} {
		base, err := url.Parse(strings.TrimSpace(rawBase))
		if err != nil || base == nil || base.Hostname() == "" {
			continue
		}
		if strings.EqualFold(host, strings.TrimSpace(base.Hostname())) {
			return true
		}
	}
	return false
}

func globalAssetInputFromCanvasCreate(uid int, projectID uint, normalized CreateAssetInput) globalAssetService.CanvasProjectFileInput {
	thumbURL := normalized.ThumbURL
	if thumbURL == "" {
		thumbURL = normalized.URL
	}
	return globalAssetService.CanvasProjectFileInput{
		UID:           uid,
		ProjectID:     projectID,
		SourceItemKey: canvasProjectFileSourceKey("url", projectID, normalized.URL),
		URL:           normalized.URL,
		ThumbURL:      thumbURL,
		MimeType:      normalized.MimeType,
		SizeBytes:     normalized.SizeBytes,
		Width:         normalized.Width,
		Height:        normalized.Height,
		Kind:          normalized.Kind,
		VariantType:   model.GlobalAssetVariantOriginal,
		Metadata:      normalized.Metadata,
	}
}

// UploadAssetInput bundles a multipart upload for UploadAsset.
type UploadAssetInput struct {
	Kind              string
	Width             int
	Height            int
	OriginalName      string
	HeaderContentType string
	FileBytes         []byte
	MaxSize           int64
	Metadata          model.JSONMap
}

// UploadAsset writes the bytes via AssetStorage, then persists the
// matching asset row inside a transaction. If the TX fails the
// storage cleanup fn is invoked so we never leak files.
//
// Validation errors (too large, unsupported MIME, empty file) come
// back as sentinel errors so handlers can map them to 400 instead of
// 500. The MaxSize field falls back to a hard 16MiB floor to match
// the original handler's defensive minimum.
func UploadAsset(ctx context.Context, db *gorm.DB, storage AssetStorage, uid int, projectID uint, in UploadAssetInput) (ProjectFileAsset, error) {
	if len(in.FileBytes) == 0 {
		return ProjectFileAsset{}, ErrCanvasAssetEmptyFile
	}

	maxSize := in.MaxSize
	if maxSize < 16*1024*1024 {
		maxSize = 16 * 1024 * 1024
	}
	if int64(len(in.FileBytes)) > maxSize {
		return ProjectFileAsset{}, ErrCanvasAssetTooLarge
	}

	contentType, ext, ok := ClassifyUploadedContent(in.HeaderContentType, in.FileBytes)
	if !ok {
		return ProjectFileAsset{}, ErrCanvasAssetUnsupportedType
	}
	if in.Width < 0 || in.Width > maxCanvasAssetDimension || in.Height < 0 || in.Height > maxCanvasAssetDimension {
		return ProjectFileAsset{}, ErrCanvasAssetInvalidInput
	}
	originalName := strings.TrimSpace(filepath.Base(in.OriginalName))
	if originalName == "." || originalName == string(filepath.Separator) {
		originalName = ""
	}
	if len(originalName) > maxCanvasAssetOriginalNameBytes || hasControlChars(originalName) {
		return ProjectFileAsset{}, ErrCanvasAssetInvalidInput
	}
	if len(in.Kind) > maxCanvasAssetKindLength || hasControlChars(in.Kind) {
		return ProjectFileAsset{}, ErrCanvasAssetInvalidInput
	}

	tx := db.WithContext(ctx)

	project, err := loadCanvasAssetProjectForAccess(ctx, db, uid, projectID, true)
	if err != nil {
		return ProjectFileAsset{}, err
	}

	filename := GenerateAssetFilename(ext)
	stored, err := storage.Store(ctx, uid, project.UUID, filename, in.FileBytes)
	if err != nil {
		return ProjectFileAsset{}, err
	}
	// P1-data #18: every error-return path AFTER Store must invoke
	// stored.Cleanup, or the bytes on disk become an orphan only the
	// 7-day C-06 cron will eventually sweep. The validate-then-return
	// branches below used to skip cleanup; this defer is the safety
	// net so a new return path can't reintroduce the leak. Cleared on
	// the success path so the success commit owns the file.
	cleanupOnError := stored.Cleanup
	defer func() {
		if cleanupOnError != nil {
			cleanupOnError()
		}
	}()

	metadata := model.JSONMap{
		"originalName": originalName,
		"contentType":  contentType,
	}
	for key, value := range in.Metadata {
		if strings.TrimSpace(key) == "" {
			return ProjectFileAsset{}, ErrCanvasAssetInvalidInput
		}
		metadata[key] = value
	}
	if encoded, err := json.Marshal(metadata); err != nil || len(encoded) > maxCanvasAssetMetadataBytes {
		return ProjectFileAsset{}, ErrCanvasAssetInvalidInput
	}
	globalInput := globalAssetService.CanvasProjectFileInput{
		UID:           uid,
		ProjectID:     projectID,
		SourceItemKey: canvasProjectFileSourceKey("upload", projectID, filename),
		URL:           stored.URL,
		ThumbURL:      stored.URL,
		MimeType:      contentType,
		SizeBytes:     int64(len(in.FileBytes)),
		Width:         in.Width,
		Height:        in.Height,
		ContentHash:   hashCanvasAssetBytes(in.FileBytes),
		Kind:          NormalizeAssetKind(in.Kind),
		VariantType:   variantTypeFromCanvasAssetKind(in.Kind),
		Metadata:      metadata,
	}
	var global *model.GlobalAsset

	if err := tx.Transaction(func(ttx *gorm.DB) error {
		var err error
		global, err = globalAssetService.NewRepository(ttx).CreateCanvasProjectFile(globalInput)
		if err != nil {
			return err
		}
		if global == nil || global.Id == 0 {
			return ErrCanvasAssetInvalidInput
		}
		return assetLedgerService.New().UpsertCanvasProjectFileWithDB(ttx, global, &project)
	}); err != nil {
		// Cleanup runs via the defer above; no need to call it here.
		return ProjectFileAsset{}, err
	}
	// Success: the file is owned by the persisted GlobalAsset row.
	// Disarm the deferred cleanup so the bytes survive past return.
	cleanupOnError = nil
	return projectFileAssetFromGlobal(global), nil
}

func canvasProjectFileSourceKey(prefix string, projectID uint, value string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", strings.TrimSpace(prefix), projectID, strings.TrimSpace(value))))
	return strings.TrimSpace(prefix) + ":" + hex.EncodeToString(sum[:])
}

func hashCanvasAssetBytes(fileBytes []byte) string {
	if len(fileBytes) == 0 {
		return ""
	}
	sum := sha256.Sum256(fileBytes)
	return hex.EncodeToString(sum[:])
}

func variantTypeFromCanvasAssetKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "extracted-keyframe", "keyframe":
		return model.GlobalAssetVariantKeyframe
	case "thumbnail", "thumb":
		return model.GlobalAssetVariantThumb
	default:
		return model.GlobalAssetVariantOriginal
	}
}

func loadCanvasAssetProjectForAccess(ctx context.Context, db *gorm.DB, uid int, projectID uint, requireEdit bool) (model.CanvasProject, error) {
	access, err := projectService.NewRepository(db.WithContext(ctx)).ResolveAccess(projectID, uint(uid))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasProject{}, ErrCanvasAssetProjectNotFound
	}
	if err != nil {
		return model.CanvasProject{}, err
	}
	if requireEdit {
		if !access.CanEdit() {
			return model.CanvasProject{}, ErrCanvasAssetProjectNotFound
		}
	} else if !access.CanView() {
		return model.CanvasProject{}, ErrCanvasAssetProjectNotFound
	}
	return *access.Project, nil
}

// ListAssetsInput is the service-layer DTO for ListAssets.
type ListAssetsInput struct {
	Page    int
	Limit   int
	NoCount bool
	Search  string
	Kind    string
}

// ProjectFileAsset is the Canvas-facing file DTO backed by w_global_asset.
// Id is the canonical file id for new callers and points to w_global_asset.id.
type ProjectFileAsset struct {
	Id            uint          `json:"id"`
	GlobalAssetID uint          `json:"globalAssetId,omitempty"`
	CreatedAt     time.Time     `json:"createdAt"`
	UpdatedAt     time.Time     `json:"updatedAt"`
	UID           int           `json:"uid"`
	ProjectID     *uint         `json:"projectId"`
	Kind          string        `json:"kind"`
	MimeType      string        `json:"mimeType"`
	SizeBytes     int64         `json:"sizeBytes"`
	Width         int           `json:"width"`
	Height        int           `json:"height"`
	URL           string        `json:"url"`
	ThumbURL      string        `json:"thumbUrl"`
	Metadata      model.JSONMap `json:"metadata"`
	Source        string        `json:"source,omitempty"`
	SourceTable   string        `json:"sourceTable,omitempty"`
	SourceID      uint64        `json:"sourceId,omitempty"`
	VariantType   string        `json:"variantType,omitempty"`
}

// normalizeListAssetsInput clamps page/limit into the handler's
// original defaults (page≥1, limit∈[1,200], default 50). Kept as a
// helper so the pagination contract stays pinned in one place.
func normalizeListAssetsInput(in ListAssetsInput) ListAssetsInput {
	out := in
	if out.Page < 1 {
		out.Page = 1
	}
	if out.Limit < 1 {
		out.Limit = 50
	}
	if out.Limit > 200 {
		out.Limit = 200
	}
	out.Search = strings.TrimSpace(out.Search)
	if len(out.Search) > 120 {
		out.Search = out.Search[:120]
	}
	out.Kind = strings.TrimSpace(out.Kind)
	switch out.Kind {
	case "", "image", "video", "upload", "generation":
	default:
		out.Kind = ""
	}
	return out
}

// ListProjectFileAssets is the global-asset backed Canvas files list.
func ListProjectFileAssets(ctx context.Context, db *gorm.DB, uid int, projectID uint, in ListAssetsInput) (model.PaginatedResult[ProjectFileAsset], error) {
	in = normalizeListAssetsInput(in)
	tx := db.WithContext(ctx)

	if _, err := loadCanvasAssetProjectForAccess(ctx, db, uid, projectID, false); err != nil {
		return model.PaginatedResult[ProjectFileAsset]{}, err
	}

	globalQuery := tx.Model(&model.GlobalAsset{}).
		Where("project_id = ? AND deleted_at IS NULL", projectID).
		Where("status <> ?", model.GlobalAssetStatusDeleted)
	globalQuery = applyProjectFileAssetFilters(globalQuery, in)

	total := int64(0)
	if !in.NoCount {
		if err := globalQuery.Count(&total).Error; err != nil {
			return model.PaginatedResult[ProjectFileAsset]{}, err
		}
	}

	var globalRows []model.GlobalAsset
	if err := globalQuery.Order("updated_at DESC").
		Offset((in.Page - 1) * in.Limit).
		Limit(in.Limit).
		Find(&globalRows).Error; err != nil {
		return model.PaginatedResult[ProjectFileAsset]{}, err
	}

	items := make([]ProjectFileAsset, 0, len(globalRows))
	for i := range globalRows {
		item := projectFileAssetFromGlobal(&globalRows[i])
		items = append(items, item)
	}

	return model.PaginatedResult[ProjectFileAsset]{
		Items: items,
		Total: total,
		Page:  in.Page,
		Limit: in.Limit,
	}, nil
}

func applyProjectFileAssetFilters(query *gorm.DB, in ListAssetsInput) *gorm.DB {
	if in.Kind == "image" {
		query = query.Where("(kind = ? OR mime_type LIKE ?)", model.GlobalAssetKindImage, "image/%")
	} else if in.Kind == "video" {
		query = query.Where("(kind = ? OR mime_type LIKE ?)", model.GlobalAssetKindVideo, "video/%")
	} else if in.Kind == "upload" || in.Kind == "generation" {
		query = query.Where("source = ?", in.Kind)
	}
	if in.Search != "" {
		like := "%" + strings.ReplaceAll(in.Search, "%", "\\%") + "%"
		query = query.Where("(url LIKE ? OR thumb_url LIKE ? OR mime_type LIKE ? OR kind LIKE ? OR source LIKE ?)", like, like, like, like, like)
	}
	return query
}

func projectFileAssetFromGlobal(asset *model.GlobalAsset) ProjectFileAsset {
	if asset == nil {
		return ProjectFileAsset{}
	}
	item := ProjectFileAsset{
		Id:            asset.Id,
		GlobalAssetID: asset.Id,
		CreatedAt:     asset.CreatedAt,
		UpdatedAt:     asset.UpdatedAt,
		UID:           asset.UID,
		ProjectID:     asset.ProjectID,
		Kind:          asset.Kind,
		MimeType:      asset.MimeType,
		SizeBytes:     asset.SizeBytes,
		Width:         asset.Width,
		Height:        asset.Height,
		URL:           asset.URL,
		ThumbURL:      asset.ThumbURL,
		Metadata:      cloneCanvasJSONMap(asset.Metadata),
		Source:        asset.Source,
		SourceTable:   asset.SourceTable,
		SourceID:      asset.SourceID,
		VariantType:   asset.VariantType,
	}
	if item.Metadata == nil {
		item.Metadata = model.JSONMap{}
	}
	item.Metadata["globalAssetId"] = asset.Id
	if item.ThumbURL == "" {
		item.ThumbURL = item.URL
	}
	return item
}

func projectFileAssetMergeKey(item ProjectFileAsset) string {
	if strings.TrimSpace(item.URL) != "" {
		return "url:" + strings.TrimSpace(item.URL)
	}
	if item.GlobalAssetID > 0 {
		return fmt.Sprintf("global:%d", item.GlobalAssetID)
	}
	return ""
}

func cloneCanvasJSONMap(in model.JSONMap) model.JSONMap {
	if len(in) == 0 {
		return nil
	}
	out := make(model.JSONMap, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
