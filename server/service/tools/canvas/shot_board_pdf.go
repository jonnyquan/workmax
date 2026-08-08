// ShotBoard PDF export — Task #5, borrowable from wonderunit/storyboarder
// + colorAi/comfyui-storyboard. Owns the GET .../export/shot-board-pdf
// surface.
//
// PPTX export was retired entirely 2026-05-15 (owner product decision —
// C-07 closed as decided-not-to-do): WorkMax focuses on per-asset
// download + in-canvas preview instead of multi-clip composition or
// editable-deck packaging. The earlier 501-stub /export/ppt route was
// deleted in the same change.
//
// Why PDF is the only "deliverable" export:
//   - WorkMax users primarily ask for "send a printable shot board to
//     a client / director" — a paper-shaped artifact.
//   - PDF generation is a single mature pure-Go dep (go-pdf/fpdf).
//   - PDFs are universally viewable.
//
// Structure of this file:
//
//   - extractShotEntries: pure projection from a canvas document
//     JSONMap to a flat []ShotPDFEntry slice. Walks doc.elements,
//     picks image-shaped rows, decodes prompt / src / aspectRatio
//     from the loose v1+v2 wire shape.
//   - BuildShotBoardPDF: pure PDF assembler. Takes a slice of
//     entries and an image fetcher func; returns []byte. The
//     fetcher is injected so callers can stub it in tests and
//     swap implementations (http GET in prod, fs read in dev,
//     no-op for tests).
//   - HTTPImageFetcher: the production fetcher. Time-budgeted HTTP
//     GET that tolerates failure (the PDF still renders, just
//     without that image).

package canvas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"server/model"
)

// ShotPDFEntry is one shot's worth of data in the PDF. Optional
// fields are zero values when absent on the source element.
type ShotPDFEntry struct {
	ID          string
	Prompt      string
	Src         string
	AspectRatio string
	// Index is the 1-based position in the source slice; used for
	// the "Shot N" label in the PDF cell.
	Index int
}

// ShotBoardImageFetcher takes a src URL/path and returns raw image
// bytes plus an image format ("png" | "jpg" | "jpeg"). Returns
// nil bytes + nil error when the image is unavailable; the PDF
// rendering treats that as "draw the placeholder box for this
// cell". Non-nil error means the fetcher itself broke
// (configuration error, panic guard tripped) — the caller may
// surface it as a 500.
//
// Production fetcher (HTTPImageFetcher below) maps any remote
// failure to (nil, nil) so the PDF always renders.
type ShotBoardImageFetcher func(ctx context.Context, src string) (data []byte, format string, err error)

// MaxShotsPerPDF caps the total number of shots a single export
// can carry. Real shot boards run 6–60 shots; a project with
// 500+ image elements is either a stress test or a misuse that
// would produce an unwieldy PDF.
const MaxShotsPerPDF = 200

// extractShotEntries walks the document's elements (the loose v1+v2
// JSONMap shape — see CanvasElementV2.V1Fields header) and pulls
// out the image elements in source order. Non-image elements
// (text/shape/drawing/frame/video) are skipped; the shot board
// view is image-first by design.
func extractShotEntries(doc model.JSONMap) []ShotPDFEntry {
	if doc == nil {
		return nil
	}
	rawElements, ok := doc["elements"]
	if !ok || rawElements == nil {
		return nil
	}
	elements, ok := rawElements.([]interface{})
	if !ok {
		return nil
	}
	out := make([]ShotPDFEntry, 0, len(elements))
	for _, raw := range elements {
		row, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		kind := jsonMapString(model.JSONMap(row), "type")
		if kind != "image" {
			continue
		}
		// `visible` defaults to true; an explicitly-hidden element
		// shouldn't appear on the printable board.
		if visible, ok := row["visible"].(bool); ok && !visible {
			continue
		}
		entry := ShotPDFEntry{
			ID:          jsonMapString(model.JSONMap(row), "id"),
			Prompt:      jsonMapString(model.JSONMap(row), "prompt"),
			Src:         jsonMapString(model.JSONMap(row), "src"),
			AspectRatio: jsonMapString(model.JSONMap(row), "aspectRatio"),
			Index:       len(out) + 1,
		}
		out = append(out, entry)
		if len(out) >= MaxShotsPerPDF {
			break
		}
	}
	return out
}

// ExtractShotEntries is the exported wrapper. The pure-function
// internal stays lowercase because tests can reach it via the
// same package; this surface is what the API handler / future
// callers use.
func ExtractShotEntries(doc model.JSONMap) []ShotPDFEntry {
	return extractShotEntries(doc)
}

// BuildShotBoardPDF assembles the PDF. Layout: A4 portrait, 2
// columns × 3 rows = 6 shots/page, header on each page with the
// project title + page number. Per-cell layout: thumbnail (top
// ~60% of cell height), shot label + prompt below.
//
// fetcher may be nil → all cells render with placeholder boxes
// (useful for "draft a board with no images attached yet").
//
// Returns ErrNoShots when entries is empty — caller decides
// whether to 404 ("no images to print") or 422 ("project has no
// shot board yet"). Other errors are fpdf-side (rare: invalid
// font load), surfaced as 500.
func BuildShotBoardPDF(
	ctx context.Context,
	title string,
	entries []ShotPDFEntry,
	fetcher ShotBoardImageFetcher,
) ([]byte, error) {
	if len(entries) == 0 {
		return nil, ErrNoShots
	}

	const (
		pageMarginX  = 14.0 // mm
		pageMarginY  = 18.0
		headerBand   = 12.0
		footerBand   = 8.0
		cellGap      = 6.0
		colsPerPage  = 2
		rowsPerPage  = 3
		shotsPerPage = colsPerPage * rowsPerPage
	)

	pdf := fpdf.New("P", "mm", "A4", "")
	pageW, pageH := pdf.GetPageSize()
	contentW := pageW - 2*pageMarginX
	contentH := pageH - 2*pageMarginY - headerBand - footerBand
	cellW := (contentW - cellGap*(colsPerPage-1)) / colsPerPage
	cellH := (contentH - cellGap*(rowsPerPage-1)) / rowsPerPage
	imageH := cellH * 0.62
	textH := cellH - imageH - 2.0 // small gap between image + caption

	now := time.Now().UTC().Format("2006-01-02 15:04 UTC")
	pageCount := (len(entries) + shotsPerPage - 1) / shotsPerPage

	displayTitle := strings.TrimSpace(title)
	if displayTitle == "" {
		displayTitle = "Untitled Project"
	}

	for page := 0; page < pageCount; page++ {
		pdf.AddPage()

		// Header — project title (left) + timestamp + page count (right).
		pdf.SetFont("Helvetica", "B", 12)
		pdf.SetXY(pageMarginX, pageMarginY)
		pdf.CellFormat(contentW, 6, sanitizeForPDF(displayTitle), "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetXY(pageMarginX, pageMarginY+6)
		pdf.CellFormat(contentW/2, 4, now, "", 0, "L", false, 0, "")
		pdf.CellFormat(contentW/2, 4, fmt.Sprintf("Page %d / %d", page+1, pageCount), "", 0, "R", false, 0, "")

		gridTop := pageMarginY + headerBand

		startIdx := page * shotsPerPage
		endIdx := startIdx + shotsPerPage
		if endIdx > len(entries) {
			endIdx = len(entries)
		}
		for i := startIdx; i < endIdx; i++ {
			cellIdx := i - startIdx
			col := cellIdx % colsPerPage
			row := cellIdx / colsPerPage
			cellX := pageMarginX + float64(col)*(cellW+cellGap)
			cellY := gridTop + float64(row)*(cellH+cellGap)

			// Border + image area + caption area.
			pdf.SetDrawColor(200, 200, 200)
			pdf.SetLineWidth(0.2)
			pdf.Rect(cellX, cellY, cellW, cellH, "D")

			renderImageBox(ctx, pdf, fetcher, entries[i], cellX+1, cellY+1, cellW-2, imageH-2)

			// Caption block: "Shot N" label + prompt (clipped).
			captionX := cellX + 2
			captionY := cellY + imageH + 1
			pdf.SetXY(captionX, captionY)
			pdf.SetFont("Helvetica", "B", 9)
			pdf.SetTextColor(40, 40, 40)
			pdf.CellFormat(cellW-4, 4, fmt.Sprintf("Shot %d", entries[i].Index), "", 0, "L", false, 0, "")

			pdf.SetFont("Helvetica", "", 8)
			pdf.SetTextColor(80, 80, 80)
			pdf.SetXY(captionX, captionY+5)
			pdf.MultiCell(cellW-4, 3.4, sanitizeForPDF(entries[i].Prompt), "", "L", false)
			_ = textH // textH reserved for future single-line truncation logic
		}
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("shot-board pdf: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// renderImageBox tries to draw the entry's image into the given
// rectangle. On any failure (no src, fetcher nil, fetcher
// returned (nil, ""), pdf.RegisterImage error) it falls back to
// drawing a hatched placeholder so the cell stays visually
// distinct from a successfully-rendered shot. The PDF never
// fails because of a single broken image.
func renderImageBox(
	ctx context.Context,
	pdf *fpdf.Fpdf,
	fetcher ShotBoardImageFetcher,
	entry ShotPDFEntry,
	x, y, w, h float64,
) {
	if entry.Src != "" && fetcher != nil {
		data, format, err := fetcher(ctx, entry.Src)
		if err == nil && len(data) > 0 && format != "" {
			// Register the image under a per-cell unique name so
			// repeated cells with the same src don't collide AND
			// so a later identical src reuses the registered image
			// (fpdf caches by name internally).
			name := "shot-img-" + entry.ID
			if name == "shot-img-" {
				name = fmt.Sprintf("shot-img-anon-%d", entry.Index)
			}
			pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{
				ImageType: format,
				ReadDpi:   false,
			}, bytes.NewReader(data))
			if pdf.Err() {
				// Reset error and fall through to placeholder so
				// one bad image doesn't abort the whole PDF.
				pdf.ClearError()
			} else {
				pdf.ImageOptions(name, x, y, w, h, false, fpdf.ImageOptions{
					ImageType: format,
					ReadDpi:   false,
				}, 0, "")
				if !pdf.Err() {
					return
				}
				pdf.ClearError()
			}
		}
	}

	// Placeholder: light grey box with "no image" centred.
	pdf.SetFillColor(245, 245, 245)
	pdf.Rect(x, y, w, h, "F")
	pdf.SetTextColor(160, 160, 160)
	pdf.SetFont("Helvetica", "I", 9)
	pdf.SetXY(x, y+h/2-2)
	pdf.CellFormat(w, 4, "(no image)", "", 0, "C", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
}

// sanitizeForPDF replaces characters that the default Helvetica
// (Latin-1 only) can't render with their ASCII fallback. Without
// this, CJK / em-dash / smart-quote characters render as "?" or
// blank. Truncates at 280 chars so a runaway prompt doesn't
// overflow the cell — long prompts already render in MultiCell
// with line wrapping, but a hard truncation prevents the
// "10kb prompt fills 3 PDF pages" pathology.
func sanitizeForPDF(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	const maxLen = 280
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '‘' || r == '’': // smart quotes → ASCII
			b.WriteByte('\'')
		case r == '“' || r == '”':
			b.WriteByte('"')
		case r == '–' || r == '—': // en/em dash → hyphen
			b.WriteByte('-')
		case r == '…': // ellipsis → three dots
			b.WriteString("...")
		case r < 0x80:
			b.WriteRune(r)
		case r < 0x100: // Latin-1 supplement passes through
			b.WriteRune(r)
		default:
			// CJK and other non-Latin glyphs render as nothing on
			// the default font; replace with a space so word
			// boundaries survive even if the glyph doesn't.
			b.WriteByte(' ')
		}
		if b.Len() >= maxLen {
			b.WriteString("...")
			break
		}
	}
	return b.String()
}

// ErrNoShots is returned by BuildShotBoardPDF when the document
// carries no image-shaped elements to print.
var ErrNoShots = errors.New("shot-board pdf: project has no image elements")

// HTTPImageFetcher returns a ShotBoardImageFetcher that pulls
// image bytes via HTTP GET with a per-request time budget. It is
// the production fetcher; tests use a stub that returns canned
// bytes.
//
// Failures (network error, 4xx/5xx, unsupported content-type) all
// resolve to (nil, "", nil) so the PDF renders without the image.
// The handler never surfaces a per-image fetch failure as a 5xx.
func HTTPImageFetcher(timeout time.Duration) ShotBoardImageFetcher {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, src string) ([]byte, string, error) {
		src = strings.TrimSpace(src)
		if src == "" || !(strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")) {
			return nil, "", nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return nil, "", nil
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", nil
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", nil
		}
		format := imageFormatFromContentType(resp.Header.Get("Content-Type"))
		if format == "" {
			format = imageFormatFromURL(src)
		}
		if format == "" {
			return nil, "", nil
		}
		// 8 MiB cap per image — anything larger is a misuse / abuse
		// vector and not appropriate for a printable PDF anyway.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil || len(body) == 0 {
			return nil, "", nil
		}
		return body, format, nil
	}
}

func imageFormatFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case strings.HasPrefix(ct, "image/png"):
		return "PNG"
	case strings.HasPrefix(ct, "image/jpeg"), strings.HasPrefix(ct, "image/jpg"):
		return "JPG"
	case strings.HasPrefix(ct, "image/gif"):
		return "GIF"
	}
	return ""
}

func imageFormatFromURL(src string) string {
	lower := strings.ToLower(src)
	// Trim query string for extension sniffing.
	if i := strings.IndexByte(lower, '?'); i > 0 {
		lower = lower[:i]
	}
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "PNG"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "JPG"
	case strings.HasSuffix(lower, ".gif"):
		return "GIF"
	}
	return ""
}
