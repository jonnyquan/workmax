package workagent

// file_service_upload.go owns the multipart-upload → disk pipeline:
// constants, the UploadedFile result type, ProcessUploadedFile +
// ProcessUploadedFileWithCustomPath, the bounded-write helper, MIME
// validation, unique-name generation, and path-based read/delete.
//
// Pulled out of file_service.go (B2 chunk 2) so the upload concern
// can be read in one file. All methods stay on FileService receiver
// so existing call sites work unchanged.

import (
	"crypto/md5"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"server/globals"
)

const (
	defaultUploadPath = "uploads/workagent"
	defaultMaxSize    = 50 * 1024 * 1024 // 50MB
)

// UploadedFile represents an uploaded file
type UploadedFile struct {
	Id   string
	Name string
	Path string
	Type string
	Size int64
}

// ProcessUploadedFile processes an uploaded file
func (s *FileService) ProcessUploadedFile(
	file multipart.File,
	header *multipart.FileHeader,
	userID uint,
) (*UploadedFile, error) {
	// Check file size
	if header.Size > defaultMaxSize {
		return nil, fmt.Errorf("file size exceeds limit of %d MB", defaultMaxSize/1024/1024)
	}

	// Validate file type
	fileType := s.getUploadFileType(header.Filename)
	if !s.isAllowedFileType(fileType) {
		return nil, fmt.Errorf("file type %s is not supported", fileType)
	}
	if err := validateUploadContent(file, header.Filename, fileType); err != nil {
		return nil, err
	}

	// Ensure upload directory exists
	if err := os.MkdirAll(defaultUploadPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create upload directory: %w", err)
	}

	// Generate unique filename
	uniqueName := s.generateUniqueFileName(header.Filename, userID)
	filePath := filepath.Join(defaultUploadPath, uniqueName)

	written, err := writeUploadCapped(filePath, file, defaultMaxSize)
	if err != nil {
		return nil, err
	}

	globals.Info(fmt.Sprintf("[FileService] File uploaded successfully: %s", header.Filename))

	fileID := fmt.Sprintf("file_%x", md5.Sum([]byte(filePath)))

	return &UploadedFile{
		Id:   fileID,
		Name: header.Filename,
		Path: filePath,
		Type: fileType,
		Size: written,
	}, nil
}

// ProcessUploadedFileWithCustomPath processes an uploaded file to a custom path
func (s *FileService) ProcessUploadedFileWithCustomPath(
	file multipart.File,
	header *multipart.FileHeader,
	customPath string,
	userID uint,
) (*UploadedFile, error) {
	// Check file size
	if header.Size > defaultMaxSize {
		return nil, fmt.Errorf("file size exceeds limit of %d MB", defaultMaxSize/1024/1024)
	}

	// Validate file type
	fileType := s.getUploadFileType(header.Filename)
	if !s.isAllowedFileType(fileType) {
		return nil, fmt.Errorf("file type %s is not supported", fileType)
	}
	if err := validateUploadContent(file, header.Filename, fileType); err != nil {
		return nil, err
	}

	// Ensure directory exists
	dir := filepath.Dir(customPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	written, err := writeUploadCapped(customPath, file, defaultMaxSize)
	if err != nil {
		return nil, err
	}

	globals.Info(fmt.Sprintf("[FileService] File uploaded to custom path: %s", customPath))

	fileID := fmt.Sprintf("file_%x", md5.Sum([]byte(customPath)))

	return &UploadedFile{
		Id:   fileID,
		Name: header.Filename,
		Path: customPath,
		Type: fileType,
		Size: written,
	}, nil
}

// writeUploadCapped writes src to dst with two stability properties:
//
//  1. The total bytes written are capped at maxBytes. header.Size from
//     a multipart upload is client-declared and not enforced — a
//     malicious client can claim 1KB and stream 100GB. io.CopyN with
//     maxBytes+1 lets us detect "more bytes available" and refuse
//     instead of filling the disk.
//  2. Destination open uses O_TRUNC|O_NOFOLLOW. O_NOFOLLOW makes a
//     pre-existing symlink at dst fail with ELOOP rather than
//     redirect the write to wherever the link points (predictable
//     upload filename pattern + attacker pre-plant). O_TRUNC
//     preserves the legitimate "re-upload replaces existing file"
//     UX for the same-user-same-thread-same-name path; O_EXCL would
//     break that flow.
//
// On any error the partial dst is removed; callers don't need to
// clean up.
func writeUploadCapped(dst string, src io.Reader, maxBytes int64) (int64, error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC | syscall.O_NOFOLLOW
	out, err := os.OpenFile(dst, flags, 0o644)
	if err != nil {
		return 0, fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	// Copy at most maxBytes+1 — if the +1 byte arrives, the source
	// declared a smaller size than it actually has. Refuse.
	written, err := io.CopyN(out, src, maxBytes+1)
	if err != nil && err != io.EOF {
		_ = os.Remove(dst)
		return 0, fmt.Errorf("failed to save file: %w", err)
	}
	if written > maxBytes {
		_ = os.Remove(dst)
		return 0, fmt.Errorf("upload exceeded declared size (limit %d bytes)", maxBytes)
	}
	return written, nil
}

func validateUploadContent(file multipart.File, filename string, fileType string) error {
	var buf [512]byte
	n, readErr := file.Read(buf[:])
	if readErr != nil && readErr != io.EOF {
		return fmt.Errorf("failed to inspect upload content: %w", readErr)
	}
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		return fmt.Errorf("failed to rewind upload content: %w", seekErr)
	}
	if n == 0 {
		return fmt.Errorf("empty upload is not allowed")
	}
	mimeType := http.DetectContentType(buf[:n])
	if uploadContentAllowed(filename, fileType, mimeType, buf[:n]) {
		return nil
	}
	return fmt.Errorf("file content type %s does not match %s upload", mimeType, fileType)
}

func uploadContentAllowed(filename string, fileType string, mimeType string, sniff []byte) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch fileType {
	case "image":
		return strings.HasPrefix(mimeType, "image/")
	case "pdf":
		return mimeType == "application/pdf"
	case "json":
		return mimeType == "application/json" || strings.HasPrefix(mimeType, "text/plain")
	case "csv", "text":
		return strings.HasPrefix(mimeType, "text/plain") || mimeType == "application/json"
	case "word":
		if ext == ".docx" {
			return mimeType == "application/zip"
		}
		return mimeType == "application/msword" || (mimeType == "application/octet-stream" && isOLECompoundDocument(sniff))
	case "excel":
		if ext == ".xlsx" || ext == ".xlsm" || ext == ".xlsb" {
			return mimeType == "application/zip"
		}
		return mimeType == "application/vnd.ms-excel" || (mimeType == "application/octet-stream" && isOLECompoundDocument(sniff))
	default:
		return false
	}
}

func isOLECompoundDocument(sniff []byte) bool {
	if len(sniff) < 8 {
		return false
	}
	magic := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
	for i, b := range magic {
		if sniff[i] != b {
			return false
		}
	}
	return true
}

// getUploadFileType determines file type from filename
func (s *FileService) getUploadFileType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".xlsx", ".xls", ".xlsm", ".xlsb":
		return "excel"
	case ".csv":
		return "csv"
	case ".txt", ".md":
		return "text"
	case ".json":
		return "json"
	case ".pdf":
		return "pdf"
	case ".docx", ".doc":
		return "word"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".html", ".htm":
		return "html"
	default:
		return "unknown"
	}
}

// isAllowedFileType checks if file type is allowed. The "unknown" bucket from
// getUploadFileType() is intentionally excluded — otherwise any unrecognized
// extension (.exe, .sh, .bat, .js, etc.) would be silently accepted.
//
// "html" is intentionally NOT on this list. The FilePreviewDialog renders
// HTML via <iframe srcDoc sandbox="allow-same-origin allow-scripts"> —
// per spec those two flags together are equivalent to no sandbox at all,
// so a script in the document executes in the parent origin with the
// viewer's cookies. Once a thread is shared via the public share endpoint
// (see 9c447862), an attacker-uploaded evil.html runs against the
// recipient. Refuse uploads here at the server boundary; agent-generated
// HTML outputs (e.g. Plotly fig.write_html) take a different code path
// (RegisterOutputFile / SyncOutputFiles) and remain unaffected.
func (s *FileService) isAllowedFileType(fileType string) bool {
	allowedTypes := map[string]bool{
		"excel": true, "csv": true, "text": true,
		"json": true, "pdf": true, "word": true,
		"image": true,
	}
	return allowedTypes[fileType]
}

// generateUniqueFileName generates a unique filename
func (s *FileService) generateUniqueFileName(originalName string, userID uint) string {
	ext := filepath.Ext(originalName)
	nameWithoutExt := strings.TrimSuffix(originalName, ext)

	// Clean filename
	nameWithoutExt = strings.ReplaceAll(nameWithoutExt, " ", "_")
	nameWithoutExt = strings.ReplaceAll(nameWithoutExt, "/", "_")
	nameWithoutExt = strings.ReplaceAll(nameWithoutExt, "\\", "_")

	timestamp := time.Now().Format("20060102150405")
	return fmt.Sprintf("%d_%s_%s%s", userID, nameWithoutExt, timestamp, ext)
}

// GetFileByPath retrieves file information by path
func (s *FileService) GetFileByPath(filePath string) (*UploadedFile, error) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("file not found: %s", filePath)
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	fileName := filepath.Base(filePath)
	fileType := s.getUploadFileType(fileName)
	fileID := fmt.Sprintf("file_%x", md5.Sum([]byte(filePath)))

	return &UploadedFile{
		Id:   fileID,
		Name: fileName,
		Path: filePath,
		Type: fileType,
		Size: fileInfo.Size(),
	}, nil
}

// DeleteFileByPath deletes a file by path
func (s *FileService) DeleteFileByPath(filePath string) error {
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}
