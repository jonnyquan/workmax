package workagent

import (
	"path"
	"strconv"
	"strings"
)

type ArtifactHTMLAssetBundleManifest struct {
	Entries []ArtifactHTMLAssetBundleEntry `json:"entries"`
	Blocked bool                           `json:"blocked"`
}

type ArtifactHTMLAssetBundleEntry struct {
	SourceURL  string `json:"sourceUrl"`
	BundlePath string `json:"bundlePath,omitempty"`
	Source     string `json:"source,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

func BuildHTMLAssetBundleManifest(view ArtifactView) *ArtifactHTMLAssetBundleManifest {
	if view.OutputType != "html" && view.PreviewType != "html" {
		return nil
	}
	refs := []ArtifactHTMLAssetReference{}
	if view.HTMLValidation != nil {
		refs = view.HTMLValidation.AssetReferences
	}
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: make([]ArtifactHTMLAssetBundleEntry, 0, len(refs)),
	}
	usedPaths := map[string]int{}
	for _, ref := range refs {
		entry := buildHTMLAssetBundleEntry(ref, usedPaths)
		if entry.SourceURL == "" {
			continue
		}
		if entry.Status == "blocked" {
			manifest.Blocked = true
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	return manifest
}

func buildHTMLAssetBundleEntry(ref ArtifactHTMLAssetReference, usedPaths map[string]int) ArtifactHTMLAssetBundleEntry {
	raw := strings.TrimSpace(ref.URL)
	if raw == "" || ref.Kind != "local" || ref.Action != "bundle" {
		return ArtifactHTMLAssetBundleEntry{}
	}
	clean := stripHTMLAssetURLDecorators(raw)
	if hasPathTraversalSegment(clean) {
		return ArtifactHTMLAssetBundleEntry{
			SourceURL: raw,
			Source:    ref.Source,
			Status:    "blocked",
			Reason:    "path_traversal",
		}
	}
	name := path.Base(strings.TrimPrefix(clean, "/"))
	if name == "." || name == "/" || name == "" {
		return ArtifactHTMLAssetBundleEntry{
			SourceURL: raw,
			Source:    ref.Source,
			Status:    "blocked",
			Reason:    "missing_asset_filename",
		}
	}
	bundlePath := uniqueHTMLAssetBundlePath("assets/"+name, usedPaths)
	return ArtifactHTMLAssetBundleEntry{
		SourceURL:  raw,
		BundlePath: bundlePath,
		Source:     ref.Source,
		Status:     "pending",
	}
}

func stripHTMLAssetURLDecorators(raw string) string {
	clean := strings.TrimSpace(raw)
	if idx := strings.IndexAny(clean, "?#"); idx >= 0 {
		clean = clean[:idx]
	}
	return strings.TrimSpace(clean)
}

func hasPathTraversalSegment(raw string) bool {
	for _, segment := range strings.Split(strings.ReplaceAll(raw, "\\", "/"), "/") {
		if strings.TrimSpace(segment) == ".." {
			return true
		}
	}
	return false
}

func uniqueHTMLAssetBundlePath(candidate string, used map[string]int) string {
	if used == nil {
		return candidate
	}
	if used[candidate] == 0 {
		used[candidate] = 1
		return candidate
	}
	used[candidate]++
	ext := path.Ext(candidate)
	base := strings.TrimSuffix(candidate, ext)
	return base + "-" + strconv.Itoa(used[candidate]) + ext
}
