//go:build desktop

package local_agent

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Path containment for the tool loop, sunk from the cloud workagent's
// path_validator.go (sinkdown category A). Same rules, two deliberate
// desktop deviations, both documented at their sites:
//
//   - logging goes through the standard logger, not server/globals;
//   - an unparseable hook payload DENIES instead of allowing. The cloud
//     fail-open keeps a multi-tenant conversation moving; here the loop runs
//     with bypassPermissions on the user's own machine, so a validator that
//     has gone blind must stop the tool, not wave it through.
//
// The order of checks is load-bearing and inherited verbatim: traversal
// first, workspace fast-allow second (so user files named ".env" inside the
// workspace are theirs to have), denylists third, and a final refusal of any
// absolute path that survived — see the cloud file's history for the outage
// the wrong order caused.

// pathValidator validates tool-call paths against one workspace root.
type pathValidator struct {
	workspaceRoot string
}

func newPathValidator(workspaceRoot string) *pathValidator {
	return &pathValidator{workspaceRoot: workspaceRoot}
}

// validatePathSafe reports whether a path is safe for a workspace-scoped
// tool to touch.
func (pv *pathValidator) validatePathSafe(path string) error {
	cleanPath := filepath.Clean(path)

	// Segment-aware `..` detection: a component, not a substring, so a file
	// named `report..backup.pdf` is not a traversal.
	for _, seg := range strings.Split(filepath.ToSlash(cleanPath), "/") {
		if seg == ".." {
			return fmt.Errorf("directory traversal in path: %s", path)
		}
	}

	// Workspace fast-allow: inside the workspace is user-owned territory.
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
			return fmt.Errorf("system path %q is off limits", path)
		}
	}

	forbiddenKeywords := []string{
		"passwd", "shadow", "sudoers", "hosts",
		".ssh", ".aws", ".config", ".env",
		"id_rsa", "authorized_keys",
		"system32", "windows",
	}
	for _, keyword := range forbiddenKeywords {
		if strings.Contains(lowerPath, keyword) {
			return fmt.Errorf("path contains forbidden keyword %q: %s", keyword, path)
		}
	}

	// Anything still absolute is outside the workspace and merely failed to
	// match a denylist. Outside is outside.
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute path outside the workspace: %s", path)
	}

	return nil
}

// pathToolArgs maps tool names to the argument keys that carry a path or a
// command. Bash stays listed even though it is not in allowedTools yet: the
// day it is enabled, forgetting to add it here must not be possible.
var pathToolArgs = map[string][]string{
	"Read":       {"path", "file_path", "filepath"},
	"Write":      {"path", "file_path", "filepath"},
	"MultiWrite": {"path", "file_path", "filepath"},
	"Edit":       {"path", "file_path", "filepath"},
	"Bash":       {"command"},
}

// validateToolCall gates one tool invocation.
func (pv *pathValidator) validateToolCall(toolName string, args map[string]any) error {
	pathKeys, ok := pathToolArgs[toolName]
	if !ok {
		return nil
	}
	for _, key := range pathKeys {
		value, exists := args[key]
		if !exists {
			continue
		}
		pathStr, ok := value.(string)
		if !ok {
			continue
		}
		if toolName == "Bash" {
			if err := pv.validateBashCommand(pathStr); err != nil {
				return err
			}
		} else if err := pv.validatePathSafe(pathStr); err != nil {
			return err
		}
	}
	return nil
}

// validateBashCommand spot-checks agent-issued shell commands. Best-effort by
// design and inherited as such: real Bash containment is process sandboxing,
// which is why Bash is not in allowedTools yet.
func (pv *pathValidator) validateBashCommand(command string) error {
	lowerCmd := strings.ToLower(command)
	variants := bashCommandMatchVariants(lowerCmd)

	dangerousPaths := []string{
		"/etc/passwd", "/etc/shadow", "/etc/hosts",
		"/etc/pa", "/etc/pas",
		"/root/", "/sys/", "/proc/", "/boot/",
		".ssh/", ".aws/",
		"169.254.169.254",
		"metadata.google.internal",
	}
	for _, dangerPath := range dangerousPaths {
		if anyBashVariantContains(variants, dangerPath) {
			return fmt.Errorf("bash command touches forbidden path %q", dangerPath)
		}
	}

	dangerousOps := []string{
		"rm -rf", "rm -fr", "rm -r",
		"rm-rf", "rm-fr", "rm-r",
		"mkfs", "dd if=", "format",
		"sudo", "su -", "chmod 777",
		"| sh", "|sh ", "| bash", "|bash ",
		"curl -s | sh", "wget -qo- | sh",
	}
	for _, op := range dangerousOps {
		if anyBashVariantContains(variants, op) {
			return fmt.Errorf("bash command contains dangerous operation %q", op)
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
