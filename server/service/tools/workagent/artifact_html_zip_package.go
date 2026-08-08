package workagent

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type HTMLAssetLoader func(sourceURL string) ([]byte, error)

type HTMLZipPackageError struct {
	Code      string
	SourceURL string
	Err       error
}

func (e *HTMLZipPackageError) Error() string {
	if e == nil {
		return ""
	}
	if e.SourceURL != "" && e.Err != nil {
		return fmt.Sprintf("build html zip: %s %s: %v", e.Code, e.SourceURL, e.Err)
	}
	if e.SourceURL != "" {
		return fmt.Sprintf("build html zip: %s %s", e.Code, e.SourceURL)
	}
	if e.Err != nil {
		return fmt.Sprintf("build html zip: %s: %v", e.Code, e.Err)
	}
	return "build html zip: " + e.Code
}

func (e *HTMLZipPackageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func BuildHTMLZipPackage(content string, manifest *ArtifactHTMLAssetBundleManifest, loadAsset HTMLAssetLoader) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("build html zip: missing asset bundle manifest")
	}
	if manifest.Blocked {
		return nil, fmt.Errorf("build html zip: asset bundle manifest is blocked")
	}
	if loadAsset == nil {
		return nil, fmt.Errorf("build html zip: nil asset loader")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	html := content
	usedBundlePaths := map[string]bool{}
	for _, entry := range manifest.Entries {
		if entry.Status == "blocked" {
			_ = zw.Close()
			return nil, fmt.Errorf("build html zip: blocked asset %s: %s", entry.SourceURL, entry.Reason)
		}
		if entry.SourceURL == "" || entry.BundlePath == "" {
			continue
		}
		if !isSafeHTMLZipEntryPath(entry.BundlePath) {
			_ = zw.Close()
			return nil, fmt.Errorf("build html zip: unsafe bundle path %s", entry.BundlePath)
		}
		if usedBundlePaths[entry.BundlePath] {
			_ = zw.Close()
			return nil, fmt.Errorf("build html zip: duplicate bundle path %s", entry.BundlePath)
		}
		usedBundlePaths[entry.BundlePath] = true
		raw, err := loadAsset(entry.SourceURL)
		if err != nil {
			_ = zw.Close()
			if errors.Is(err, os.ErrNotExist) {
				return nil, &HTMLZipPackageError{Code: "missing_local_asset", SourceURL: entry.SourceURL, Err: err}
			}
			return nil, fmt.Errorf("build html zip: load asset %s: %w", entry.SourceURL, err)
		}
		w, err := zw.Create(entry.BundlePath)
		if err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("build html zip: create asset %s: %w", entry.BundlePath, err)
		}
		if strings.EqualFold(path.Ext(entry.BundlePath), ".css") {
			raw = []byte(rewriteBundledCSSAssetURLs(string(raw), entry.BundlePath, manifest.Entries))
		}
		if _, err := w.Write(raw); err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("build html zip: write asset %s: %w", entry.BundlePath, err)
		}
		html = rewriteHTMLAssetURL(html, entry.SourceURL, entry.BundlePath)
	}
	w, err := zw.Create("index.html")
	if err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("build html zip: create index: %w", err)
	}
	if _, err := w.Write([]byte(html)); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("build html zip: write index: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("build html zip: close: %w", err)
	}
	return buf.Bytes(), nil
}

func BuildHTMLZipPackageForFile(content string, htmlFilePath string, manifest *ArtifactHTMLAssetBundleManifest) ([]byte, error) {
	root := filepath.Dir(htmlFilePath)
	expanded, resolvedPaths := expandHTMLZipManifestForCSSImports(root, manifest)
	return BuildHTMLZipPackage(content, expanded, func(sourceURL string) ([]byte, error) {
		assetPath, err := resolveHTMLAssetPathWithManifestAndResolvedPaths(root, sourceURL, expanded, resolvedPaths)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(assetPath)
	})
}

func rewriteHTMLAssetURL(content string, sourceURL string, bundlePath string) string {
	if sourceURL == "" || bundlePath == "" {
		return content
	}
	return strings.ReplaceAll(content, sourceURL, bundlePath)
}

func rewriteBundledCSSAssetURLs(content string, cssBundlePath string, entries []ArtifactHTMLAssetBundleEntry) string {
	out := content
	for _, entry := range entries {
		if entry.SourceURL == "" || entry.BundlePath == "" || entry.BundlePath == cssBundlePath || entry.Status == "blocked" {
			continue
		}
		rel := relativeHTMLZipAssetPath(cssBundlePath, entry.BundlePath)
		out = rewriteDecoratedCSSAssetURLAliases(out, entry.SourceURL, rel)
		out = rewriteHTMLAssetURL(out, entry.SourceURL, rel)
	}
	return out
}

func rewriteDecoratedCSSAssetURLAliases(content string, sourceURL string, bundlePath string) string {
	clean := stripHTMLAssetURLDecorators(sourceURL)
	if clean == "" || isRemoteAssetURL(clean) || !isLocalAssetURL(clean) {
		return content
	}
	pattern := regexp.MustCompile(regexp.QuoteMeta(clean) + `(?:[?#][^"'\)\s,;]*)?`)
	return pattern.ReplaceAllString(content, bundlePath)
}

func relativeHTMLZipAssetPath(fromFile string, toFile string) string {
	fromDir := path.Clean(path.Dir(strings.TrimSpace(fromFile)))
	to := path.Clean(strings.TrimSpace(toFile))
	if fromDir == "." || fromDir == "/" || fromDir == "" {
		return to
	}
	fromParts := strings.Split(fromDir, "/")
	toParts := strings.Split(to, "/")
	for len(fromParts) > 0 && len(toParts) > 0 && fromParts[0] == toParts[0] {
		fromParts = fromParts[1:]
		toParts = toParts[1:]
	}
	parts := make([]string, 0, len(fromParts)+len(toParts))
	for range fromParts {
		parts = append(parts, "..")
	}
	parts = append(parts, toParts...)
	if len(parts) == 0 {
		return path.Base(to)
	}
	return strings.Join(parts, "/")
}

func isSafeHTMLZipEntryPath(raw string) bool {
	clean := strings.TrimSpace(raw)
	if clean == "" || strings.HasPrefix(clean, "/") || strings.Contains(clean, "\\") {
		return false
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func resolveHTMLAssetPath(root string, sourceURL string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("empty html asset root")
	}
	clean := stripHTMLAssetURLDecorators(sourceURL)
	if clean == "" || isRemoteAssetURL(clean) || !isLocalAssetURL(clean) {
		return "", fmt.Errorf("unsupported html asset url: %s", sourceURL)
	}
	clean = strings.TrimLeft(strings.ReplaceAll(clean, "\\", "/"), "/")
	candidate := filepath.Clean(filepath.Join(root, filepath.FromSlash(clean)))
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve html asset path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("html asset escapes artifact directory: %s", sourceURL)
	}
	return candidate, nil
}

func resolveHTMLAssetPathWithManifest(root string, sourceURL string, manifest *ArtifactHTMLAssetBundleManifest) (string, error) {
	return resolveHTMLAssetPathWithManifestAndResolvedPaths(root, sourceURL, manifest, nil)
}

func resolveHTMLAssetPathWithManifestAndResolvedPaths(root string, sourceURL string, manifest *ArtifactHTMLAssetBundleManifest, resolvedPaths map[string]string) (string, error) {
	if resolvedPaths != nil {
		if resolvedPath := strings.TrimSpace(resolvedPaths[sourceURL]); resolvedPath != "" {
			return resolvedPath, nil
		}
	}
	assetPath, err := resolveHTMLAssetPath(root, sourceURL)
	if err == nil {
		return assetPath, nil
	}
	if manifest == nil {
		return "", err
	}
	entry := findHTMLAssetBundleEntry(manifest, sourceURL)
	if entry == nil || !isCSSDerivedHTMLAssetSource(entry.Source) {
		return "", err
	}
	for _, cssEntry := range manifest.Entries {
		if !strings.EqualFold(path.Ext(cssEntry.BundlePath), ".css") || cssEntry.SourceURL == "" {
			continue
		}
		cssPath, cssErr := resolveHTMLAssetPath(root, cssEntry.SourceURL)
		if cssErr != nil {
			continue
		}
		candidate, cssErr := resolveHTMLAssetPathFromBase(root, filepath.Dir(cssPath), sourceURL)
		if cssErr == nil {
			return candidate, nil
		}
	}
	return "", err
}

var (
	htmlZipCSSImportPattern = regexp.MustCompile(`(?is)@import\s+(?:url\()?\s*["']?([^"');\s]+)`)
	htmlZipCSSURLPattern    = regexp.MustCompile(`(?is)url\(\s*["']?([^"')]+)`)
)

func expandHTMLZipManifestForCSSImports(root string, manifest *ArtifactHTMLAssetBundleManifest) (*ArtifactHTMLAssetBundleManifest, map[string]string) {
	if manifest == nil {
		return manifest, nil
	}
	expanded := &ArtifactHTMLAssetBundleManifest{
		Blocked: manifest.Blocked,
		Entries: append([]ArtifactHTMLAssetBundleEntry(nil), manifest.Entries...),
	}
	resolvedPaths := map[string]string{}
	usedBundlePaths := map[string]int{}
	seenEntries := map[string]bool{}
	seenResolved := map[string]bool{}
	for _, entry := range expanded.Entries {
		if entry.SourceURL != "" {
			seenEntries[entry.Source+"|"+entry.SourceURL] = true
		}
		if entry.BundlePath != "" {
			usedBundlePaths[entry.BundlePath] = 1
		}
		if strings.EqualFold(path.Ext(entry.BundlePath), ".css") && entry.SourceURL != "" && entry.Status != "blocked" {
			if cssPath, err := resolveHTMLAssetPath(root, entry.SourceURL); err == nil {
				if abs, absErr := filepath.Abs(cssPath); absErr == nil {
					seenResolved[abs] = true
				}
			}
		}
	}
	for i := 0; i < len(expanded.Entries); i++ {
		entry := expanded.Entries[i]
		if entry.SourceURL == "" || entry.Status == "blocked" || !strings.EqualFold(path.Ext(entry.BundlePath), ".css") {
			continue
		}
		cssPath, err := resolveHTMLAssetPathWithManifestAndResolvedPaths(root, entry.SourceURL, expanded, resolvedPaths)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(cssPath)
		if err != nil {
			continue
		}
		cssDir := filepath.Dir(cssPath)
		for _, discovered := range discoverHTMLZipCSSDependencies(string(raw)) {
			if discovered.url == "" || isRemoteAssetURL(discovered.url) || !isLocalAssetURL(discovered.url) {
				continue
			}
			depPath, err := resolveHTMLAssetPathFromBase(root, cssDir, discovered.url)
			if err != nil {
				continue
			}
			abs, err := filepath.Abs(depPath)
			if err != nil {
				continue
			}
			if seenResolved[abs] {
				continue
			}
			entryKey := discovered.source + "|" + discovered.url
			if seenEntries[entryKey] {
				continue
			}
			seenResolved[abs] = true
			bundlePath := uniqueHTMLAssetBundlePath("assets/"+path.Base(strings.TrimPrefix(stripHTMLAssetURLDecorators(discovered.url), "/")), usedBundlePaths)
			newEntry := ArtifactHTMLAssetBundleEntry{
				SourceURL:  discovered.url,
				BundlePath: bundlePath,
				Source:     discovered.source,
				Status:     "pending",
			}
			expanded.Entries = append(expanded.Entries, newEntry)
			seenEntries[entryKey] = true
			resolvedPaths[discovered.url] = depPath
		}
	}
	return expanded, resolvedPaths
}

type htmlZipCSSDependency struct {
	url    string
	source string
}

func discoverHTMLZipCSSDependencies(content string) []htmlZipCSSDependency {
	out := make([]htmlZipCSSDependency, 0, 2)
	for _, match := range htmlZipCSSImportPattern.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			out = append(out, htmlZipCSSDependency{url: strings.TrimSpace(match[1]), source: "css_import"})
		}
	}
	for _, match := range htmlZipCSSURLPattern.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			out = append(out, htmlZipCSSDependency{url: strings.TrimSpace(match[1]), source: "css_url"})
		}
	}
	for _, url := range cssImageSetURLs(content) {
		out = append(out, htmlZipCSSDependency{url: url, source: "css_image_set"})
	}
	return out
}

func findHTMLAssetBundleEntry(manifest *ArtifactHTMLAssetBundleManifest, sourceURL string) *ArtifactHTMLAssetBundleEntry {
	if manifest == nil {
		return nil
	}
	for i := range manifest.Entries {
		if manifest.Entries[i].SourceURL == sourceURL {
			return &manifest.Entries[i]
		}
	}
	return nil
}

func isCSSDerivedHTMLAssetSource(source string) bool {
	return source == "css_url" || source == "css_import" || source == "css_image_set"
}

func resolveHTMLAssetPathFromBase(root string, baseDir string, sourceURL string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("empty html asset root")
	}
	clean := stripHTMLAssetURLDecorators(sourceURL)
	if clean == "" || isRemoteAssetURL(clean) || !isLocalAssetURL(clean) {
		return "", fmt.Errorf("unsupported html asset url: %s", sourceURL)
	}
	if strings.HasPrefix(clean, "/") {
		return resolveHTMLAssetPath(root, sourceURL)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve html asset root: %w", err)
	}
	candidate := filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(strings.ReplaceAll(clean, "\\", "/"))))
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve html asset path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("html asset escapes artifact directory: %s", sourceURL)
	}
	return candidate, nil
}
