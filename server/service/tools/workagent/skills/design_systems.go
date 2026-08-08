package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"
)

// DesignSystem holds the parsed metadata + raw markdown body of one
// design-system file. Sprint-B DS-1 ships 8 brand-neutral systems
// in skills/_shared/design-systems/<basename>.md; the loader reads
// them via the skillsFS embed.FS so deployment layout doesn't matter.
//
// The body is intentionally NOT structurally parsed — the model
// reads it whole when it lands in SystemAdditionsComposer.DesignSystem.
// We just expose the basename + size + a derived-from anchor so the
// dispatcher can resolve direction → design-system without re-parsing.
//
// See visual-directions.yaml's `ds_link` field for direction → ds
// resolution. Each DS-3 visual direction's ds_link points to one of
// these files.
type DesignSystem struct {
	// Basename is the filename without extension, e.g.
	// "modern-minimal". Used as the lookup key.
	Basename string

	// Body is the raw markdown — what the model reads.
	Body string

	// DerivedFrom is parsed from the `derived_from: <id>` line in
	// the file's first 5 lines. Empty when the file doesn't
	// declare one (e.g. brand-neutral starters that aren't tied
	// to a specific direction).
	DerivedFrom string
}

// DesignSystemCatalogItem is the service-layer row used by API
// surfaces and future project-asset pickers. Body is included so a
// picker can preview or inject the selected system without a second
// lookup; callers that only need names can still use
// AvailableDesignSystems.
type DesignSystemCatalogItem struct {
	Basename       string     `json:"basename"`
	Title          string     `json:"title"`
	DerivedFrom    string     `json:"derivedFrom"`
	Body           string     `json:"body"`
	Source         string     `json:"source,omitempty"`
	ProjectID      uint       `json:"projectId,omitempty"`
	DesignSystemID uint       `json:"designSystemId,omitempty"`
	ThreadID       uint       `json:"threadId,omitempty"`
	ArtifactID     uint       `json:"artifactId,omitempty"`
	CandidateID    uint       `json:"candidateId,omitempty"`
	Status         string     `json:"status,omitempty"`
	Version        string     `json:"version,omitempty"`
	ReadOnly       bool       `json:"readOnly,omitempty"`
	Permissions    []string   `json:"permissions,omitempty"`
	ReviewedBy     int        `json:"reviewedBy,omitempty"`
	ReviewedAt     *time.Time `json:"reviewedAt,omitempty"`
	ReviewNote     string     `json:"reviewNote,omitempty"`
}

// LoadDesignSystem returns the design-system markdown for the
// supplied basename (e.g. "modern-minimal"). Returns nil when the
// file doesn't exist — caller treats as "no system selected".
//
// Empty basename returns nil. Embed.FS read errors propagate so a
// deployment problem fails fast rather than silently returning an
// empty system.
func LoadDesignSystem(basename string) (*DesignSystem, error) {
	if basename == "" {
		return nil, nil
	}
	if cached := getCachedDesignSystem(basename); cached != nil {
		return cached, nil
	}

	path := "_shared/design-systems/" + basename + ".md"
	data, err := skillsFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	body := string(data)
	ds := &DesignSystem{
		Basename:    basename,
		Body:        body,
		DerivedFrom: parseDerivedFrom(body),
	}
	storeCachedDesignSystem(basename, ds)
	return ds, nil
}

// LoadDesignSystemForDirection resolves a VisualDirection to its
// design-system file via the direction's DsLink. Convenience for
// the dispatcher — when a direction is selected and a design-system
// is requested, this single call returns the right markdown.
//
// Returns (nil, nil) for nil direction or empty DsLink.
func LoadDesignSystemForDirection(d *VisualDirection) (*DesignSystem, error) {
	if d == nil || d.DsLink == "" {
		return nil, nil
	}
	basename := strings.TrimSuffix(d.DsLink, ".md")
	return LoadDesignSystem(basename)
}

// AvailableDesignSystems returns the basenames of every
// design-system markdown shipped in the embed FS, sorted. Used by
// the picker UI / debug endpoints.
func AvailableDesignSystems() []string {
	entries, err := fs.ReadDir(skillsFS, "_shared/design-systems")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".md"))
	}
	sort.Strings(out)
	return out
}

// ListDesignSystemCatalog loads every shipped design system as stable
// catalog rows. The order is by basename, matching
// AvailableDesignSystems, so API responses are deterministic.
func ListDesignSystemCatalog() ([]DesignSystemCatalogItem, error) {
	names := AvailableDesignSystems()
	items := make([]DesignSystemCatalogItem, 0, len(names))
	for _, name := range names {
		ds, err := LoadDesignSystem(name)
		if err != nil {
			return nil, err
		}
		if ds == nil {
			continue
		}
		items = append(items, DesignSystemCatalogItem{
			Basename:    ds.Basename,
			Title:       DesignSystemTitle(ds),
			DerivedFrom: ds.DerivedFrom,
			Body:        ds.Body,
			Source:      "official",
			Status:      "published",
			Version:     "shipped",
			ReadOnly:    true,
			Permissions: []string{"use", "fork"},
		})
	}
	return items, nil
}

// DesignSystemTitle returns the markdown H1 for display/catalog rows,
// falling back to the basename when the file is malformed.
func DesignSystemTitle(ds *DesignSystem) string {
	if ds == nil {
		return ""
	}
	for _, line := range strings.Split(ds.Body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "# "))
	}
	return ds.Basename
}

// parseDerivedFrom scans the first ~5 lines of the markdown for a
// `derived_from: <id>` annotation. Tolerant of markdown variation
// — looks for the literal substring after a colon. Empty when not
// present.
func parseDerivedFrom(body string) string {
	lines := strings.SplitN(body, "\n", 8)
	for _, line := range lines {
		// We use the convention `derived_from: id` (or
		// "`derived_from: id`" wrapped in backticks). Trim
		// markdown punctuation around the value.
		idx := strings.Index(line, "derived_from:")
		if idx < 0 {
			continue
		}
		v := strings.TrimSpace(line[idx+len("derived_from:"):])
		// Strip trailing inline-code backticks and parenthetical
		// notes ("modern_minimal (visual-directions.yaml)").
		v = strings.TrimSuffix(v, "`")
		v = strings.TrimPrefix(v, "`")
		if paren := strings.IndexByte(v, '('); paren > 0 {
			v = strings.TrimSpace(v[:paren])
		}
		v = strings.Trim(v, "`'\" ")
		return v
	}
	return ""
}

var (
	designSystemCache   = map[string]*DesignSystem{}
	designSystemCacheMu sync.Mutex
)

func getCachedDesignSystem(basename string) *DesignSystem {
	designSystemCacheMu.Lock()
	defer designSystemCacheMu.Unlock()
	return designSystemCache[basename]
}

func storeCachedDesignSystem(basename string, ds *DesignSystem) {
	designSystemCacheMu.Lock()
	defer designSystemCacheMu.Unlock()
	designSystemCache[basename] = ds
}
