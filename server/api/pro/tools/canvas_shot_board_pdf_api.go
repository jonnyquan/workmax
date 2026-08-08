package tools

// canvas_shot_board_pdf_api.go — Task #5 endpoint for exporting a
// project's ShotBoard view as a printable PDF. The old /export/ppt
// reservation was removed with the PPTX/MP4 export scope cut; this
// endpoint is the supported printable shot-board export surface.
//
// Surface:
//
//   GET /api/tools/canvas/projects/:id/export/shot-board-pdf
//        Streams `application/pdf` bytes inline. The browser's
//        default behavior on this content-type is to display the
//        PDF; users who want a download can right-click → "Save".
//        Filename hint is set via Content-Disposition.
//
// Auth + ownership: project must belong to the caller (collapses
// cross-tenant and missing to one 404, IDOR-safe — same posture
// as the decision-log endpoint).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/service/project"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// shotBoardPDFFetchTimeout caps per-image HTTP fetch. Project
// shot boards typically reference public CDN images that respond
// in < 1s; the cap defends against a slow / hung CDN turning a
// "click to print" into a 30-second hang. Individual image
// failures fall through to the placeholder box.
const shotBoardPDFFetchTimeout = 4 * time.Second

// ExportShotBoardPDF assembles the project's image elements into
// a printable PDF and streams it back as application/pdf.
//
// Error shapes match the rest of the canvas API:
//   - missing / cross-tenant project → 404 ("Project not found")
//   - project has no image elements → 422 ("No shots to export")
//   - any other failure → 500 ("Failed to generate PDF")
func (a *CanvasApi) ExportShotBoardPDF(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	db := globals.GraDBs["system"]
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database unavailable"})
		return
	}

	projectRow, err := project.NewRepository(db).LoadByIDForOwner(uint(projectID), uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondCanvasError(c, "Project not found")
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load project"})
		return
	}

	// Normalise the document before extracting entries so the
	// schema is in the shape extractShotEntries expects (v2). The
	// migrator is a no-op for already-v2 docs; it's still cheap
	// and keeps this handler tolerant of older project rows.
	doc, schemaErr := canvasService.NormalizeCanvasDocumentForStorage(projectRow.Document)
	if schemaErr != nil {
		if respondCanvasSchemaErrorIfApplicable(c, schemaErr) {
			return
		}
		respondCanvasError(c, "Schema validation failed: "+schemaErr.Error())
		return
	}

	entries := canvasService.ExtractShotEntries(doc)
	if len(entries) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"code":    http.StatusUnprocessableEntity,
			"message": "No shots to export",
			"data": gin.H{
				"success":   false,
				"errorCode": "CANVAS_SHOT_BOARD_EMPTY",
				"message":   "Project has no image elements to print",
			},
		})
		return
	}

	// Per-request context so a slow client disconnect cancels the
	// in-flight image fetches.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	fetcher := canvasService.HTTPImageFetcher(shotBoardPDFFetchTimeout)
	pdfBytes, err := canvasService.BuildShotBoardPDF(ctx, projectRow.Title, entries, fetcher)
	if err != nil {
		if errors.Is(err, canvasService.ErrNoShots) {
			// Defensive — extract returned 0 entries should have
			// caught this above, but double-cover the contract.
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "No shots to export"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	filename := buildShotBoardPDFFilename(projectRow.Title, projectID)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if _, err := c.Writer.Write(pdfBytes); err != nil {
		globals.Warn(fmt.Sprintf("[Canvas ShotBoard PDF] write failed for project %d: %v", projectID, err))
	}
}

// buildShotBoardPDFFilename produces a safe, descriptive filename
// hint. Title goes through a strict ASCII-only filter so the
// Content-Disposition header doesn't need RFC 5987 encoding; the
// project id keeps the filename unique even when titles collide
// or get sanitised to "".
func buildShotBoardPDFFilename(title string, projectID uint64) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "shot-board"
	}
	return fmt.Sprintf("%s-%d.pdf", slug, projectID)
}
