package workagent

// agent_files_context_test.go — coverage for the file-context
// builders that compose the AVAILABLE FILES block in the system
// prompt. Three logic surfaces worth pinning:
//
//   1. formatFileSize — KB/MB/GB classification at boundaries
//   2. getFileTypeDescription — switch table, every documented branch
//   3. buildCrossFormatSuggestions Multi-Format gate — documented
//      regression where pptFiles was silently excluded from
//      fileTypeCount, suppressing the suggestion for PPT-inclusive
//      uploads
//   4. buildIntelligentFilesContext — empty-input + non-empty paths

import (
	"strings"
	"testing"
)

func TestFormatFileSize_Boundaries(t *testing.T) {
	cases := []struct {
		size int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},                  // 1.5 KB
		{1024 * 1024, "1.0 MB"},
		{int64(1.5 * 1024 * 1024), "1.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
	}
	for _, tc := range cases {
		got := formatFileSize(tc.size)
		if got != tc.want {
			t.Errorf("formatFileSize(%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

func TestGetFileTypeDescription_KnownMimeTypes(t *testing.T) {
	cases := map[string]string{
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "Excel Spreadsheet",
		"application/vnd.ms-excel":                                         "Excel Spreadsheet",
		"text/csv":                                                         "CSV Data",
		"application/pdf":                                                  "PDF Document",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "Word Document",
		"application/msword":      "Word Document",
		"text/markdown":           "Markdown Text",
		"text/plain":              "Plain Text",
		"text/html":               "Text File",
		"image/png":               "PNG Image",
		"image/jpeg":              "JPEG Image",
		"image/jpg":               "JPEG Image",
		"image/gif":               "GIF Image",
		"image/webp":              "WebP Image",
		"image/bmp":               "Image File",
		"application/json":        "JSON Data",
		"application/xml":         "XML Data",
	}
	for mime, want := range cases {
		t.Run(mime, func(t *testing.T) {
			got := getFileTypeDescription(mime)
			if got != want {
				t.Errorf("getFileTypeDescription(%q) = %q, want %q", mime, got, want)
			}
		})
	}
}

func TestGetFileTypeDescription_UnknownMimeFallsBackToSuffix(t *testing.T) {
	got := getFileTypeDescription("application/vnd.ms-outlook")
	// Fallback title-case the suffix.
	if got != "Vnd.ms-outlook" {
		t.Errorf("unknown mime fallback = %q, want %q", got, "Vnd.ms-outlook")
	}
	// No slash → return literal "Unknown File".
	if got := getFileTypeDescription("garbage-no-slash"); got != "Unknown File" {
		t.Errorf("malformed mime fallback = %q, want %q", got, "Unknown File")
	}
}

func makeFile(name, mime string) AgentFileInfo {
	return AgentFileInfo{Name: name, Type: mime, Size: 1024}
}

func TestBuildIntelligentFilesContext_EmptyReturnsEmpty(t *testing.T) {
	if got := buildIntelligentFilesContext(nil); got != "" {
		t.Errorf("empty input returned %q", got)
	}
	if got := buildIntelligentFilesContext([]AgentFileInfo{}); got != "" {
		t.Errorf("empty slice returned %q", got)
	}
}

func TestBuildIntelligentFilesContext_SingleFileBuildsBlock(t *testing.T) {
	files := []AgentFileInfo{makeFile("data.csv", "text/csv")}
	got := buildIntelligentFilesContext(files)
	if !strings.Contains(got, "AVAILABLE FILES") {
		t.Errorf("missing header: %s", got)
	}
	if !strings.Contains(got, "data.csv") {
		t.Errorf("missing filename: %s", got)
	}
	if !strings.Contains(got, "Excel/CSV") {
		t.Errorf("missing bucket header for csv: %s", got)
	}
}

// TestBuildCrossFormatSuggestions_PPTCountedInMultiFormatGate is the
// REGRESSION PIN for the documented bug at line ~301 of
// agent_files_context.go. The previous fileTypeCount loop iterated
// over excel/pdf/word/text/image but quietly skipped pptFiles even
// though pptFiles is in the function signature and powers pairwise
// opportunities. A user uploading PPT + Excel + Image (3 buckets)
// hit count=2 and never saw the "Multi-Format Integration"
// suggestion. Fixed by driving from a slice that includes pptFiles.
func TestBuildCrossFormatSuggestions_PPTCountedInMultiFormatGate(t *testing.T) {
	excel := []AgentFileInfo{makeFile("a.xlsx", "application/vnd.ms-excel")}
	ppt := []AgentFileInfo{makeFile("b.pptx", "application/vnd.ms-powerpoint")}
	image := []AgentFileInfo{makeFile("c.png", "image/png")}

	got := buildCrossFormatSuggestions(excel, nil, nil, ppt, nil, image)
	if !strings.Contains(got, "Multi-Format Integration") {
		t.Errorf("3 distinct buckets (Excel + PPT + Image) must trigger Multi-Format suggestion; got %s", got)
	}
}

func TestBuildCrossFormatSuggestions_TwoBucketsNoMultiFormatGate(t *testing.T) {
	excel := []AgentFileInfo{makeFile("a.xlsx", "application/vnd.ms-excel")}
	pdf := []AgentFileInfo{makeFile("b.pdf", "application/pdf")}

	got := buildCrossFormatSuggestions(excel, pdf, nil, nil, nil, nil)
	// Multi-Format Integration is only for >= 3 distinct buckets;
	// pairwise PDF+Excel is fine but Multi-Format must NOT trigger.
	if strings.Contains(got, "Multi-Format Integration") {
		t.Errorf("2 buckets must NOT trigger Multi-Format gate; got %s", got)
	}
	if !strings.Contains(got, "PDF + Excel") {
		t.Errorf("pairwise PDF+Excel suggestion missing: %s", got)
	}
}

func TestBuildCrossFormatSuggestions_EmptyInputsReturnEmpty(t *testing.T) {
	got := buildCrossFormatSuggestions(nil, nil, nil, nil, nil, nil)
	if got != "" {
		t.Errorf("all-empty input returned %q", got)
	}
}

func TestBuildSkillSuggestions_PerKindHints(t *testing.T) {
	pdf := []AgentFileInfo{makeFile("a.pdf", "application/pdf")}
	got := buildSkillSuggestions(pdf, nil, nil, nil)
	if !strings.Contains(got, "`pdf` skill") {
		t.Errorf("PDF skill suggestion missing: %s", got)
	}

	ppt := []AgentFileInfo{makeFile("b.pptx", "application/vnd.ms-powerpoint")}
	got = buildSkillSuggestions(nil, nil, ppt, nil)
	if !strings.Contains(got, "`pptx` skill") {
		t.Errorf("PPTX skill suggestion missing: %s", got)
	}

	// All-empty short-circuits.
	if got := buildSkillSuggestions(nil, nil, nil, nil); got != "" {
		t.Errorf("all-empty must return empty; got %q", got)
	}
}

func TestBuildAnalysisSuggestions_BucketSpecificIdeas(t *testing.T) {
	excel := []AgentFileInfo{makeFile("a.xlsx", "application/vnd.ms-excel")}
	got := buildAnalysisSuggestions(excel, nil, nil, nil, nil, nil)
	if !strings.Contains(got, "Excel/CSV Analysis Ideas") {
		t.Errorf("Excel ideas section missing: %s", got)
	}
	if !strings.Contains(got, "Pivot tables") {
		t.Errorf("expected 'Pivot tables' bullet under Excel: %s", got)
	}

	if got := buildAnalysisSuggestions(nil, nil, nil, nil, nil, nil); got != "" {
		t.Errorf("all-empty must return empty; got %q", got)
	}
}

// Cache integration: repeated builds with the same input must
// return the same string (memoized).
func TestBuildIntelligentFilesContext_MemoizesByFingerprint(t *testing.T) {
	files := []AgentFileInfo{
		{ID: "a", Name: "data.csv", Type: "text/csv", Size: 1024, Hash: "abc"},
	}
	first := buildIntelligentFilesContext(files)
	second := buildIntelligentFilesContext(files)
	if first != second {
		t.Errorf("memoized fetch differs from first build")
	}
}
