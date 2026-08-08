package canvas

// canvas_paths.go · URL-prefix helpers for canvas-uploaded files.
// Extracted on 2026-05-15 to consolidate the 5 sites across
// canvas_project_service.go + canvas_asset_service.go that built the
// `/uploads/canvas/uid/{uid}/{uuid}/` prefix inline. Drift surface:
// if the upload root ever moves (e.g. behind a CDN path rewrite),
// the 5 inline fmt.Sprintf calls would diverge silently. Consolidate
// behind a single helper + a constant the legacy-compat check can
// reference too.
//
// Two roots in production today:
//   - canvasUploadURLRoot       — the canonical /uploads/canvas/uid/
//   - canvasUploadLegacyURLRoot — the legacy /canvas/uid/ prefix that
//     pre-2025 file paths still emit. isManagedCanvasAssetPath +
//     isSharedProjectDocumentAssetPath both accept either root so
//     migrated historical projects keep working.

import "fmt"

const (
	canvasUploadURLRoot       = "/uploads/canvas/uid/"
	canvasUploadLegacyURLRoot = "/canvas/uid/"
)

// canvasUploadURLPrefix returns the canonical URL prefix for files
// uploaded into a specific (uid, projectUUID) pair. Always trailing
// slash so the caller can use strings.HasPrefix safely.
func canvasUploadURLPrefix(uid int, projectUUID string) string {
	return fmt.Sprintf("%s%d/%s/", canvasUploadURLRoot, uid, projectUUID)
}

// canvasUploadLegacyURLPrefix is the legacy-root equivalent of
// canvasUploadURLPrefix. Kept so migrated historical projects whose
// document JSON still embeds /canvas/uid/... paths keep resolving.
func canvasUploadLegacyURLPrefix(uid int, projectUUID string) string {
	return fmt.Sprintf("%s%d/%s/", canvasUploadLegacyURLRoot, uid, projectUUID)
}
