package skills

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

const maxSideFileBytes = 24 * 1024

// LoadSideFiles reads every regular file under <skillName>/assets/
// from the embedded skill tree. Returns an empty slice when the
// directory doesn't exist, so a skill that hasn't authored an
// assets/ tree degrades to "no preflight content" rather than an
// error.
//
// Used by the gate / SystemAdditions composer (server/service/tools/
// workagent/checklist_gate.go neighbours) to populate the
// <skill-side-files> XML block in the system prompt.
//
// Caching: results are memoized per skill name; embed.FS is immutable
// for the process lifetime, so a single read per skill suffices.
func LoadSideFiles(skillName string) []SideFile {
	if skillName == "" {
		return nil
	}
	if cached, ok := getCachedSideFiles(skillName); ok {
		return cached
	}
	files := loadSideFilesUncached(skillName)
	storeCachedSideFiles(skillName, files)
	return files
}

func loadSideFilesUncached(skillName string) []SideFile {
	dir := skillName + "/assets"
	entries, err := fs.ReadDir(skillsFS, dir)
	if err != nil {
		return []SideFile{} // missing dir — no assets, not an error
	}

	var out []SideFile
	for _, entry := range entries {
		if entry.IsDir() {
			// Nested directories under assets/ are reserved for
			// future use (e.g. assets/sample-pages/). The current
			// shape ships only top-level files; recursive walking
			// can land in a follow-up.
			continue
		}
		name := entry.Name()
		// Skip dotfiles and any files we don't want in the prompt
		// (.DS_Store on macOS dev machines, .gitkeep, etc.).
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Only include text-like assets. Binary assets (sample
		// images / sample MP4) are useful as files the agent can
		// reference by path but shouldn't be inlined into the
		// prompt. The gate's composer will treat them as URL
		// references when we wire that path; for now we filter
		// to .md / .txt / .yaml / .json.
		if !isTextAsset(name) {
			continue
		}

		full := path.Join(dir, name)
		data, err := skillsFS.ReadFile(full)
		if err != nil {
			continue
		}
		if err := validateSideFileForAuthoring(name, data); err != nil {
			continue
		}
		out = append(out, SideFile{
			Path:     name,
			Contents: string(data),
		})
	}

	// Sort for deterministic prompt assembly — the model sees the
	// same order every turn, which makes prompt-cache hits more
	// likely on the SDK side.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func validateSideFileForAuthoring(relPath string, data []byte) error {
	if err := validateSideFilePath(relPath); err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("side file %q is empty", relPath)
	}
	if len(data) > maxSideFileBytes {
		return fmt.Errorf("side file %q exceeds %d bytes", relPath, maxSideFileBytes)
	}
	return nil
}

func validateSideFilePath(relPath string) error {
	if err := validateRelPath("side file path", relPath, maxScriptPathLen); err != nil {
		return err
	}
	if strings.Contains(relPath, "/") {
		return fmt.Errorf("side file path %q must be top-level under assets/", relPath)
	}
	if strings.HasPrefix(relPath, ".") {
		return fmt.Errorf("side file path %q must not be a dotfile", relPath)
	}
	if strings.ContainsAny(relPath, "\"'<>") {
		return fmt.Errorf("side file path %q contains characters unsafe for XML wrapping", relPath)
	}
	if !isTextAsset(relPath) {
		return fmt.Errorf("side file path %q must use a text asset extension", relPath)
	}
	return nil
}

func isTextAsset(name string) bool {
	switch {
	case strings.HasSuffix(name, ".md"):
		return true
	case strings.HasSuffix(name, ".txt"):
		return true
	case strings.HasSuffix(name, ".yaml"), strings.HasSuffix(name, ".yml"):
		return true
	case strings.HasSuffix(name, ".json"):
		return true
	case strings.HasSuffix(name, ".html"):
		return true
	}
	return false
}

// FormatSideFilesXML builds the <skill-side-files> XML block ready
// to drop into SystemAdditionsComposer.SkillSideFiles. Empty slice
// → empty string (composer skips empty fields cleanly).
//
// Schema:
//
//	<skill-side-files>
//	<asset path="template-shell.md">
//	...contents...
//	</asset>
//	<asset path="poster-grid.md">
//	...
//	</asset>
//	</skill-side-files>
//
// Asset bodies are NOT escaped because they're authored markdown
// not user-supplied content; the model treats `<asset>` tags as
// structural cues, not as data the model could be tricked by. If
// future skills carry user-content assets we'll need to revisit.
func FormatSideFilesXML(files []SideFile) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<skill-side-files>\n")
	for _, f := range files {
		b.WriteString("<asset path=\"")
		b.WriteString(f.Path)
		b.WriteString("\">\n")
		b.WriteString(strings.TrimRight(f.Contents, "\n"))
		b.WriteString("\n</asset>\n")
	}
	b.WriteString("</skill-side-files>")
	return b.String()
}

// sideFilesCache memoizes per-skill assets loads. Same rationale
// as manifestCache — embed.FS bytes are immutable per process.
var (
	sideFilesCache   = map[string][]SideFile{}
	sideFilesCacheMu sync.Mutex
)

func getCachedSideFiles(skillName string) ([]SideFile, bool) {
	sideFilesCacheMu.Lock()
	defer sideFilesCacheMu.Unlock()
	v, ok := sideFilesCache[skillName]
	return v, ok
}

func storeCachedSideFiles(skillName string, files []SideFile) {
	sideFilesCacheMu.Lock()
	defer sideFilesCacheMu.Unlock()
	sideFilesCache[skillName] = files
}
