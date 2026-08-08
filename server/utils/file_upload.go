package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileType represents the type of file being uploaded
type FileType string

const (
	FileTypeImage    FileType = "image"
	FileTypeTemplate FileType = "template"
)

// UploadConfig holds configuration for file uploads
type UploadConfig struct {
	MaxSize      int64    // Maximum file size in bytes
	AllowedTypes []string // Allowed MIME types
	UploadDir    string   // Upload directory
}

// FileUploadResult represents the result of a file upload
type FileUploadResult struct {
	FileName    string `json:"fileName"`
	FilePath    string `json:"filePath"`
	FileURL     string `json:"fileUrl"`
	FileSize    int64  `json:"fileSize"`
	ContentType string `json:"contentType"`
}

// TemplateUploadConfig holds configuration for template file uploads with category
type TemplateUploadConfig struct {
	MaxSize      int64    // Maximum file size in bytes
	AllowedTypes []string // Allowed MIME types
	BaseDir      string   // Base upload directory
	Category     string   // Category slug for directory structure
	FileType     FileType // Type of file being uploaded
}

// GetUploadConfig returns configuration for different file types
func GetUploadConfig(fileType FileType) UploadConfig {
	switch fileType {
	case FileTypeImage:
		return UploadConfig{
			MaxSize: 5 * 1024 * 1024, // 5MB
			AllowedTypes: []string{
				"image/jpeg",
				"image/png",
				"image/svg+xml",
			},
			UploadDir: "uploads/images",
		}
	case FileTypeTemplate:
		return UploadConfig{
			MaxSize: 50 * 1024 * 1024, // 50MB
			AllowedTypes: []string{
				"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", // .xlsx
				"application/vnd.ms-excel", // .xls
				"text/csv",                 // .csv
			},
			UploadDir: "uploads/templates",
		}
	default:
		return UploadConfig{
			MaxSize:      1 * 1024 * 1024, // 1MB default
			AllowedTypes: []string{},
			UploadDir:    "uploads/misc",
		}
	}
}

// GetTemplateUploadConfig returns configuration for template files with category structure
func GetTemplateUploadConfig(fileType FileType, categorySlug string) TemplateUploadConfig {
	switch fileType {
	case FileTypeImage:
		return TemplateUploadConfig{
			MaxSize: 5 * 1024 * 1024, // 5MB
			AllowedTypes: []string{
				"image/jpeg",
				"image/png",
				"image/svg+xml",
			},
			BaseDir:  "uploads/templates",
			Category: categorySlug,
			FileType: fileType,
		}
	case FileTypeTemplate:
		return TemplateUploadConfig{
			MaxSize: 50 * 1024 * 1024, // 50MB
			AllowedTypes: []string{
				"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", // .xlsx
				"application/vnd.ms-excel", // .xls
				"text/csv",                 // .csv
			},
			BaseDir:  "uploads/templates",
			Category: categorySlug,
			FileType: fileType,
		}
	default:
		return TemplateUploadConfig{
			MaxSize:      1 * 1024 * 1024, // 1MB default
			AllowedTypes: []string{},
			BaseDir:      "uploads/misc",
			Category:     categorySlug,
			FileType:     fileType,
		}
	}
}

// GenerateFileName generates a unique filename with timestamp and random string
func GenerateFileName(originalName string) string {
	ext := filepath.Ext(originalName)
	timestamp := time.Now().Unix()

	// Generate random string
	randomBytes := make([]byte, 8)
	rand.Read(randomBytes)
	randomString := hex.EncodeToString(randomBytes)

	return fmt.Sprintf("%d_%s%s", timestamp, randomString, ext)
}

// GenerateTemplateFileName generates filename for template files based on type
func GenerateTemplateFileName(originalName string, fileType FileType) string {
	ext := filepath.Ext(originalName)
	timestamp := time.Now().Unix()

	// Generate random string
	randomBytes := make([]byte, 8)
	rand.Read(randomBytes)
	randomString := hex.EncodeToString(randomBytes)

	if fileType == FileTypeImage {
		// For cover images, add "_cover" suffix
		return fmt.Sprintf("%d_%s_cover%s", timestamp, randomString, ext)
	}
	// For template files, use regular naming
	return fmt.Sprintf("%d_%s%s", timestamp, randomString, ext)
}

// SanitizeFilename strips any directory components from the filename to prevent
// path traversal attacks (e.g., "../../etc/passwd" → "passwd").
func SanitizeFilename(name string) string {
	// filepath.Base extracts the last element, stripping all directory parts
	clean := filepath.Base(name)
	// Reject names that resolve to current/parent directory
	if clean == "." || clean == ".." {
		return "unnamed"
	}
	return clean
}

// detectContentType reads the first 512 bytes of the file to detect its real
// MIME type via magic bytes (file signature), instead of trusting the client
// Content-Type header which can be trivially spoofed.
func detectContentType(fileHeader *multipart.FileHeader) (string, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open file for content detection: %v", err)
	}
	defer file.Close()

	// http.DetectContentType needs at most 512 bytes
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read file header: %v", err)
	}

	// Reset the file position for subsequent reads
	if seeker, ok := file.(io.Seeker); ok {
		seeker.Seek(0, io.SeekStart)
	}

	return http.DetectContentType(buf[:n]), nil
}

// validateFileCore is the unified validation logic shared by ValidateFile and
// ValidateTemplateFile. It checks file size, detects the real content type via
// magic bytes, and verifies it against the allowed list.
func validateFileCore(fileHeader *multipart.FileHeader, maxSize int64, allowedTypes []string) error {
	// 1. Check file size
	if fileHeader.Size > maxSize {
		return fmt.Errorf("file size %d bytes exceeds maximum allowed size %d bytes", fileHeader.Size, maxSize)
	}

	// 2. Detect real content type via magic bytes
	if len(allowedTypes) > 0 {
		detected, err := detectContentType(fileHeader)
		if err != nil {
			return err
		}

		allowed := false
		for _, allowedType := range allowedTypes {
			// Use prefix match because DetectContentType may return "text/xml; charset=utf-8"
			// for SVG files, while our allowlist has "image/svg+xml".
			// For images, exact match is reliable ("image/jpeg", "image/png").
			if detected == allowedType || strings.HasPrefix(detected, allowedType) {
				allowed = true
				break
			}
		}

		// Fallback for types that magic bytes can't distinguish well (e.g., SVG detected
		// as "text/xml", CSV as "text/plain", XLSX as "application/zip").
		// In these cases, also check extension as a secondary signal.
		if !allowed {
			ext := strings.ToLower(filepath.Ext(SanitizeFilename(fileHeader.Filename)))
			extensionTypeMap := map[string]string{
				".svg":  "image/svg+xml",
				".csv":  "text/csv",
				".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
				".xls":  "application/vnd.ms-excel",
			}
			if mappedType, ok := extensionTypeMap[ext]; ok {
				for _, allowedType := range allowedTypes {
					if mappedType == allowedType {
						allowed = true
						break
					}
				}
			}
		}

		if !allowed {
			return fmt.Errorf("file type %s is not allowed. Allowed types: %s", detected, strings.Join(allowedTypes, ", "))
		}
	}

	return nil
}

// ValidateFile validates the uploaded file against the configuration using magic bytes detection.
func ValidateFile(fileHeader *multipart.FileHeader, config UploadConfig) error {
	return validateFileCore(fileHeader, config.MaxSize, config.AllowedTypes)
}

// ValidateTemplateFile validates the uploaded file against the template configuration using magic bytes detection.
func ValidateTemplateFile(fileHeader *multipart.FileHeader, config TemplateUploadConfig) error {
	return validateFileCore(fileHeader, config.MaxSize, config.AllowedTypes)
}

// SaveUploadedFile saves the uploaded file to the specified directory
func SaveUploadedFile(fileHeader *multipart.FileHeader, config UploadConfig) (*FileUploadResult, error) {
	// Validate file
	if err := ValidateFile(fileHeader, config); err != nil {
		return nil, err
	}

	// Create upload directory if it doesn't exist
	if err := os.MkdirAll(config.UploadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create upload directory: %v", err)
	}

	// Sanitize and generate unique filename (prevents path traversal)
	fileName := GenerateFileName(SanitizeFilename(fileHeader.Filename))
	filePath := filepath.Join(config.UploadDir, fileName)

	// Open uploaded file
	src, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open uploaded file: %v", err)
	}
	defer src.Close()

	// Create destination file
	dst, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create destination file: %v", err)
	}
	defer dst.Close()

	// Copy file contents
	if _, err := io.Copy(dst, src); err != nil {
		return nil, fmt.Errorf("failed to save file: %v", err)
	}

	// Generate file URL (relative to web root)
	fileURL := "/" + strings.ReplaceAll(filePath, "\\", "/")

	return &FileUploadResult{
		FileName:    fileName,
		FilePath:    filePath,
		FileURL:     fileURL,
		FileSize:    fileHeader.Size,
		ContentType: fileHeader.Header.Get("Content-Type"),
	}, nil
}

// DeleteFile deletes a file from the filesystem
func DeleteFile(filePath string) error {
	if filePath == "" {
		return nil
	}

	// Remove leading slash if present
	if strings.HasPrefix(filePath, "/") {
		filePath = filePath[1:]
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil // File doesn't exist, nothing to delete
	}

	return os.Remove(filePath)
}

// SaveTemplateUploadedFile saves the uploaded file to the category-based directory structure
func SaveTemplateUploadedFile(fileHeader *multipart.FileHeader, config TemplateUploadConfig) (*FileUploadResult, error) {
	// Validate file
	if err := ValidateTemplateFile(fileHeader, config); err != nil {
		return nil, err
	}

	// Create category-based upload directory
	uploadDir := filepath.Join(config.BaseDir, config.Category)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create upload directory: %v", err)
	}

	// Sanitize and generate unique filename (prevents path traversal)
	fileName := GenerateTemplateFileName(SanitizeFilename(fileHeader.Filename), config.FileType)
	filePath := filepath.Join(uploadDir, fileName)

	// Open uploaded file
	src, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open uploaded file: %v", err)
	}
	defer src.Close()

	// Create destination file
	dst, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create destination file: %v", err)
	}
	defer dst.Close()

	// Copy file contents
	if _, err := io.Copy(dst, src); err != nil {
		return nil, fmt.Errorf("failed to save file: %v", err)
	}

	// Generate file URL (relative to web root)
	fileURL := "/" + strings.ReplaceAll(filePath, "\\", "/")

	return &FileUploadResult{
		FileName:    fileName,
		FilePath:    filePath,
		FileURL:     fileURL,
		FileSize:    fileHeader.Size,
		ContentType: fileHeader.Header.Get("Content-Type"),
	}, nil
}

// GetFileExtension returns the file extension for a given MIME type
func GetFileExtension(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/svg+xml":
		return ".svg"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "text/csv":
		return ".csv"
	default:
		return ""
	}
}
