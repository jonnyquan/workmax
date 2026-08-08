package canvas

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"server/globals"
	workagentService "server/service/tools/workagent"
	"server/utils/safehttp"

	"github.com/google/uuid"
)

// ErrorResultPayload returns the canonical "agent failed" SSE result
// payload — the FE consumes this same shape regardless of which handler
// serializes it. Lives here because the payload shape is part of the
// canvas-agent contract, not HTTP plumbing.
func ErrorResultPayload() json.RawMessage {
	payload := map[string]interface{}{
		"type":     "result",
		"subtype":  "error",
		"is_error": true,
		"error":    "An error occurred while processing your canvas request. Please try again.",
	}
	data, _ := json.Marshal(payload)
	return json.RawMessage(data)
}

// attachmentWriteFlags is the open flag set used by every canvas-agent
// attachment write (URL download + base64 decode). O_NOFOLLOW makes a
// pre-existing symlink at destPath fail with ELOOP rather than
// redirect the write to wherever the link points (predictable
// uploads/<sanitized-filename> path; concurrent agent runs could plant
// symlinks across each other's threads on a shared box). O_TRUNC
// preserves the legitimate "re-download replaces the previous version"
// semantic — same trade-off as workagent's writeUploadCapped.
const attachmentWriteFlags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC | syscall.O_NOFOLLOW

// Canvas Agent constants
const (
	CanvasAgentType = "canvas"
	CanvasAgentMode = "canvas"
)

const (
	MaxRequestBodyBytes                      = 48 << 20 // 48 MiB
	maxCanvasAgentMessages                   = 30
	maxCanvasAgentMessageChars               = 6000
	maxCanvasAgentTotalMessageChars          = 60000
	maxCanvasAgentContextElements            = 300
	maxCanvasAgentSelectedIDs                = 200
	maxCanvasAgentAttachments                = 5
	maxCanvasAgentAttachmentBytes            = 25 << 20 // 25 MiB
	maxCanvasAgentAttachmentBase64Chars      = ((maxCanvasAgentAttachmentBytes + 2) / 3) * 4
	maxCanvasAgentTotalAttachmentBase64Chars = ((32 << 20) + 2) / 3 * 4
	maxCanvasAgentFilenameChars              = 255
	maxCanvasAgentURLChars                   = 2048
)

var allowUnsafeAttachmentDownloadsForTest bool

// ─── Utilities ──────────────────────────────────────────────────────────────

func TruncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func NormalizeConversationID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func ValidateRequest(req CanvasAgentChatRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages array is required")
	}
	if len(req.Messages) > maxCanvasAgentMessages {
		return fmt.Errorf("too many messages")
	}
	if len(req.Context.Elements) > maxCanvasAgentContextElements {
		return fmt.Errorf("too many canvas elements")
	}
	if len(req.Context.SelectedIDs) > maxCanvasAgentSelectedIDs {
		return fmt.Errorf("too many selected ids")
	}
	if req.Context.Skill != "" && len([]rune(req.Context.Skill)) > 80 {
		return fmt.Errorf("canvas skill is too long")
	}
	if req.Context.ProjectID != "" && (len([]rune(req.Context.ProjectID)) > 64 || strings.ContainsAny(req.Context.ProjectID, "/\\\x00")) {
		return fmt.Errorf("invalid projectId")
	}

	totalMessageChars := 0
	totalAttachmentChars := 0
	for i, msg := range req.Messages {
		role := strings.TrimSpace(msg.Role)
		if role != "user" && role != "assistant" && role != "system" {
			return fmt.Errorf("messages[%d].role is invalid", i)
		}
		contentLen := len([]rune(msg.Content))
		if contentLen > maxCanvasAgentMessageChars {
			return fmt.Errorf("messages[%d].content is too long", i)
		}
		totalMessageChars += contentLen
		if totalMessageChars > maxCanvasAgentTotalMessageChars {
			return fmt.Errorf("messages content is too long")
		}
		if len(msg.Attachments) > maxCanvasAgentAttachments {
			return fmt.Errorf("messages[%d] has too many attachments", i)
		}
		added, err := validateCanvasAgentAttachments(msg.Attachments, fmt.Sprintf("messages[%d].attachments", i))
		if err != nil {
			return err
		}
		totalAttachmentChars += added
	}
	if totalAttachmentChars > maxCanvasAgentTotalAttachmentBase64Chars {
		return fmt.Errorf("attachments are too large")
	}

	added, err := validateCanvasAgentAttachments(req.Attachments, "attachments")
	if err != nil {
		return err
	}
	totalAttachmentChars += added
	if totalAttachmentChars > maxCanvasAgentTotalAttachmentBase64Chars {
		return fmt.Errorf("attachments are too large")
	}

	return nil
}

func validateCanvasAgentAttachments(attachments []CanvasAgentAttachment, path string) (int, error) {
	if len(attachments) > maxCanvasAgentAttachments {
		return 0, fmt.Errorf("%s has too many items", path)
	}
	totalDataChars := 0
	for i, att := range attachments {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		switch strings.TrimSpace(att.Type) {
		case "image", "video", "file":
		default:
			return 0, fmt.Errorf("%s.type is invalid", itemPath)
		}
		if att.Filename != "" && (len([]rune(att.Filename)) > maxCanvasAgentFilenameChars || strings.ContainsAny(att.Filename, "\x00")) {
			return 0, fmt.Errorf("%s.filename is invalid", itemPath)
		}
		if att.MimeType != "" && !isAllowedCanvasAgentMimeType(att.MimeType) {
			return 0, fmt.Errorf("%s.mimeType is invalid", itemPath)
		}
		if strings.TrimSpace(att.URL) != "" {
			trimmedURL := strings.TrimSpace(att.URL)
			if len(trimmedURL) > maxCanvasAgentURLChars || strings.ContainsFunc(trimmedURL, func(r rune) bool { return r < 32 || r == 127 }) {
				return 0, fmt.Errorf("%s.url is too long", itemPath)
			}
			if !(strings.HasPrefix(trimmedURL, "/") && !strings.HasPrefix(trimmedURL, "//")) {
				if _, err := safehttp.ValidateURL(trimmedURL); err != nil {
					return 0, fmt.Errorf("%s.url is invalid", itemPath)
				}
			}
		}
		if att.Data != "" {
			data := att.Data
			if idx := strings.Index(data, ","); idx >= 0 {
				data = data[idx+1:]
			}
			if len(data) > maxCanvasAgentAttachmentBase64Chars {
				return 0, fmt.Errorf("%s.data is too large", itemPath)
			}
			totalDataChars += len(data)
		}
	}
	return totalDataChars, nil
}

func isAllowedCanvasAgentMimeType(mimeType string) bool {
	value := strings.TrimSpace(strings.ToLower(mimeType))
	if value == "" || len(value) > 120 || strings.ContainsFunc(value, func(r rune) bool { return r < 32 || r == 127 }) {
		return false
	}
	return strings.HasPrefix(value, "image/") ||
		strings.HasPrefix(value, "video/") ||
		value == "application/pdf" ||
		value == "application/octet-stream"
}

// PrepareAttachments downloads URL-based attachments or decodes base64 data
// into the workspace uploads/ directory, returning them as AgentFileInfo for the SDK.
func PrepareAttachments(attachments []CanvasAgentAttachment, workspacePath string) []workagentService.AgentFileInfo {
	if len(attachments) == 0 {
		return nil
	}

	uploadsDir := filepath.Join(workspacePath, "uploads")
	_ = os.MkdirAll(uploadsDir, 0o755)

	var agentFiles []workagentService.AgentFileInfo

	for i, att := range attachments {
		if i >= maxCanvasAgentAttachments {
			globals.Warn(fmt.Sprintf("[CanvasAgent] Skipping attachment %d: max attachment count exceeded", i))
			break
		}

		// Determine filename
		filename := sanitizeAttachmentFilename(att.Filename)
		if filename == "" {
			ext := mimeToExtension(att.MimeType)
			filename = fmt.Sprintf("attachment_%d%s", i+1, ext)
		}

		filePath := filepath.Join(uploadsDir, filename)

		var saved bool
		var fileSize int64

		// Prefer URL download
		if att.URL != "" {
			if size, err := downloadFileToPath(att.URL, filePath); err != nil {
				globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to download attachment %s: %v", att.URL, err))
			} else {
				saved = true
				fileSize = size
			}
		}

		// Fallback to base64 data
		if !saved && att.Data != "" {
			// Strip data URI prefix if present (e.g. "data:image/png;base64,...")
			data := att.Data
			if idx := strings.Index(data, ","); idx >= 0 {
				data = data[idx+1:]
			}
			if base64.StdEncoding.DecodedLen(len(data)) > maxCanvasAgentAttachmentBytes {
				globals.Warn(fmt.Sprintf("[CanvasAgent] Skipping attachment %s: decoded size exceeds limit", filename))
			} else if decoded, err := base64.StdEncoding.DecodeString(data); err != nil {
				globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to decode base64 attachment %s: %v", filename, err))
			} else if int64(len(decoded)) > maxCanvasAgentAttachmentBytes {
				globals.Warn(fmt.Sprintf("[CanvasAgent] Skipping attachment %s: decoded size exceeds limit", filename))
			} else {
				// O_NOFOLLOW: refuse to write through a pre-existing
				// symlink at filePath. os.WriteFile's default open
				// follows symlinks and would silently redirect the
				// decoded bytes to the link's target.
				out, openErr := os.OpenFile(filePath, attachmentWriteFlags, 0o644)
				if openErr != nil {
					globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to open attachment %s: %v", filename, openErr))
				} else if _, writeErr := out.Write(decoded); writeErr != nil {
					_ = out.Close()
					_ = os.Remove(filePath)
					globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to write attachment %s: %v", filename, writeErr))
				} else if closeErr := out.Close(); closeErr != nil {
					_ = os.Remove(filePath)
					globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to close attachment %s: %v", filename, closeErr))
				} else {
					saved = true
					fileSize = int64(len(decoded))
				}
			}
		}

		if saved {
			// Stamp ModTime so the filesContext cache fingerprint
			// distinguishes a re-uploaded attachment with otherwise
			// identical (id, name, size). Stat-after-write costs
			// nothing relative to the write itself.
			var modTime int64
			if st, err := os.Stat(filePath); err == nil {
				modTime = st.ModTime().UnixNano()
			}
			agentFiles = append(agentFiles, workagentService.AgentFileInfo{
				ID:      fmt.Sprintf("canvas_att_%d", i),
				Name:    filename,
				Path:    filePath,
				Size:    fileSize,
				Type:    att.Type,
				ModTime: modTime,
			})
			globals.Info(fmt.Sprintf("[CanvasAgent] Prepared attachment: %s (%d bytes)", filename, fileSize))
		}
	}

	return agentFiles
}

// PrepareAttachmentsStrict keeps the existing best-effort helper available for
// tests/legacy callers, but lets the HTTP handler fail fast when the user
// explicitly attached files that cannot be passed to the agent.
func PrepareAttachmentsStrict(attachments []CanvasAgentAttachment, workspacePath string) ([]workagentService.AgentFileInfo, error) {
	files := PrepareAttachments(attachments, workspacePath)
	if len(attachments) > 0 && len(files) < len(attachments) {
		return files, fmt.Errorf("failed to prepare %d of %d canvas agent attachments", len(attachments)-len(files), len(attachments))
	}
	return files, nil
}

func sanitizeAttachmentFilename(filename string) string {
	name := strings.TrimSpace(filename)
	if name == "" {
		return ""
	}

	// Normalize both Unix and Windows separators before taking basename.
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}

	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || strings.ContainsRune(`/\:*?"<>|`, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}

	cleaned := strings.Trim(b.String(), " .")
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return ""
	}

	const maxFilenameRunes = 120
	runes := []rune(cleaned)
	if len(runes) > maxFilenameRunes {
		cleaned = string(runes[:maxFilenameRunes])
	}
	return cleaned
}

// downloadFileToPath downloads a URL to a local file path.
//
// The SSRF guard layer (validate, dial-time re-resolve, redirect
// re-validation) lives in server/utils/safehttp; canvasagent used to
// carry private copies of those helpers but they're shared with the
// comic-page importer now and any future endpoint that needs the
// same protection.
func downloadFileToPath(rawURL, destPath string) (int64, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, fmt.Errorf("invalid attachment url: %w", err)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	if !allowUnsafeAttachmentDownloadsForTest {
		parsed, err = safehttp.ValidateURL(rawURL)
		if err != nil {
			return 0, err
		}
		client.Transport = &http.Transport{
			Proxy:       nil,
			DialContext: safehttp.DialContext,
		}
		client.CheckRedirect = safehttp.CheckRedirect
	}
	resp, err := client.Get(parsed.String())
	if err != nil {
		return 0, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxCanvasAgentAttachmentBytes {
		return 0, fmt.Errorf("download exceeds max size %d bytes", maxCanvasAgentAttachmentBytes)
	}

	// O_NOFOLLOW: a symlink at destPath (planted by an earlier
	// concurrent agent run, or an attacker on a shared box) would
	// otherwise let os.Create redirect this download to wherever
	// the link points. Fail-closed instead.
	file, err := os.OpenFile(destPath, attachmentWriteFlags, 0o644)
	if err != nil {
		return 0, fmt.Errorf("create file failed: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(resp.Body, maxCanvasAgentAttachmentBytes+1)
	n, err := io.Copy(file, limited)
	if err != nil {
		_ = os.Remove(destPath)
		return 0, fmt.Errorf("write file failed: %w", err)
	}
	if n > maxCanvasAgentAttachmentBytes {
		_ = os.Remove(destPath)
		return 0, fmt.Errorf("download exceeds max size %d bytes", maxCanvasAgentAttachmentBytes)
	}

	return n, nil
}

// mimeToExtension maps common MIME types to file extensions.
func mimeToExtension(mimeType string) string {
	extMap := map[string]string{
		"image/png":       ".png",
		"image/jpeg":      ".jpg",
		"image/gif":       ".gif",
		"image/webp":      ".webp",
		"image/svg+xml":   ".svg",
		"video/mp4":       ".mp4",
		"video/webm":      ".webm",
		"application/pdf": ".pdf",
	}
	if ext, ok := extMap[mimeType]; ok {
		return ext
	}
	return ".bin"
}
