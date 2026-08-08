package workagent

import "testing"

// IsConverted is the read-side signal that the upload pipeline
// silently transcoded the stored file (e.g. .xls → .xlsx). The matrix
// is small but the original implementation's nested branches made it
// easy to miss a case; the table below pins each path explicitly.
func TestThreadFile_IsConverted(t *testing.T) {
	cases := []struct {
		name     string
		fileName string
		filePath string
		fileType string
		want     bool
	}{
		// Standard: .xls original transcoded to .xlsx.
		{"xls→xlsx", "report.xls", "uploads/report.xlsx", "application/vnd.ms-excel", true},
		{"xls→xlsx case insensitive", "Report.XLS", "uploads/Report.XLSX", "", true},

		// Same-extension passes through (no conversion).
		{"xlsx untouched", "report.xlsx", "uploads/report.xlsx", "", false},

		// Different non-converted formats.
		{"pdf untouched", "doc.pdf", "uploads/doc.pdf", "application/pdf", false},
		{"csv untouched", "data.csv", "uploads/data.csv", "text/csv", false},

		// No extension on the original — fall back to FileType.
		// (Note: the openxml spreadsheet MIME doesn't contain the
		// substring "excel", so the type-based fallback misses it —
		// preserved-bug for backward compatibility, but pinned here
		// so anyone tightening the heuristic sees the case.)
		{"no-ext+ms-excel-type→xlsx", "data", "uploads/data.xlsx", "application/vnd.ms-excel", true},
		{"no-ext+excel-substring-type→xlsx", "data", "uploads/data.xlsx", "application/excel-derivative", true},
		{"no-ext+openxml-mime→xlsx (false; type lacks 'excel')", "spreadsheet", "uploads/spreadsheet.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", false},
		{"no-ext+non-excel-type→xlsx", "data", "uploads/data.xlsx", "application/octet-stream", false},

		// Stored .xls is by definition not "converted" — only .xlsx outputs flag.
		{"path stays xls", "report.xls", "uploads/report.xls", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tf := &ThreadFile{FileName: tc.fileName, FilePath: tc.filePath, FileType: tc.fileType}
			if got := tf.IsConverted(); got != tc.want {
				t.Errorf("IsConverted(%q, %q, %q) = %v, want %v",
					tc.fileName, tc.filePath, tc.fileType, got, tc.want)
			}
		})
	}
}
