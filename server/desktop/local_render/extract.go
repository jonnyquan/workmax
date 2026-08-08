//go:build desktop

package local_render

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ledongthuc/pdf"

	localinference "server/desktop/local_inference"
)

// Attachment is defined in local_inference (the Engine consumes it); this alias
// lets local_render's extract functions read cleanly without dragging the
// storage package into the Engine's dependency graph.
type Attachment = localinference.Attachment

const (
	// maxExtractTextBytes caps extracted text length so a huge PDF/document
	// cannot blow up the model context window or memory.
	maxExtractTextBytes int64 = 1 << 20 // 1 MiB

	attachmentKindImage = "image"
	attachmentKindText  = "text"
)

// (Attachment type is aliased above to local_inference.Attachment.)

// ExtractFile loads a persisted thread file and converts it into an Attachment
// the local model can consume:
//   - image  → base64 of the raw bytes
//   - text/json/csv/xml → file content as-is (UTF-8)
//   - pdf    → plain-text extraction (github.com/ledongthuc/pdf)
//   - word   → .docx zip+xml text extraction (word/document.xml)
//
// pptx/xlsx fall back to "unsupported" in this slice (TODO L3b-later).
func ExtractFile(absPath, mimeType, fileType string) (Attachment, error) {
	switch fileType {
	case "image":
		return extractImage(absPath, mimeType)
	case "pdf":
		return extractPDF(absPath)
	case "word":
		return extractDocx(absPath)
	case "json", "text", "csv", "xml":
		return extractTextFile(absPath)
	}
	// Fallback by MIME when fileType is generic ("other").
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return extractImage(absPath, mimeType)
	case strings.HasPrefix(mimeType, "text/"):
		return extractTextFile(absPath)
	case mimeType == "application/pdf":
		return extractPDF(absPath)
	}
	return Attachment{}, fmt.Errorf("unsupported attachment type %q (%s)", fileType, mimeType)
}

func extractImage(path, mimeType string) (Attachment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Attachment{}, fmt.Errorf("read image: %w", err)
	}
	return Attachment{
		Kind:     attachmentKindImage,
		MimeType: mimeType,
		Base64:   base64.StdEncoding.EncodeToString(data),
	}, nil
}

func extractTextFile(path string) (Attachment, error) {
	f, err := os.Open(path)
	if err != nil {
		return Attachment{}, fmt.Errorf("open text file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxExtractTextBytes+1))
	if err != nil {
		return Attachment{}, fmt.Errorf("read text file: %w", err)
	}
	return Attachment{Kind: attachmentKindText, MimeType: "text/plain", Text: capText(data)}, nil
}

func extractPDF(path string) (Attachment, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return Attachment{}, fmt.Errorf("open pdf: %w", err)
	}
	defer f.Close()
	textReader, err := r.GetPlainText()
	if err != nil {
		return Attachment{}, fmt.Errorf("pdf extract: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(textReader, maxExtractTextBytes+1))
	if err != nil {
		return Attachment{}, fmt.Errorf("read pdf text: %w", err)
	}
	return Attachment{Kind: attachmentKindText, MimeType: "application/pdf", Text: capText(data)}, nil
}

// extractDocx reads a .docx (zip) container's word/document.xml and extracts
// the text of every <w:t> element, inserting a newline at each paragraph end
// (<w:p>) so paragraphs don't run together. Zero third-party deps.
func extractDocx(path string) (Attachment, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Attachment{}, fmt.Errorf("open docx: %w", err)
	}
	defer zr.Close()
	docFile, err := zr.Open("word/document.xml")
	if err != nil {
		return Attachment{}, fmt.Errorf("docx document.xml: %w", err)
	}
	defer docFile.Close()

	var buf bytes.Buffer
	dec := xml.NewDecoder(docFile)
	for {
		if int64(buf.Len()) > maxExtractTextBytes {
			break
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Attachment{}, fmt.Errorf("parse docx xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			// WordprocessingML text node. Local name "t" without checking the
			// namespace URI is sufficient here — the document.xml body only
			// uses the w: namespace.
			if t.Name.Local == "t" {
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return Attachment{}, fmt.Errorf("decode w:t: %w", err)
				}
				buf.WriteString(s)
			}
		case xml.EndElement:
			if t.Name.Local == "p" {
				buf.WriteByte('\n')
			}
		}
	}
	return Attachment{
		Kind:     attachmentKindText,
		MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Text:     capText(buf.Bytes()),
	}, nil
}

// capText truncates extracted bytes to maxExtractTextBytes.
func capText(data []byte) string {
	if int64(len(data)) > maxExtractTextBytes {
		data = data[:maxExtractTextBytes]
	}
	return string(data)
}
