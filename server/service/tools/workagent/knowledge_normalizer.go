package workagent

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

type NormalizedKnowledgeFile struct {
	Text     string
	MimeType string
}

const knowledgeOCRMaxOutputBytes = 2 * 1024 * 1024

var pdfOCRExtractor = runConfiguredPDFOCRExtractor

func NormalizeKnowledgeFile(filename string, mimeType string, data []byte) (NormalizedKnowledgeFile, error) {
	mimeType = normalizeKnowledgeMime(filename, mimeType)
	ext := strings.ToLower(filepath.Ext(filename))
	var text string
	var err error
	switch {
	case mimeType == "text/html" || ext == ".html" || ext == ".htm":
		text = stripHTMLForKnowledge(string(data))
	case mimeType == "text/plain" || mimeType == "text/markdown" || strings.HasPrefix(mimeType, "text/") || ext == ".txt" || ext == ".md":
		text = string(data)
	case mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || ext == ".docx":
		text, err = extractDOCXText(data)
	case mimeType == "application/pdf" || ext == ".pdf":
		text = extractPDFTextBestEffort(data)
	default:
		return NormalizedKnowledgeFile{}, fmt.Errorf("unsupported knowledge file type: %s", mimeType)
	}
	if err != nil {
		return NormalizedKnowledgeFile{}, err
	}
	text = normalizeKnowledgeWhitespace(text)
	if text == "" {
		return NormalizedKnowledgeFile{}, fmt.Errorf("knowledge file has no extractable text")
	}
	return NormalizedKnowledgeFile{Text: text, MimeType: mimeType}, nil
}

func normalizeKnowledgeMime(filename string, mimeType string) string {
	mimeType = strings.TrimSpace(strings.Split(mimeType, ";")[0])
	if mimeType != "" && mimeType != "application/octet-stream" {
		return mimeType
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if guessed := mime.TypeByExtension(ext); guessed != "" {
		return strings.Split(guessed, ";")[0]
	}
	switch ext {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func stripHTMLForKnowledge(input string) string {
	for _, tag := range []string{"script", "style", "noscript", "template"} {
		input = regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</`+tag+`>`).ReplaceAllString(input, " ")
	}
	replacer := strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n", "</div>", "\n", "</li>", "\n")
	input = replacer.Replace(input)
	tagRe := regexp.MustCompile(`(?s)<[^>]+>`)
	return html.UnescapeString(tagRe.ReplaceAllString(input, " "))
}

func extractDOCXText(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read docx: %w", err)
	}
	parts := []string{"word/document.xml"}
	for _, prefix := range []string{"word/header", "word/footer", "word/footnotes.xml", "word/endnotes.xml", "word/comments.xml"} {
		for _, f := range reader.File {
			if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, ".xml") {
				parts = append(parts, f.Name)
			}
		}
	}
	seen := map[string]bool{}
	var b strings.Builder
	for _, part := range parts {
		if seen[part] {
			continue
		}
		seen[part] = true
		text, ok, err := extractDOCXXMLPart(reader, part)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		if strings.TrimSpace(text) != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(text)
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("docx missing extractable word XML")
	}
	return b.String(), nil
}

func extractDOCXXMLPart(reader *zip.Reader, partName string) (string, bool, error) {
	for _, f := range reader.File {
		if f.Name != partName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", false, fmt.Errorf("open docx %s: %w", partName, err)
		}
		defer rc.Close()
		var b strings.Builder
		decoder := xml.NewDecoder(rc)
		for {
			tok, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", false, fmt.Errorf("parse docx %s: %w", partName, err)
			}
			switch t := tok.(type) {
			case xml.CharData:
				b.Write([]byte(t))
			case xml.EndElement:
				if t.Name.Local == "p" || t.Name.Local == "tab" || t.Name.Local == "br" {
					b.WriteString("\n")
				}
			}
		}
		return b.String(), true, nil
	}
	return "", false, nil
}

func extractPDFTextBestEffort(data []byte) string {
	raw := string(data)
	if text := extractPDFTextOperators(raw); strings.TrimSpace(text) != "" {
		return text
	}
	if text := extractPDFRawStrings(raw); strings.TrimSpace(text) != "" {
		return text
	}
	return pdfOCRExtractor(data)
}

func runConfiguredPDFOCRExtractor(data []byte) string {
	commandPath := strings.TrimSpace(os.Getenv("WORKMAX_KNOWLEDGE_PDF_OCR_CMD"))
	if commandPath == "" {
		return ""
	}
	tmp, err := os.CreateTemp("", "workmax-knowledge-*.pdf")
	if err != nil {
		return ""
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return ""
	}
	if err := tmp.Close(); err != nil {
		return ""
	}
	out, err := exec.Command(commandPath, tmpPath).Output()
	if err != nil {
		return ""
	}
	if len(out) > knowledgeOCRMaxOutputBytes {
		out = out[:knowledgeOCRMaxOutputBytes]
	}
	return string(out)
}

func extractPDFRawStrings(raw string) string {
	var b strings.Builder
	literalRe := regexp.MustCompile(`\((?:\\.|[^\\)]){2,}\)`)
	for _, m := range literalRe.FindAllString(raw, 2000) {
		s := decodePDFLiteralString(strings.TrimSuffix(strings.TrimPrefix(m, "("), ")"))
		if printableRatio(s) < 0.6 {
			continue
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	hexRe := regexp.MustCompile(`<([0-9A-Fa-f\s]{4,})>`)
	for _, match := range hexRe.FindAllStringSubmatch(raw, 2000) {
		s := decodePDFHexString(match[1])
		if printableRatio(s) < 0.6 {
			continue
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

type pdfTextToken struct {
	kind  string
	value string
}

func extractPDFTextOperators(raw string) string {
	tokens := tokenizePDFTextOperators(raw)
	var out strings.Builder
	var lastText string
	for _, tok := range tokens {
		switch tok.kind {
		case "string", "arrayText":
			lastText = tok.value
		case "op":
			switch tok.value {
			case "Tj", "TJ":
				writePDFExtractedText(&out, lastText, false)
				lastText = ""
			case "'", `"`:
				writePDFExtractedText(&out, lastText, true)
				lastText = ""
			case "T*", "Td", "TD":
				writePDFLineBreak(&out)
				lastText = ""
			}
		}
	}
	return out.String()
}

func tokenizePDFTextOperators(raw string) []pdfTextToken {
	tokens := make([]pdfTextToken, 0, 256)
	for i := 0; i < len(raw); {
		switch {
		case isPDFWhitespace(raw[i]):
			i++
		case raw[i] == '(':
			text, next := readPDFLiteralToken(raw, i)
			if printableRatio(text) >= 0.6 {
				tokens = append(tokens, pdfTextToken{kind: "string", value: text})
			}
			i = next
		case raw[i] == '[':
			text, next := readPDFArrayTextToken(raw, i)
			if strings.TrimSpace(text) != "" && printableRatio(text) >= 0.6 {
				tokens = append(tokens, pdfTextToken{kind: "arrayText", value: text})
			}
			i = next
		case raw[i] == '<' && i+1 < len(raw) && raw[i+1] != '<':
			text, next := readPDFHexToken(raw, i)
			if printableRatio(text) >= 0.6 {
				tokens = append(tokens, pdfTextToken{kind: "string", value: text})
			}
			i = next
		case isPDFOperatorByte(raw[i]):
			op, next := readPDFOperatorToken(raw, i)
			if op != "" {
				tokens = append(tokens, pdfTextToken{kind: "op", value: op})
			}
			i = next
		default:
			i++
		}
	}
	return tokens
}

func readPDFLiteralToken(raw string, start int) (string, int) {
	var b strings.Builder
	depth := 0
	escaped := false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		if i == start {
			depth = 1
			continue
		}
		if escaped {
			b.WriteByte('\\')
			b.WriteByte(c)
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '(':
			depth++
			b.WriteByte(c)
		case ')':
			depth--
			if depth == 0 {
				return decodePDFLiteralString(b.String()), i + 1
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return decodePDFLiteralString(b.String()), len(raw)
}

func readPDFArrayTextToken(raw string, start int) (string, int) {
	var parts []string
	for i := start + 1; i < len(raw); {
		switch {
		case raw[i] == ']':
			return strings.Join(parts, ""), i + 1
		case raw[i] == '(':
			text, next := readPDFLiteralToken(raw, i)
			parts = append(parts, text)
			i = next
		case raw[i] == '<' && i+1 < len(raw) && raw[i+1] != '<':
			text, next := readPDFHexToken(raw, i)
			parts = append(parts, text)
			i = next
		default:
			i++
		}
	}
	return strings.Join(parts, ""), len(raw)
}

func readPDFHexToken(raw string, start int) (string, int) {
	end := start + 1
	for end < len(raw) && raw[end] != '>' {
		end++
	}
	if end >= len(raw) {
		return decodePDFHexString(raw[start+1:]), len(raw)
	}
	return decodePDFHexString(raw[start+1 : end]), end + 1
}

func readPDFOperatorToken(raw string, start int) (string, int) {
	if raw[start] == '\'' || raw[start] == '"' {
		return raw[start : start+1], start + 1
	}
	end := start
	for end < len(raw) && isPDFOperatorByte(raw[end]) && raw[end] != '\'' && raw[end] != '"' {
		end++
	}
	return raw[start:end], end
}

func writePDFExtractedText(out *strings.Builder, text string, forceNewLine bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if forceNewLine {
		writePDFLineBreak(out)
	} else if out.Len() > 0 {
		last := out.String()[out.Len()-1]
		if last != '\n' && last != ' ' {
			out.WriteByte(' ')
		}
	}
	out.WriteString(text)
}

func writePDFLineBreak(out *strings.Builder) {
	if out.Len() == 0 {
		return
	}
	if out.String()[out.Len()-1] != '\n' {
		out.WriteByte('\n')
	}
}

func isPDFWhitespace(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

func isPDFOperatorByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '*' || c == '\'' || c == '"'
}

func decodePDFLiteralString(input string) string {
	var b strings.Builder
	for i := 0; i < len(input); i++ {
		if input[i] != '\\' || i+1 >= len(input) {
			b.WriteByte(input[i])
			continue
		}
		i++
		switch input[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '(', ')', '\\':
			b.WriteByte(input[i])
		case '\n':
		case '\r':
			if i+1 < len(input) && input[i+1] == '\n' {
				i++
			}
		default:
			if input[i] >= '0' && input[i] <= '7' {
				val := int(input[i] - '0')
				for j := 0; j < 2 && i+1 < len(input) && input[i+1] >= '0' && input[i+1] <= '7'; j++ {
					i++
					val = val*8 + int(input[i]-'0')
				}
				b.WriteByte(byte(val))
			} else {
				b.WriteByte(input[i])
			}
		}
	}
	return b.String()
}

func decodePDFHexString(input string) string {
	hexChars := make([]byte, 0, len(input))
	for i := 0; i < len(input); i++ {
		c := input[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			hexChars = append(hexChars, c)
		}
	}
	if len(hexChars)%2 == 1 {
		hexChars = append(hexChars, '0')
	}
	out := make([]byte, 0, len(hexChars)/2)
	for i := 0; i+1 < len(hexChars); i += 2 {
		var v byte
		for _, c := range hexChars[i : i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v += c - '0'
			case c >= 'a' && c <= 'f':
				v += c - 'a' + 10
			case c >= 'A' && c <= 'F':
				v += c - 'A' + 10
			}
		}
		out = append(out, v)
	}
	return string(out)
}

func printableRatio(s string) float64 {
	total := 0
	printable := 0
	for _, r := range s {
		total++
		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			printable++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(printable) / float64(total)
}

func normalizeKnowledgeWhitespace(input string) string {
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		out = append(out, strings.Join(fields, " "))
	}
	return strings.Join(out, "\n")
}
