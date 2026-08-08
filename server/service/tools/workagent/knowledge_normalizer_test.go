package workagent

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestNormalizeKnowledgeFile_TextAndMarkdown(t *testing.T) {
	got, err := NormalizeKnowledgeFile("brief.md", "", []byte("# Brand\n\nUse honest image references."))
	if err != nil {
		t.Fatalf("NormalizeKnowledgeFile: %v", err)
	}
	if got.MimeType != "text/markdown" || !strings.Contains(got.Text, "honest image references") {
		t.Fatalf("normalized markdown = %+v", got)
	}
}

func TestNormalizeKnowledgeFile_DOCX(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create docx xml: %v", err)
	}
	_, _ = w.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Design reference</w:t></w:r></w:p><w:p><w:r><w:t>Use stable HTML artboards</w:t></w:r></w:p></w:body></w:document>`))
	if err := zw.Close(); err != nil {
		t.Fatalf("close docx: %v", err)
	}

	got, err := NormalizeKnowledgeFile("handbook.docx", "", buf.Bytes())
	if err != nil {
		t.Fatalf("NormalizeKnowledgeFile: %v", err)
	}
	if !strings.Contains(got.Text, "Design reference") || !strings.Contains(got.Text, "stable HTML artboards") {
		t.Fatalf("normalized docx text = %q", got.Text)
	}
}

func TestNormalizeKnowledgeFile_HTMLRemovesNonContentAndDecodesEntities(t *testing.T) {
	got, err := NormalizeKnowledgeFile("brief.html", "", []byte(`<html><head><style>.hidden{}</style><script>alert("x")</script></head><body><h1>Brand &amp; product</h1><p>Use real assets.</p></body></html>`))
	if err != nil {
		t.Fatalf("NormalizeKnowledgeFile: %v", err)
	}
	if strings.Contains(got.Text, "alert") || strings.Contains(got.Text, ".hidden") {
		t.Fatalf("html non-content leaked into normalized text: %q", got.Text)
	}
	if !strings.Contains(got.Text, "Brand & product") || !strings.Contains(got.Text, "Use real assets.") {
		t.Fatalf("html content/entities not normalized: %q", got.Text)
	}
}

func TestNormalizeKnowledgeFile_DOCXIncludesHeadersAndComments(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Main body guidance</w:t></w:r></w:p></w:body></w:document>`,
		"word/header1.xml":  `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Header brand rule</w:t></w:r></w:p></w:hdr>`,
		"word/comments.xml": `<w:comments xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:comment><w:p><w:r><w:t>Reviewer note</w:t></w:r></w:p></w:comment></w:comments>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close docx: %v", err)
	}

	got, err := NormalizeKnowledgeFile("handbook.docx", "", buf.Bytes())
	if err != nil {
		t.Fatalf("NormalizeKnowledgeFile: %v", err)
	}
	for _, want := range []string{"Main body guidance", "Header brand rule", "Reviewer note"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("normalized docx text missing %q: %q", want, got.Text)
		}
	}
}

func TestNormalizeKnowledgeFile_PDFBestEffortLiteralAndHex(t *testing.T) {
	data := []byte(`%PDF-1.7
1 0 obj <<>> stream
BT (Design\040rules) Tj <48657820627566666572> Tj ET
endstream endobj`)
	got, err := NormalizeKnowledgeFile("brief.pdf", "", data)
	if err != nil {
		t.Fatalf("NormalizeKnowledgeFile: %v", err)
	}
	if !strings.Contains(got.Text, "Design rules") || !strings.Contains(got.Text, "Hex buffer") {
		t.Fatalf("normalized pdf text = %q", got.Text)
	}
}

func TestNormalizeKnowledgeFile_PDFTextOperatorsPreserveLines(t *testing.T) {
	data := []byte(`%PDF-1.7
1 0 obj <<>> stream
BT
72 720 Td
[(Hero ) 30 (layout ) -20 (rules)] TJ
T*
(Second\040line) Tj
T*
<5468697264206c696e65> Tj
ET
endstream endobj`)
	got, err := NormalizeKnowledgeFile("brief.pdf", "", data)
	if err != nil {
		t.Fatalf("NormalizeKnowledgeFile: %v", err)
	}
	for _, want := range []string{"Hero layout rules", "Second line", "Third line"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("normalized pdf text missing %q: %q", want, got.Text)
		}
	}
	if strings.Contains(got.Text, "Td") || strings.Contains(got.Text, "Tj") {
		t.Fatalf("pdf operators leaked into normalized text: %q", got.Text)
	}
}

func TestNormalizeKnowledgeFile_PDFUsesOCRFallbackWhenNoTextExtracted(t *testing.T) {
	old := pdfOCRExtractor
	t.Cleanup(func() { pdfOCRExtractor = old })
	pdfOCRExtractor = func(data []byte) string {
		if !strings.Contains(string(data), "%PDF") {
			t.Fatalf("OCR fallback did not receive PDF bytes")
		}
		return "Scanned layout text from OCR"
	}

	got, err := NormalizeKnowledgeFile("scanned.pdf", "", []byte("%PDF-1.7\nstream\n/Im0 Do\nendstream"))
	if err != nil {
		t.Fatalf("NormalizeKnowledgeFile: %v", err)
	}
	if got.Text != "Scanned layout text from OCR" {
		t.Fatalf("OCR fallback text = %q", got.Text)
	}
}

func TestNormalizeKnowledgeFile_Unsupported(t *testing.T) {
	if _, err := NormalizeKnowledgeFile("data.bin", "application/octet-stream", []byte{1, 2, 3}); err == nil {
		t.Fatal("expected unsupported file type")
	}
}
