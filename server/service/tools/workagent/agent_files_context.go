package workagent

// File context-building helpers for the Agent system prompt.
// Lifted out of agent_processor.go (which now focuses on the SDK
// conversation loop) because these functions have zero coupling to
// the Claude SDK — they're pure (file metadata) → markdown string
// formatting + suggestion-block builders.
//
// The whole rendered output is memoized in files_context_cache.go,
// so allocation cost is amortized across turns when the file set
// doesn't change. New file types or suggestion heuristics belong
// here, not in agent_processor.go.

import (
	"fmt"
	"strings"
)
// formatFileSize formats file size in human-readable format
func formatFileSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

// buildIntelligentFilesContext creates a smart file listing grouped by type with usage hints.
//
// Memoized by content-addressed fingerprint (see filesContextFingerprint).
// The same file set produces the same context, and the system prompt
// the SDK forwards to Claude embeds this string — so a stable output
// across turns lets Claude's prompt cache hit for unchanged file sets.
// Without this cache the function rebuilds ~30 Sprintf calls + 7
// bucket slices + suggestion sub-blocks on every turn, even when the
// caller is just continuing an existing conversation with the same
// pinned attachments.
func buildIntelligentFilesContext(files []AgentFileInfo) string {
	if len(files) == 0 {
		return ""
	}

	cache := getFilesContextCache()
	fingerprint := filesContextFingerprint(files)
	if cached, ok := cache.get(fingerprint); ok {
		return cached
	}

	rendered := renderFilesContext(files)
	cache.put(fingerprint, rendered)
	return rendered
}

// renderFilesContext is the cache-miss path: actually build the
// grouped listing string from scratch.
func renderFilesContext(files []AgentFileInfo) string {
	// Group files by category
	excelFiles := []AgentFileInfo{}
	pdfFiles := []AgentFileInfo{}
	wordFiles := []AgentFileInfo{}
	pptFiles := []AgentFileInfo{}
	textFiles := []AgentFileInfo{}
	imageFiles := []AgentFileInfo{}
	otherFiles := []AgentFileInfo{}

	for _, file := range files {
		mimeTypeLower := strings.ToLower(file.Type)
		switch {
		case strings.Contains(mimeTypeLower, "spreadsheet"), strings.Contains(mimeTypeLower, "excel"), strings.Contains(mimeTypeLower, "csv"):
			excelFiles = append(excelFiles, file)
		case strings.Contains(mimeTypeLower, "pdf"):
			pdfFiles = append(pdfFiles, file)
		case strings.Contains(mimeTypeLower, "wordprocessing"), strings.Contains(mimeTypeLower, "msword"):
			wordFiles = append(wordFiles, file)
		case strings.Contains(mimeTypeLower, "presentation"), strings.Contains(mimeTypeLower, "powerpoint"), strings.Contains(mimeTypeLower, "ppt"):
			pptFiles = append(pptFiles, file)
		case strings.Contains(mimeTypeLower, "text"), strings.Contains(mimeTypeLower, "markdown"):
			textFiles = append(textFiles, file)
		case strings.Contains(mimeTypeLower, "image"):
			imageFiles = append(imageFiles, file)
		default:
			otherFiles = append(otherFiles, file)
		}
	}

	// One row per file-type bucket. The first six render identically
	// (header → per-file line → tools hint); the seventh ("other")
	// uses a richer per-file line that pulls in getFileTypeDescription
	// and has no tools hint. Driving from a slice keeps the seven
	// near-identical 4-line blocks from drifting apart over time —
	// each new tools hint or emoji rename used to require touching
	// six places.
	type bucketRender struct {
		files     []AgentFileInfo
		header    string
		toolsLine string // empty = no tools hint
	}
	buckets := []bucketRender{
		{excelFiles, "\n📊 **Excel/CSV Files:**\n", "→ Tools: pandas (pd.read_excel/read_csv), openpyxl\n"},
		{pdfFiles, "\n📄 **PDF Documents:**\n", "→ Tools: pdfplumber, PyPDF2, tabula-py\n"},
		{wordFiles, "\n📝 **Word Documents:**\n", "→ Tools: python-docx (Document)\n"},
		{pptFiles, "\n📊 **PowerPoint Presentations:**\n", "→ Tools: python-pptx (Presentation), or use `pptx` Skill for advanced processing\n"},
		{textFiles, "\n📋 **Text Files:**\n", "→ Tools: open(), built-in file operations\n"},
		{imageFiles, "\n🖼️ **Image Files:**\n", "→ Tools: PIL/Pillow (Image.open)\n"},
	}

	var sb strings.Builder
	sb.WriteString("\n\n**AVAILABLE FILES:**\n")

	for _, b := range buckets {
		if len(b.files) == 0 {
			continue
		}
		sb.WriteString(b.header)
		for _, f := range b.files {
			fmt.Fprintf(&sb, "- %s (./uploads/%s) - %s\n", f.Name, f.Name, formatFileSize(f.Size))
		}
		if b.toolsLine != "" {
			sb.WriteString(b.toolsLine)
		}
	}

	// "Other" needs the trailing type description, so it doesn't fit
	// the bucketRender shape — keep it inline rather than complicating
	// the table just for one case.
	if len(otherFiles) > 0 {
		sb.WriteString("\n📦 **Other Files:**\n")
		for _, f := range otherFiles {
			fmt.Fprintf(&sb, "- %s (./uploads/%s) - %s, Type: %s\n",
				f.Name, f.Name, formatFileSize(f.Size), getFileTypeDescription(f.Type))
		}
	}

	// General file reference instructions
	sb.WriteString("\n**Access Instructions:**\n")
	sb.WriteString("- Use @ notation to reference: @filename.xlsx\n")
	sb.WriteString("- Direct path access: ./uploads/filename\n")
	sb.WriteString("- All output files must go to: ./outputs/\n")
	context := sb.String()

	// Phase 3.1: Skill Pre-loading Suggestions
	skillSuggestions := buildSkillSuggestions(pdfFiles, wordFiles, pptFiles, imageFiles)
	if skillSuggestions != "" {
		context += "\n" + skillSuggestions
	}

	// Phase 3.2: Intelligent Analysis Direction Suggestions
	analysisSuggestions := buildAnalysisSuggestions(excelFiles, pdfFiles, wordFiles, pptFiles, textFiles, imageFiles)
	if analysisSuggestions != "" {
		context += "\n" + analysisSuggestions
	}

	// Phase 3.3: Cross-Format Analysis Opportunities
	crossFormatSuggestions := buildCrossFormatSuggestions(excelFiles, pdfFiles, wordFiles, pptFiles, textFiles, imageFiles)
	if crossFormatSuggestions != "" {
		context += "\n" + crossFormatSuggestions
	}

	return context
}

// buildSkillSuggestions generates skill pre-loading hints based on file types
func buildSkillSuggestions(pdfFiles, wordFiles, pptFiles, imageFiles []AgentFileInfo) string {
	if len(pdfFiles) == 0 && len(wordFiles) == 0 && len(pptFiles) == 0 && len(imageFiles) == 0 {
		return ""
	}

	suggestions := "\n💡 **Recommended Skills to Load:**\n"
	skillsNeeded := []string{}

	if len(pdfFiles) > 0 {
		skillsNeeded = append(skillsNeeded, "- `pdf` skill for advanced PDF processing (table extraction, OCR)")
	}
	if len(wordFiles) > 0 {
		skillsNeeded = append(skillsNeeded, "- `docx` skill for Word document manipulation")
	}
	if len(pptFiles) > 0 {
		skillsNeeded = append(skillsNeeded, "- `pptx` skill for PowerPoint content extraction and analysis")
	}
	if len(imageFiles) > 0 {
		skillsNeeded = append(skillsNeeded, "- `ocr` skill for text extraction from images")
		skillsNeeded = append(skillsNeeded, "- `image` skill for advanced image analysis")
	}

	if len(skillsNeeded) > 0 {
		suggestions += strings.Join(skillsNeeded, "\n") + "\n"
		suggestions += "\n**How to Load Skills:**\n"
		suggestions += "Use the `Skill` tool to load these capabilities when needed.\n"
		suggestions += "Example: Call Skill tool with skill_name='pptx' to load PowerPoint processing capabilities.\n"
		return suggestions
	}

	return ""
}

// buildAnalysisSuggestions provides intelligent analysis direction hints
func buildAnalysisSuggestions(excelFiles, pdfFiles, wordFiles, pptFiles, textFiles, imageFiles []AgentFileInfo) string {
	suggestions := []string{}

	// Excel/CSV analysis suggestions
	if len(excelFiles) > 0 {
		suggestions = append(suggestions, "\n📊 **Excel/CSV Analysis Ideas:**")
		suggestions = append(suggestions, "- Statistical summary and descriptive analytics")
		suggestions = append(suggestions, "- Trend analysis and visualization")
		suggestions = append(suggestions, "- Pivot tables and data aggregation")
		suggestions = append(suggestions, "- Interactive charts (Plotly recommended)")
	}

	// PDF analysis suggestions
	if len(pdfFiles) > 0 {
		suggestions = append(suggestions, "\n📄 **PDF Analysis Ideas:**")
		suggestions = append(suggestions, "- Extract and structure tables for analysis")
		suggestions = append(suggestions, "- Text mining and keyword extraction")
		suggestions = append(suggestions, "- Document summarization")
		suggestions = append(suggestions, "- Convert to structured formats (CSV, JSON)")
	}

	// Word document suggestions
	if len(wordFiles) > 0 {
		suggestions = append(suggestions, "\n📝 **Word Document Analysis Ideas:**")
		suggestions = append(suggestions, "- Content extraction and text analysis")
		suggestions = append(suggestions, "- Table data extraction")
		suggestions = append(suggestions, "- Document structure analysis")
		suggestions = append(suggestions, "- Convert to other formats")
	}

	// PowerPoint presentation suggestions
	if len(pptFiles) > 0 {
		suggestions = append(suggestions, "\n📊 **PowerPoint Presentation Analysis Ideas:**")
		suggestions = append(suggestions, "- Extract text content from all slides")
		suggestions = append(suggestions, "- Parse tables and charts from slides")
		suggestions = append(suggestions, "- Extract images and diagrams")
		suggestions = append(suggestions, "- Structure analysis (slide titles, content hierarchy)")
		suggestions = append(suggestions, "- Convert to other formats (PDF, images)")
		suggestions = append(suggestions, "- Load `pptx` skill for advanced processing")
	}

	// Text file suggestions
	if len(textFiles) > 0 {
		suggestions = append(suggestions, "\n📋 **Text File Analysis Ideas:**")
		suggestions = append(suggestions, "- Word frequency and text statistics")
		suggestions = append(suggestions, "- Pattern matching and regex analysis")
		suggestions = append(suggestions, "- Log file parsing and insights")
		suggestions = append(suggestions, "- Sentiment analysis (if applicable)")
	}

	// Image analysis suggestions
	if len(imageFiles) > 0 {
		suggestions = append(suggestions, "\n🖼️ **Image Analysis Ideas:**")
		suggestions = append(suggestions, "- Extract text via OCR")
		suggestions = append(suggestions, "- Analyze charts/graphs in images")
		suggestions = append(suggestions, "- Metadata extraction (dimensions, format, EXIF)")
		suggestions = append(suggestions, "- Image comparison and similarity")
	}

	if len(suggestions) > 0 {
		return "\n**💡 Analysis Suggestions:**" + strings.Join(suggestions, "\n") + "\n"
	}

	return ""
}

// buildCrossFormatSuggestions identifies opportunities for cross-format analysis
func buildCrossFormatSuggestions(excelFiles, pdfFiles, wordFiles, pptFiles, textFiles, imageFiles []AgentFileInfo) string {
	opportunities := []string{}

	// PDF + Excel combination
	if len(pdfFiles) > 0 && len(excelFiles) > 0 {
		opportunities = append(opportunities, "🔗 **PDF + Excel**: Extract tables from PDF and merge with Excel data for comprehensive analysis")
	}

	// Word + Excel combination
	if len(wordFiles) > 0 && len(excelFiles) > 0 {
		opportunities = append(opportunities, "🔗 **Word + Excel**: Extract tables from Word documents and combine with spreadsheet data")
	}

	// PowerPoint + Excel combination
	if len(pptFiles) > 0 && len(excelFiles) > 0 {
		opportunities = append(opportunities, "🔗 **PowerPoint + Excel**: Extract data from presentation slides and merge with Excel for detailed analysis")
	}

	// Image + Excel combination
	if len(imageFiles) > 0 && len(excelFiles) > 0 {
		opportunities = append(opportunities, "🔗 **Image + Excel**: Use OCR to extract data from image tables and merge with Excel datasets")
	}

	// PowerPoint + PDF combination
	if len(pptFiles) > 0 && len(pdfFiles) > 0 {
		opportunities = append(opportunities, "🔗 **PowerPoint + PDF**: Compare presentation content with PDF reports for consistency analysis")
	}

	// PDF + Word combination
	if len(pdfFiles) > 0 && len(wordFiles) > 0 {
		opportunities = append(opportunities, "🔗 **PDF + Word**: Compare document content and structure across formats")
	}

	// Multi-format comprehensive analysis. The previous shape counted
	// excel/pdf/word/text/image but quietly skipped pptFiles — even
	// though pptFiles is in the function signature and powers the
	// pairwise opportunity checks above. A user uploading PPT + Excel
	// + image (3 distinct buckets) hit count=2 and never saw the
	// "Multi-Format Integration" suggestion despite obviously having
	// multi-format input. Drive from a slice so adding a new bucket
	// type in the future doesn't get accidentally dropped here.
	buckets := [][]AgentFileInfo{excelFiles, pdfFiles, wordFiles, pptFiles, textFiles, imageFiles}
	fileTypeCount := 0
	for _, b := range buckets {
		if len(b) > 0 {
			fileTypeCount++
		}
	}

	if fileTypeCount >= 3 {
		opportunities = append(opportunities, "🔗 **Multi-Format Integration**: Create unified analysis combining insights from all file types")
	}

	if len(opportunities) > 0 {
		return "\n**🔄 Cross-Format Analysis Opportunities:**\n" + strings.Join(opportunities, "\n") + "\n"
	}

	return ""
}

// getFileTypeDescription returns a human-readable file type description with detailed classification
func getFileTypeDescription(mimeType string) string {
	// Normalize for case-insensitive comparison
	mimeTypeLower := strings.ToLower(mimeType)

	switch {
	// Excel formats
	case strings.Contains(mimeTypeLower, "spreadsheet"), strings.Contains(mimeTypeLower, "excel"):
		return "Excel Spreadsheet"
	case strings.Contains(mimeTypeLower, "csv"):
		return "CSV Data"

	// PDF
	case strings.Contains(mimeTypeLower, "pdf"):
		return "PDF Document"

	// Word formats
	case strings.Contains(mimeTypeLower, "wordprocessing"), strings.Contains(mimeTypeLower, "msword"):
		return "Word Document"

	// Text formats - distinguish between types
	case strings.Contains(mimeTypeLower, "markdown"):
		return "Markdown Text"
	case mimeTypeLower == "text/plain":
		return "Plain Text"
	case strings.Contains(mimeTypeLower, "text"):
		return "Text File"

	// Image formats - detailed classification
	case strings.Contains(mimeTypeLower, "image/png"):
		return "PNG Image"
	case strings.Contains(mimeTypeLower, "image/jpeg"), strings.Contains(mimeTypeLower, "image/jpg"):
		return "JPEG Image"
	case strings.Contains(mimeTypeLower, "image/gif"):
		return "GIF Image"
	case strings.Contains(mimeTypeLower, "image/webp"):
		return "WebP Image"
	case strings.Contains(mimeTypeLower, "image"):
		return "Image File"

	// Data formats
	case strings.Contains(mimeTypeLower, "json"):
		return "JSON Data"
	case strings.Contains(mimeTypeLower, "xml"):
		return "XML Data"

	default:
		// Extract from mime type (e.g., "application/vnd.ms-excel" -> "Vnd.ms-excel").
		// strings.Title is deprecated; mime suffixes here are always ASCII
		// after the slash, so a first-byte upper is the equivalent transform
		// without dragging in golang.org/x/text/cases for a fallback label
		// that almost no caller will hit (the explicit cases above cover
		// the real traffic).
		parts := strings.Split(mimeType, "/")
		if len(parts) == 2 && parts[1] != "" {
			suffix := parts[1]
			if c := suffix[0]; c >= 'a' && c <= 'z' {
				return string(c-'a'+'A') + suffix[1:]
			}
			return suffix
		}
		return "Unknown File"
	}
}
