package workagent

import (
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
)

// =============================================================================
// FileManager - Centralized file operations and URL building
// =============================================================================

// FileManager provides centralized file operations and URL building
type FileManager struct {
	workspaceRoutePrefix string
}

// NewFileManager creates a new FileManager instance
func NewFileManager() *FileManager {
	return &FileManager{
		workspaceRoutePrefix: workagentService.WorkspaceRoutePrefix(),
	}
}

// DefaultFileManager is a singleton instance for convenience
var DefaultFileManager = NewFileManager()

// BuildFileUrl builds a complete HTTP URL for a workspace file path.
func (fm *FileManager) BuildFileUrl(path string, subdir string) string {
	if path == "" {
		return ""
	}

	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}

	if strings.HasPrefix(path, "/"+fm.workspaceRoutePrefix+"/") {
		return path
	}

	if isAbsoluteFilesystemPath(path) {
		relPath := extractWorkspaceRelativePath(path)
		if relPath != "" {
			return fmt.Sprintf("/%s/%s", fm.workspaceRoutePrefix, relPath)
		}
		globals.Warn(fmt.Sprintf("[FileManager] Cannot convert absolute path to URL: %s", path))
		return ""
	}

	cleanPath := strings.TrimPrefix(path, "/")
	if subdir != "" && !strings.Contains(cleanPath, subdir) {
		cleanPath = fmt.Sprintf("%s/%s", subdir, cleanPath)
	}

	return fmt.Sprintf("/%s/%s", fm.workspaceRoutePrefix, cleanPath)
}

// BuildOutputFileUrl builds a URL for an output file
func (fm *FileManager) BuildOutputFileUrl(workspaceRelPath, filename string) string {
	if filename == "" {
		return ""
	}
	escapedFilename := url.PathEscape(filename)
	return fmt.Sprintf("/%s/%s/outputs/%s", fm.workspaceRoutePrefix, workspaceRelPath, escapedFilename)
}

// BuildUploadFileUrl builds a URL for an uploaded file
func (fm *FileManager) BuildUploadFileUrl(workspaceRelPath, filename string) string {
	if filename == "" {
		return ""
	}
	escapedFilename := url.PathEscape(filename)
	return fmt.Sprintf("/%s/%s/uploads/%s", fm.workspaceRoutePrefix, workspaceRelPath, escapedFilename)
}

// ResolveWorkspaceFilePath resolves a workspace-relative file path to an
// absolute on-disk path, refusing absolute inputs and anything that escapes
// workspaceRoot. Returns "" when the path would leave the workspace — callers
// must treat that as "file not found".
func (fm *FileManager) ResolveWorkspaceFilePath(filePath string) string {
	agentManager := workagentService.GetAgentClientManager()
	root := ""
	if agentManager != nil {
		root = agentManager.WorkspaceRoot()
	}
	if strings.TrimSpace(root) == "" {
		root = workspaceRoot()
	}
	return workagentService.ResolveInsideWorkspace(root, filePath)
}

// FileExists checks if a file exists at the given path
func (fm *FileManager) FileExists(filePath string) bool {
	absPath := fm.ResolveWorkspaceFilePath(filePath)
	_, err := os.Stat(absPath)
	return err == nil
}

// GetWorkspaceRelativePath converts an absolute path to workspace-relative path
func (fm *FileManager) GetWorkspaceRelativePath(absPath string) string {
	agentManager := workagentService.GetAgentClientManager()
	if agentManager == nil {
		return ""
	}

	workspaceRoot := agentManager.WorkspaceRoot()
	relPath, err := filepath.Rel(workspaceRoot, absPath)
	if err != nil {
		return ""
	}

	return filepath.ToSlash(relPath)
}

// =============================================================================
// Convenience functions for global access
// =============================================================================

func BuildFileUrl(path string, subdir string) string {
	return DefaultFileManager.BuildFileUrl(path, subdir)
}

func BuildOutputFileUrl(workspaceRelPath, filename string) string {
	return DefaultFileManager.BuildOutputFileUrl(workspaceRelPath, filename)
}

func BuildUploadFileUrl(workspaceRelPath, filename string) string {
	return DefaultFileManager.BuildUploadFileUrl(workspaceRelPath, filename)
}

func ResolveWorkspaceFilePath(filePath string) string {
	return DefaultFileManager.ResolveWorkspaceFilePath(filePath)
}

// =============================================================================
// Path helper functions
// =============================================================================

func isAbsoluteFilesystemPath(path string) bool {
	unixPattern := regexp.MustCompile(`^/(?:Users|home|tmp|var|opt|root|mnt|data)/`)
	return unixPattern.MatchString(path)
}

func extractWorkspaceRelativePath(absPath string) string {
	agentWorkspacePattern := regexp.MustCompile(`/agent_workspace[^/]*/(.+)$`)
	if matches := agentWorkspacePattern.FindStringSubmatch(absPath); len(matches) > 1 {
		return matches[1]
	}

	dateThreadPattern := regexp.MustCompile(`(\d{8}/thread_[a-f0-9-]+/(?:uploads|outputs)/.+)$`)
	if matches := dateThreadPattern.FindStringSubmatch(absPath); len(matches) > 1 {
		return matches[1]
	}

	return ""
}

// =============================================================================
// File type detection
// =============================================================================

func detectFileType(ext string) string {
	switch strings.ToLower(ext) {
	case ".xlsx", ".xls":
		return "excel"
	case ".csv":
		return "csv"
	case ".html", ".htm":
		return "html"
	case ".json":
		return "json"
	case ".pdf":
		return "pdf"
	case ".pptx", ".ppt":
		return "pptx"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".txt", ".md", ".log":
		return "text"
	default:
		return "other"
	}
}

func generateFileDescription(fileName, fileType string) string {
	switch fileType {
	case "excel":
		return fmt.Sprintf("Excel spreadsheet: %s", fileName)
	case "csv":
		return fmt.Sprintf("CSV data file: %s", fileName)
	case "html":
		return fmt.Sprintf("HTML report: %s", fileName)
	case "json":
		return fmt.Sprintf("JSON data: %s", fileName)
	case "pdf":
		return fmt.Sprintf("PDF document: %s", fileName)
	case "image":
		return fmt.Sprintf("Image file: %s", fileName)
	case "pptx":
		return fmt.Sprintf("PowerPoint presentation: %s", fileName)
	case "text":
		return fmt.Sprintf("Text file: %s", fileName)
	default:
		return fmt.Sprintf("Generated file: %s", fileName)
	}
}

// =============================================================================
// Directory operations
// =============================================================================

// FileOutputInfo represents a generated output file
type FileOutputInfo struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Size        int64     `json:"size"`
	Path        string    `json:"path"`
	ThreadID    uint      `json:"threadId,omitempty"`
	ThreadUUID  string    `json:"threadUUID,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	Description string    `json:"description,omitempty"`
	MimeType    string    `json:"mimeType,omitempty"`
	DownloadURL string    `json:"downloadUrl,omitempty"`
	PreviewURL  string    `json:"previewUrl,omitempty"`
}

func scanOutputsDirectory(threadWorkspace string) []FileOutputInfo {
	return scanOutputsDirectorySince(threadWorkspace, time.Time{})
}

func scanOutputsDirectorySince(threadWorkspace string, since time.Time) []FileOutputInfo {
	outputsPath := filepath.Join(threadWorkspace, "outputs")
	var files []FileOutputInfo

	if _, err := os.Stat(outputsPath); os.IsNotExist(err) {
		return files
	}

	err := filepath.Walk(outputsPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if path != outputsPath && strings.HasPrefix(filepath.Base(path), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}

		if !since.IsZero() && info.ModTime().Before(since) {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(info.Name()))
		fileType := detectFileType(ext)
		mimeType := mime.TypeByExtension(ext)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		if fileType == "pptx" {
			if _, convErr := ensurePresentationPreviewPDF(path); shouldLogPreviewConversionError(convErr) {
				globals.Warn(fmt.Sprintf("[scanOutputsDirectorySince] PPT preview conversion skipped for %s: %v", path, convErr))
			}
		}

		description := workagentService.VariationDraftDescriptionForOutput(threadWorkspace, path)
		if description == "" {
			description = generateFileDescription(info.Name(), fileType)
		}

		files = append(files, FileOutputInfo{
			Name:        info.Name(),
			Type:        fileType,
			Size:        info.Size(),
			Path:        path,
			CreatedAt:   info.ModTime(),
			Description: description,
			MimeType:    mimeType,
		})

		return nil
	})

	if err != nil {
		globals.Warn(fmt.Sprintf("[scanOutputsDirectorySince] Error walking outputs: %v", err))
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].CreatedAt.After(files[j].CreatedAt)
	})

	return files
}

func clearOutputsDirectory(threadWorkspace string) (int, error) {
	outputsDir := filepath.Join(threadWorkspace, "outputs")

	entries, err := os.ReadDir(outputsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	deletedCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(outputsDir, entry.Name())
		if err := os.Remove(filePath); err != nil {
			globals.Warn(fmt.Sprintf("Failed to delete output file %s: %v", filePath, err))
			continue
		}
		deletedCount++
	}

	return deletedCount, nil
}

func clearUploadsDirectory(threadWorkspace string) (int, error) {
	uploadsDir := filepath.Join(threadWorkspace, "uploads")

	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	deletedCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(uploadsDir, entry.Name())
		if err := os.Remove(filePath); err != nil {
			globals.Warn(fmt.Sprintf("Failed to delete upload file %s: %v", filePath, err))
			continue
		}
		deletedCount++
	}

	return deletedCount, nil
}

// buildFileOutputData builds file output data structure for API responses
func (api *AIChatApiNew) buildFileOutputData(
	thread *workagentModel.ChatThread,
	files []FileOutputInfo,
) map[string]interface{} {
	// Get workspace relative path. Surface EnsureThreadWorkspace
	// errors instead of silently swallowing them — on failure
	// threadWorkspace is "" and every BuildOutputFileUrl call below
	// emits a malformed `/agent_workspace//<file>` URL that 404s on
	// the frontend with no server-side breadcrumb. Same posture as
	// 37d2186d / b0766676. Continue building the response (the file
	// rows themselves are still useful — name, size, type are
	// independent of the URL builder), but the URLs will be empty.
	manager := workagentService.GetAgentClientManager()
	threadWorkspace, err := manager.EnsureThreadWorkspace(thread.UID, thread.UUID)
	if err != nil {
		globals.Warn(fmt.Sprintf("[buildFileOutputData] EnsureThreadWorkspace failed for thread=%d uid=%d uuid=%s: %v (download/preview URLs in this response will be empty)", thread.Id, thread.UID, thread.UUID, err))
	}
	workspaceRelPath := DefaultFileManager.GetWorkspaceRelativePath(threadWorkspace)

	fileOutputs := make([]map[string]interface{}, 0, len(files))
	for _, f := range files {
		staticPath := BuildOutputFileUrl(workspaceRelPath, f.Name)
		previewPath := staticPath

		if f.Type == "pptx" && isPresentationPath(f.Path) {
			if previewPDFPath, convErr := ensurePresentationPreviewPDF(f.Path); convErr == nil && previewPDFPath != "" {
				previewPath = BuildOutputFileUrl(workspaceRelPath, filepath.Base(previewPDFPath))
			} else if shouldLogPreviewConversionError(convErr) {
				globals.Warn(fmt.Sprintf("[buildFileOutputData] PPT preview conversion skipped for %s: %v", f.Path, convErr))
			}
		}

		fileOutput := map[string]interface{}{
			"name":        f.Name,
			"type":        f.Type,
			"size":        f.Size,
			"path":        f.Path,
			"threadId":    thread.Id,
			"threadUUID":  thread.UUID,
			"downloadUrl": staticPath,
			"previewUrl":  previewPath,
			"createdAt":   f.CreatedAt.UnixMilli(),
			"mimeType":    f.MimeType,
		}

		if f.Description != "" {
			fileOutput["description"] = f.Description
		}

		fileOutputs = append(fileOutputs, fileOutput)
	}

	return map[string]interface{}{
		"files": fileOutputs,
		"count": len(fileOutputs),
	}
}
