package workagent

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// PathValidator provides server-side path security validation
type PathValidator struct {
	workspaceRoot string
}

// NewPathValidator creates a new path validator
func NewPathValidator(workspaceRoot string) *PathValidator {
	return &PathValidator{
		workspaceRoot: workspaceRoot,
	}
}

// ValidatePathSafe checks if a path is safe to access. Returns error
// if path is dangerous.
//
// Order matters. The original implementation ran the system-prefix
// denylist BEFORE the workspace-allow check, which meant any deploy
// where the workspace lives under a "system" prefix (e.g. the
// production layout `/var/lib/workmax/agent_workspace`, `/opt/workmax/...`,
// `/tmp/...` in dev) had every legitimate tool call blocked at
// "/var" before the allow path could fire. Now:
//
//  1. Segment-aware traversal check (rejects `..` as a path
//     component, not as a substring — file names like
//     `report..backup.pdf` no longer false-positive).
//  2. If the path is absolute AND resolves inside workspaceRoot →
//     allow. The workspace is user-owned territory; filenames
//     inside it can contain "passwd" or ".env" without triggering
//     the system-file denylist (those denylists are about reading
//     OS files, not about restricting what a user names their own
//     uploads).
//  3. Otherwise apply the system-prefix and keyword denylists.
//  4. Reject anything that's still absolute at this point — it's
//     outside the workspace and survived the denylists.
func (pv *PathValidator) ValidatePathSafe(path string) error {
	cleanPath := filepath.Clean(path)

	// Step 1: segment-aware `..` detection. ToSlash + Split so the
	// check works on both unix and windows-style separators.
	for _, seg := range strings.Split(filepath.ToSlash(cleanPath), "/") {
		if seg == ".." {
			return fmt.Errorf("SECURITY: Directory traversal detected in path: %s", path)
		}
	}

	// Step 2: workspace fast-allow.
	if filepath.IsAbs(path) && pv.workspaceRoot != "" {
		if absWorkspace, err := filepath.Abs(pv.workspaceRoot); err == nil {
			if absPath, err := filepath.Abs(path); err == nil {
				sep := string(filepath.Separator)
				if absPath == absWorkspace || strings.HasPrefix(absPath+sep, absWorkspace+sep) {
					return nil
				}
			}
		}
	}

	lowerPath := strings.ToLower(cleanPath)

	// Step 3a: system-path prefix denylist (only fires for paths
	// NOT inside the workspace, thanks to step 2's early return).
	forbiddenPrefixes := []string{
		"/etc", "/etc/",
		"/root", "/root/",
		"/sys", "/sys/",
		"/proc", "/proc/",
		"/boot", "/boot/",
		"/var", "/var/",
		"/usr", "/usr/",
		"/bin", "/bin/",
		"/sbin", "/sbin/",
		"/dev", "/dev/",
		"/tmp", "/tmp/",
		"/opt", "/opt/",
		"c:\\windows", "c:/windows",
		"c:\\program files", "c:/program files",
	}
	for _, prefix := range forbiddenPrefixes {
		if strings.HasPrefix(lowerPath, prefix) {
			return fmt.Errorf("SECURITY: Access to system path '%s' is forbidden", path)
		}
	}

	// Step 3b: keyword denylist. Mirrors step 3a's "outside
	// workspace only" scope — the workspace short-circuit above
	// means a user file named `.env` inside their workspace gets
	// through, which is the desired behaviour.
	forbiddenKeywords := []string{
		"passwd", "shadow", "sudoers", "hosts",
		".ssh", ".aws", ".config", ".env",
		"id_rsa", "authorized_keys",
		"system32", "windows",
	}
	for _, keyword := range forbiddenKeywords {
		if strings.Contains(lowerPath, keyword) {
			return fmt.Errorf("SECURITY: Path contains forbidden keyword '%s': %s", keyword, path)
		}
	}

	// Step 4: any absolute path that survived denylists is outside
	// the workspace — refuse.
	if filepath.IsAbs(path) {
		return fmt.Errorf("SECURITY: Absolute paths outside workspace are forbidden: %s", path)
	}

	return nil
}

// pathTools is the static map from tool name to argument keys that
// hold a path-or-command string. Hoisted to package scope so
// ValidateToolCall doesn't rebuild the map on every call (hot path
// during a chat turn — every tool_use event hits this).
var pathTools = map[string][]string{
	"Read":       {"path", "file_path", "filepath"},
	"Write":      {"path", "file_path", "filepath"},
	"MultiWrite": {"path", "file_path", "filepath"},
	"Edit":       {"path", "file_path", "filepath"},
	"Bash":       {"command"},
}

// ValidateToolCall validates a tool call before execution.
//
// For Read/Write/Edit/MultiWrite this is a real boundary: paths must
// resolve inside the workspace and must not contain `..` segments
// (see ValidatePathSafe). For Bash this is best-effort spot-checking
// only — see validateBashCommand's doc comment. Real Bash containment
// requires sandboxing the agent subprocess at launch time.
func (pv *PathValidator) ValidateToolCall(toolName string, args map[string]interface{}) error {
	pathKeys, ok := pathTools[toolName]
	if !ok {
		return nil // Tool doesn't involve paths, allow
	}

	// Check each possible path argument
	for _, key := range pathKeys {
		if value, exists := args[key]; exists {
			if pathStr, ok := value.(string); ok {
				// For Bash commands, extract file paths
				if toolName == "Bash" {
					if err := pv.validateBashCommand(pathStr); err != nil {
						return err
					}
				} else {
					// For file operations, validate the path directly
					if err := pv.ValidatePathSafe(pathStr); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

// validateBashCommand is a best-effort spot-check for obvious mistakes
// in agent-issued shell commands. It is NOT a complete security
// boundary; process and network sandboxing remain the real containment
// layer. The matcher checks both the raw lowercase command and a few
// normalized variants so trivial quote/escape/whitespace bypasses do
// not defeat the preflight hook.
func (pv *PathValidator) validateBashCommand(command string) error {
	lowerCmd := strings.ToLower(command)
	variants := bashCommandMatchVariants(lowerCmd)

	dangerousPaths := []string{
		"/etc/passwd", "/etc/shadow", "/etc/hosts",
		"/etc/pa", "/etc/pas",
		"/root/", "/sys/", "/proc/", "/boot/",
		".ssh/", ".aws/",
		// Cloud instance-metadata endpoints — agents that shell out
		// to curl these are almost certainly trying to exfiltrate
		// IAM creds.
		"169.254.169.254",
		"metadata.google.internal",
	}
	for _, dangerPath := range dangerousPaths {
		if anyBashVariantContains(variants, dangerPath) {
			return fmt.Errorf("SECURITY: Bash command contains forbidden path '%s'", dangerPath)
		}
	}

	dangerousOps := []string{
		"rm -rf", "rm -fr", "rm -r",
		"rm-rf", "rm-fr", "rm-r",
		"mkfs", "dd if=", "format",
		"sudo", "su -", "chmod 777",
		// Pipe-to-shell exfil/install pattern. Catches the lazy
		// version; quoting around `sh`/`bash` defeats it.
		"| sh", "|sh ", "| bash", "|bash ",
		"curl -s | sh", "wget -qo- | sh",
	}
	for _, op := range dangerousOps {
		if anyBashVariantContains(variants, op) {
			return fmt.Errorf("SECURITY: Bash command contains dangerous operation '%s'", op)
		}
	}

	return nil
}

func bashCommandMatchVariants(lowerCmd string) []string {
	decoded := decodeShellHexEscapes(lowerCmd)
	dequoted := stripShellJoinNoise(decoded)
	compact := compactShellWhitespace(dequoted)
	return []string{lowerCmd, decoded, dequoted, compact}
}

func anyBashVariantContains(variants []string, needle string) bool {
	compactNeedle := compactShellWhitespace(stripShellJoinNoise(needle))
	for _, variant := range variants {
		if strings.Contains(variant, needle) || (compactNeedle != "" && strings.Contains(variant, compactNeedle)) {
			return true
		}
	}
	return false
}

func decodeShellHexEscapes(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if i+3 < len(value) && value[i] == '\\' && (value[i+1] == 'x' || value[i+1] == 'X') {
			if decoded, err := strconv.ParseUint(value[i+2:i+4], 16, 8); err == nil {
				out.WriteByte(byte(decoded))
				i += 3
				continue
			}
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

func stripShellJoinNoise(value string) string {
	replacer := strings.NewReplacer(
		`"`, "",
		`'`, "",
		`\`, "",
		"`", "",
		"$", "",
		"(", "",
		")", "",
		"{", "",
		"}", "",
		";", "",
	)
	return replacer.Replace(value)
}

func compactShellWhitespace(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
