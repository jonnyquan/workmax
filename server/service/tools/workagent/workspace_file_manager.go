package workagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"server/globals"
)

// syscallNOFOLLOW aliases syscall.O_NOFOLLOW so the copyFile open call
// reads as a single O_* expression. The package already depends on
// unix-only flags (skill_validator.go uses O_NOFOLLOW directly), so
// re-using the constant here doesn't change the platform matrix.
const syscallNOFOLLOW = syscall.O_NOFOLLOW

// PrepareFilesForAgent prepares files in the agent workspace
// It creates symlinks or copies files from the uploads directory to the thread workspace
func PrepareFilesForAgent(files []AgentFileInfo, threadWorkspace string) ([]string, error) {
	if len(files) == 0 {
		return []string{}, nil
	}

	uploadsDir := filepath.Join(threadWorkspace, "uploads")
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create uploads directory: %w", err)
	}

	// Resolve workspaceRoot once. Source paths must EvalSymlinks to a
	// location inside this root — without that check, the agent's own
	// Write tool could plant a symlink at e.g. <thread>/sneaky →
	// /etc/passwd inside its own legit workspace, then we'd unwittingly
	// re-link that target into uploads/ on the next turn so the SDK
	// reads /etc/passwd. ResolveInsideWorkspace upstream only validates
	// the path string, not the symlink chain, so we have to follow it
	// here before trusting the source.
	//
	// We EvalSymlinks the root too — on macOS /var is itself a symlink
	// to /private/var, so a root configured as /var/... and a source
	// resolving to /private/var/... would prefix-mismatch otherwise.
	manager := GetAgentClientManager()
	absWorkspaceRoot := ""
	if manager != nil {
		if abs, err := filepath.Abs(filepath.Clean(manager.WorkspaceRoot())); err == nil {
			if eval, err := filepath.EvalSymlinks(abs); err == nil {
				absWorkspaceRoot = eval
			} else {
				absWorkspaceRoot = abs
			}
		}
	}

	preparedFiles := make([]string, 0, len(files))

	for _, file := range files {
		// Validate source file path
		sourcePath := strings.TrimSpace(file.Path)
		if sourcePath == "" {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Skipping file %s: no path provided", file.Name))
			continue
		}

		// Clean and resolve absolute paths for comparison
		cleanSourcePath := filepath.Clean(sourcePath)
		absSourcePath, err := filepath.Abs(cleanSourcePath)
		if err != nil {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Failed to resolve absolute path for %s: %v", sourcePath, err))
			absSourcePath = cleanSourcePath
		}

		// Determine target filename in uploads directory. file.Name is
		// user-controlled HTTP input, so a crafted request can put "../../"
		// segments or an absolute prefix in here. If we pass it straight into
		// filepath.Join, the traversal escapes uploadsDir and the os.Remove
		// below can delete arbitrary server files, while the subsequent
		// Symlink/copyFile can write anywhere the process has access to.
		// Strip to the base name and reject anything that still resolves outside.
		rawName := strings.TrimSpace(file.Name)
		if rawName == "" {
			rawName = filepath.Base(sourcePath)
		}
		fileName := filepath.Base(filepath.Clean(rawName))
		if fileName == "" || fileName == "." || fileName == ".." ||
			strings.ContainsAny(fileName, `/\`) {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Skipping file with unsafe name: %q", file.Name))
			continue
		}

		absUploadsDir, err := filepath.Abs(uploadsDir)
		if err != nil {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Failed to resolve uploads dir: %v", err))
			continue
		}
		targetPath := filepath.Join(absUploadsDir, fileName)
		absTargetPath, err := filepath.Abs(targetPath)
		if err != nil {
			absTargetPath = targetPath
		}
		// Belt-and-suspenders: after cleaning, the target must still be a
		// direct child of uploadsDir. If the name somehow survived sanitisation
		// and escaped anyway, refuse to proceed.
		sep := string(filepath.Separator)
		if !strings.HasPrefix(absTargetPath+sep, absUploadsDir+sep) {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Skipping file %s: resolved target %q escapes uploads dir", file.Name, absTargetPath))
			continue
		}

		// OPTIMIZATION: Check if source file is already in the target uploads directory
		// This handles files that were uploaded directly to the correct location
		if absSourcePath == absTargetPath {
			globals.Info(fmt.Sprintf("[WorkspaceFileManager] File already in target location, skipping: %s", fileName))
			preparedFiles = append(preparedFiles, fileName)
			continue
		}

		// Check if source file exists AND its symlink chain stays inside
		// workspaceRoot. EvalSymlinks both confirms existence (returns
		// an error for missing files) and resolves every link in the
		// chain — so we can verify the *final* on-disk target is still
		// inside the workspace before trusting it. Plain os.Stat would
		// follow links silently and let a planted symlink escape.
		evaluated, err := filepath.EvalSymlinks(absSourcePath)
		if err != nil {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Source file not accessible: %s (%v)", absSourcePath, err))
			continue
		}
		absEvaluated, err := filepath.Abs(evaluated)
		if err != nil {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Failed to abs evaluated path %q: %v", evaluated, err))
			continue
		}
		if absWorkspaceRoot == "" {
			// Without a known root we can't enforce the boundary. Refuse
			// rather than silently allowing — a misconfigured root is
			// less bad than a silent escape.
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] No workspace root configured; refusing to link %s", absSourcePath))
			continue
		}
		if !strings.HasPrefix(absEvaluated+sep, absWorkspaceRoot+sep) && absEvaluated != absWorkspaceRoot {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Refusing to link %s: symlink chain escapes workspace root (resolves to %s)", file.Name, absEvaluated))
			continue
		}
		// Use the evaluated path for subsequent operations so the
		// symlink we create points at the real underlying file, not at
		// another link. (If the source was a regular file, evaluated ==
		// absSourcePath and this is a no-op.)
		absSourcePath = absEvaluated

		// If target already exists and is the same file, skip
		if targetInfo, err := os.Lstat(targetPath); err == nil {
			// Check if it's a symlink pointing to the same source
			if targetInfo.Mode()&os.ModeSymlink != 0 {
				if linkTarget, err := os.Readlink(targetPath); err == nil {
					// Resolve symlink target to absolute path for comparison
					absLinkTarget, _ := filepath.Abs(linkTarget)
					if absLinkTarget == absSourcePath {
						globals.Info(fmt.Sprintf("[WorkspaceFileManager] File already linked: %s", fileName))
						preparedFiles = append(preparedFiles, fileName)
						continue
					}
				}
			}
			// Remove existing file/link before recreating. If the
			// remove fails (race, permissions, weird FS state) we
			// MUST NOT fall through to Symlink+copy: a leftover
			// symlink at targetPath would survive the Symlink call
			// (EEXIST) and the copyFile fallback's os.Create would
			// then follow that symlink and write through it. Refuse
			// to proceed for this file instead.
			if err := os.Remove(targetPath); err != nil {
				globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Skipping %s: failed to clear existing target: %v", fileName, err))
				continue
			}
		}

		// Try to create symlink first (more efficient)
		if err := os.Symlink(absSourcePath, targetPath); err != nil {
			globals.Warn(fmt.Sprintf("[WorkspaceFileManager] Failed to create symlink for %s, copying instead: %v", fileName, err))

			// Fallback: copy file. copyFile uses O_CREATE|O_EXCL|
			// O_NOFOLLOW so a symlink that somehow got planted at
			// targetPath since the Lstat above can't redirect the
			// write — we'd error out instead of writing through.
			if err := copyFile(absSourcePath, targetPath); err != nil {
				globals.Error(fmt.Sprintf("[WorkspaceFileManager] Failed to copy file %s: %v", fileName, err))
				continue
			}

			globals.Info(fmt.Sprintf("[WorkspaceFileManager] Copied file to workspace: %s", fileName))
		} else {
			globals.Info(fmt.Sprintf("[WorkspaceFileManager] Linked file to workspace: %s", fileName))
		}

		preparedFiles = append(preparedFiles, fileName)
	}

	globals.Info(fmt.Sprintf("[WorkspaceFileManager] Prepared %d files in workspace: %v", len(preparedFiles), preparedFiles))
	return preparedFiles, nil
}

// AgentFileInfo represents file information for agent workspace preparation.
//
// Hash and ModTime are optional cache-busting hints. The filesContext
// cache fingerprints the slice to skip rebuilding the system prompt
// when the file set is unchanged. Without either of these fields a
// re-uploaded file with the same ID/Name/Size/Path looks identical and
// the cache happily serves a stale prompt that points the agent at the
// old contents. Populate Hash when the source row carries a content
// hash (DB-backed thread files), otherwise populate ModTime from the
// resolved file's stat.
type AgentFileInfo struct {
	ID      string
	Name    string
	Path    string // Absolute or relative path to the source file
	Size    int64
	Type    string
	Hash    string // Content hash; takes precedence over ModTime in fingerprint.
	ModTime int64  // File mtime in unix nanoseconds; used when Hash is empty.
}

// copyFile copies a file from src to dst.
//
// Destination open uses O_CREATE|O_EXCL|O_NOFOLLOW so we never
// follow a pre-existing symlink at dst — without that flag, a
// leftover symlink (e.g. one the previous turn's Write tool placed
// pointing at a system file) would silently redirect our write to
// wherever the link points. EEXIST/ELOOP from this open is the
// signal "refuse and let the caller skip this file".
//
// On windows the syscall.O_NOFOLLOW symbol isn't defined; the
// !windows build is the production target so we don't add a build
// tag here. If that ever changes, copy_file_windows.go can stub
// the flag with 0 and accept the residual followthrough risk.
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	sourceInfo, err := sourceFile.Stat()
	if err != nil {
		return err
	}

	destFile, err := os.OpenFile(
		dst,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscallNOFOLLOW,
		sourceInfo.Mode().Perm(),
	)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := destFile.ReadFrom(sourceFile); err != nil {
		return err
	}

	return nil
}
