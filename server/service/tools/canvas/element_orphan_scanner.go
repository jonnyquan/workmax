// Element-orphan scanner — Stage 1 of the C-06 work. The
// CleanOrphanCanvasAssets sweep (server/service/tools/canvas_asset_cleaner.go)
// only catches `project_id IS NULL` failed-binding orphans. Assets whose
// referencing element was patched out of a still-live project keep their
// project_id intact and are never reclaimed. This file produces a
// READ-ONLY preview of those rows so ops can size the population before
// any sweep is wired.
//
// Conservatism: when a value's interpretation is ambiguous, the walker
// ADDS to the reference set rather than omitting it. False-negative on
// orphan detection ("we missed a real orphan") is strictly preferred
// to false-positive ("we marked a live asset as orphan") — the latter
// would let a future sweep tombstone something the user still sees.
//
// Channels recognised (per the wave-2 audit notes in m5-backlog C-06):
//
//   - Numeric IDs at any key matching globalAssetId / assetId /
//     resultAssetId (case-insensitive). Covers the primary
//     element.metadata.globalAssetId path written by
//     canvas_asset_service.go:1063 and the SessionAttempt /
//     ResultAssetID paths in document_v2.go.
//
//   - URL strings at any key matching url / src / thumbUrl /
//     thumbnailUrl / posterUrl (case-insensitive). Covers the legacy
//     V1Fields["src"] path that document_v2 preserves verbatim, plus
//     thumb-URL references the FE may carry inline.

package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"server/model"

	"gorm.io/gorm"
)

// AssetReferenceSet collects the asset identifiers a canvas document
// references. Sized for lookup, not iteration.
type AssetReferenceSet struct {
	GlobalAssetIDs map[uint]struct{}
	URLs           map[string]struct{}
}

func newAssetReferenceSet() *AssetReferenceSet {
	return &AssetReferenceSet{
		GlobalAssetIDs: make(map[uint]struct{}),
		URLs:           make(map[string]struct{}),
	}
}

// extractAssetReferences walks a canvas document in its persisted
// JSONMap form and returns every asset-identifying value it finds.
// Safe on nil / empty input — returns an empty set.
func extractAssetReferences(doc model.JSONMap) *AssetReferenceSet {
	refs := newAssetReferenceSet()
	if doc == nil {
		return refs
	}
	walkAssetReferences(doc, refs)
	return refs
}

func walkAssetReferences(v interface{}, refs *AssetReferenceSet) {
	// model.JSONMap is `type JSONMap map[string]interface{}` — the
	// type switch needs an explicit case because Go does not match
	// named types against their underlying-type case clauses.
	switch typed := v.(type) {
	case model.JSONMap:
		walkAssetReferencesMap(typed, refs)
	case map[string]interface{}:
		walkAssetReferencesMap(typed, refs)
	case []interface{}:
		for _, item := range typed {
			walkAssetReferences(item, refs)
		}
	case []map[string]interface{}:
		for _, item := range typed {
			walkAssetReferences(item, refs)
		}
	}
}

func walkAssetReferencesMap(m map[string]interface{}, refs *AssetReferenceSet) {
	for k, child := range m {
		switch strings.ToLower(k) {
		case "globalassetid", "assetid", "resultassetid":
			if id, ok := coerceAssetID(child); ok {
				refs.GlobalAssetIDs[id] = struct{}{}
			}
		case "url", "src", "thumburl", "thumbnailurl", "posterurl":
			if s, ok := child.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					refs.URLs[s] = struct{}{}
				}
			}
		}
		walkAssetReferences(child, refs)
	}
}

// coerceAssetID accepts the value-typed surfaces JSON unmarshal can
// produce (float64, json.Number, int, uint, string-of-int) and returns
// the numeric asset id when representable as a positive uint.
func coerceAssetID(v interface{}) (uint, bool) {
	switch n := v.(type) {
	case float64:
		if n <= 0 || n != float64(uint64(n)) {
			return 0, false
		}
		return uint(n), true
	case int:
		if n <= 0 {
			return 0, false
		}
		return uint(n), true
	case int64:
		if n <= 0 {
			return 0, false
		}
		return uint(n), true
	case uint:
		return n, n > 0
	case uint64:
		return uint(n), n > 0
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 {
			return uint(i), true
		}
	case string:
		if i, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64); err == nil && i > 0 {
			return uint(i), true
		}
	}
	return 0, false
}

// OrphanElementAssetSummary is the per-project preview output.
// Total is the full candidate count (pre-limit) so a caller can sense
// the population without paging the entire result set.
type OrphanElementAssetSummary struct {
	ProjectID uint
	Total     int
	AssetIDs  []uint
	AssetURLs []string
}

// ListOrphanElementAssetsByProject returns up to `limit` w_global_asset
// rows that are bound to projectID, not tombstoned, and not referenced
// by any element in the project's live document. READ-ONLY — does not
// mutate any row or filesystem path.
//
// Stage 1 of C-06: the function exists so ops can scale the population
// with `for p in projects: ListOrphan…(p, 0)` before any sweep is
// wired. Stage 2 (tombstone + storage GC) belongs behind an admin
// endpoint with explicit dry-run-by-default semantics.
func ListOrphanElementAssetsByProject(ctx context.Context, db *gorm.DB, projectID uint, limit int) (OrphanElementAssetSummary, error) {
	summary := OrphanElementAssetSummary{ProjectID: projectID}
	if limit <= 0 {
		limit = 100
	}

	var project model.CanvasProject
	if err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", projectID).
		First(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return summary, ErrCanvasProjectNotFound
		}
		return summary, err
	}

	refs := extractAssetReferences(project.Document)

	var assets []model.GlobalAsset
	if err := db.WithContext(ctx).
		Where("source_table = ? AND project_id = ?", "canvas_project_file", projectID).
		Where("deleted_at IS NULL AND status <> ?", model.GlobalAssetStatusDeleted).
		Order("id ASC").
		Find(&assets).Error; err != nil {
		return summary, err
	}

	for i := range assets {
		a := &assets[i]
		if _, ok := refs.GlobalAssetIDs[a.Id]; ok {
			continue
		}
		if a.URL != "" {
			if _, ok := refs.URLs[a.URL]; ok {
				continue
			}
		}
		if a.ThumbURL != "" {
			if _, ok := refs.URLs[a.ThumbURL]; ok {
				continue
			}
		}
		summary.Total++
		if len(summary.AssetIDs) < limit {
			summary.AssetIDs = append(summary.AssetIDs, a.Id)
			if a.URL != "" {
				summary.AssetURLs = append(summary.AssetURLs, a.URL)
			}
		}
	}

	return summary, nil
}

// OrphanDistributionStats describes how orphan counts are spread
// across the projects scanned. Used by the admin preview to gate
// Stage 2 (per C-06: median ≥ 10 or p99 ≥ 200 are the promote
// signals).
type OrphanDistributionStats struct {
	Min    int `json:"min"`
	Median int `json:"median"`
	P95    int `json:"p95"`
	P99    int `json:"p99"`
	Max    int `json:"max"`
}

// ProjectOrphanCount is one row of the top-K worst-offenders list.
type ProjectOrphanCount struct {
	ProjectID   uint   `json:"projectId"`
	ProjectUUID string `json:"projectUuid"`
	Title       string `json:"title"`
	OrphanCount int    `json:"orphanCount"`
}

// OrphanScanReport is the aggregated output of a global scan across
// all active canvas projects. Self-describing JSON for the admin
// endpoint.
type OrphanScanReport struct {
	ProjectsScanned int                     `json:"projectsScanned"`
	OrphansFound    int                     `json:"orphansFound"`
	Distribution    OrphanDistributionStats `json:"distribution"`
	TopProjects     []ProjectOrphanCount    `json:"topProjects"`
}

// ScanOrphanElementAssetsOpts controls the cross-project scan. Zero
// values are sensible defaults — TopK=20, PageSize=100, MaxProjects=0
// meaning "no cap".
type ScanOrphanElementAssetsOpts struct {
	TopK        int
	PageSize    int
	MaxProjects int
}

// ScanOrphanElementAssets walks every active w_canvas_project row,
// computes the orphan-element-asset count per project, and returns
// the aggregated distribution + top-K worst offenders. READ-ONLY.
//
// Memory: one project's full asset list at peak (per-project work is
// sequential; only the per-project counts + top-K heap state survive
// between iterations). Designed for ad-hoc admin invocation, not a
// hot path.
func ScanOrphanElementAssets(ctx context.Context, db *gorm.DB, opts ScanOrphanElementAssetsOpts) (OrphanScanReport, error) {
	if opts.TopK <= 0 {
		opts.TopK = 20
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 100
	}

	report := OrphanScanReport{}
	counts := make([]int, 0, 1024)
	topProjects := make([]ProjectOrphanCount, 0, opts.TopK+1)

	page := 0
	for {
		page++
		var batch []model.CanvasProject
		q := db.WithContext(ctx).
			Where("deleted_at IS NULL").
			Order("id ASC").
			Offset((page - 1) * opts.PageSize).
			Limit(opts.PageSize)
		if err := q.Find(&batch).Error; err != nil {
			return report, err
		}
		if len(batch) == 0 {
			break
		}
		for i := range batch {
			p := &batch[i]
			// Use limit=1 — we only need Total here, the per-project
			// sample is not interesting at aggregator scope.
			summary, err := ListOrphanElementAssetsByProject(ctx, db, p.Id, 1)
			if err != nil {
				return report, err
			}
			report.ProjectsScanned++
			report.OrphansFound += summary.Total
			counts = append(counts, summary.Total)
			if summary.Total == 0 {
				continue
			}
			topProjects = insertIntoTopK(topProjects, ProjectOrphanCount{
				ProjectID:   p.Id,
				ProjectUUID: p.UUID,
				Title:       p.Title,
				OrphanCount: summary.Total,
			}, opts.TopK)
			if opts.MaxProjects > 0 && report.ProjectsScanned >= opts.MaxProjects {
				break
			}
		}
		if opts.MaxProjects > 0 && report.ProjectsScanned >= opts.MaxProjects {
			break
		}
		if len(batch) < opts.PageSize {
			break
		}
	}

	report.Distribution = computeDistribution(counts)
	report.TopProjects = topProjects
	return report, nil
}

// insertIntoTopK keeps the slice sorted descending by OrphanCount and
// trims to k. Linear cost per insert; cheap for the small k expected
// here (default 20).
func insertIntoTopK(top []ProjectOrphanCount, candidate ProjectOrphanCount, k int) []ProjectOrphanCount {
	idx := len(top)
	for i, existing := range top {
		if candidate.OrphanCount > existing.OrphanCount {
			idx = i
			break
		}
	}
	if idx >= k {
		return top
	}
	top = append(top, ProjectOrphanCount{})
	copy(top[idx+1:], top[idx:])
	top[idx] = candidate
	if len(top) > k {
		top = top[:k]
	}
	return top
}

// computeDistribution returns min/median/p95/p99/max from a slice of
// per-project orphan counts. Empty input returns the zero
// distribution.
func computeDistribution(counts []int) OrphanDistributionStats {
	if len(counts) == 0 {
		return OrphanDistributionStats{}
	}
	sorted := append([]int(nil), counts...)
	sortInts(sorted)
	pick := func(p float64) int {
		// Nearest-rank: smallest index i such that i/n >= p.
		idx := int(float64(len(sorted)) * p)
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return OrphanDistributionStats{
		Min:    sorted[0],
		Median: pick(0.50),
		P95:    pick(0.95),
		P99:    pick(0.99),
		Max:    sorted[len(sorted)-1],
	}
}

// sortInts is a small in-place int sort. Kept local so we don't pull
// in `sort` package-wide here; it's the only place in this file that
// needs ordering.
func sortInts(xs []int) {
	// Insertion sort — counts slice is typically dominated by zeros,
	// so the inner loop converges quickly. For the rare large input
	// (say, 100K projects with non-zero counts) we'd swap to
	// sort.Ints, but the indirection isn't worth it today.
	for i := 1; i < len(xs); i++ {
		v := xs[i]
		j := i - 1
		for j >= 0 && xs[j] > v {
			xs[j+1] = xs[j]
			j--
		}
		xs[j+1] = v
	}
}
