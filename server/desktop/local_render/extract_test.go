//go:build desktop

package local_render

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// writeDocxFile builds a minimal .docx zip with word/document.xml containing
// two paragraphs, so the extract path's <w:t> + paragraph-newline handling is
// exercised without a real Office file.
func writeDocxFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create docx: %v", err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	doc, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create document.xml entry: %v", err)
	}
	if _, err := doc.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:t>Hello docx</w:t></w:p><w:p><w:t>second para</w:t></w:p></w:body></w:document>`)); err != nil {
		t.Fatalf("write document.xml: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close docx zip: %v", err)
	}
	return path
}

func TestExtract_TextFile(t *testing.T) {
	path := writeTempFile(t, "notes.txt", "hello text content")
	att, err := ExtractFile(path, "text/plain", "text")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	if att.Kind != "text" || att.Text != "hello text content" {
		t.Fatalf("unexpected attachment: %+v", att)
	}
}

func TestExtract_Docx(t *testing.T) {
	path := writeDocxFile(t)
	att, err := ExtractFile(path, "application/zip", "word")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	if att.Kind != "text" {
		t.Fatalf("kind = %q, want text", att.Kind)
	}
	if !strings.Contains(att.Text, "Hello docx") || !strings.Contains(att.Text, "second para") {
		t.Fatalf("docx text missing content: %q", att.Text)
	}
	if !strings.Contains(att.Text, "\n") {
		t.Fatalf("docx paragraphs not separated by newline: %q", att.Text)
	}
}

func TestExtract_Image(t *testing.T) {
	path := writeTempFile(t, "pic.png", string(minimalPNG(t)))
	att, err := ExtractFile(path, "image/png", "image")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	if att.Kind != "image" || att.Base64 == "" {
		t.Fatalf("unexpected image attachment: %+v", att)
	}
}

func TestExtract_UnsupportedType(t *testing.T) {
	path := writeTempFile(t, "x.bin", "random bytes")
	if _, err := ExtractFile(path, "application/octet-stream", "other"); err == nil {
		t.Fatal("expected unsupported-type error")
	}
}
