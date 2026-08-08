// Continuity-tracked character references (Task #13, borrowable
// idea from HKUDS/ViMax). When a character has already been rendered
// successfully in this project's history, the most recent render is
// a *stronger* reference than the character's static avatar — it
// already encodes the project's lighting, mood, and style choices.
//
// This file builds an in-memory "continuity index" keyed by
// characterID, sourced from the same w_canvas_task_binding ×
// w_generation_task join the decision-log endpoint uses. No new
// table; no write path; pure projection.
//
// What it does NOT do:
//
//   - Does not replace the character's avatar URL; it stacks
//     *before* it. The generator sees [latest, avatar, ...] and
//     each provider's reference-weighting handles the rest.
//   - Does not look across projects. Continuity is per-project on
//     purpose: a character might be lit differently between project
//     A and project B, and bleeding one into the other would hurt
//     more than help.
//   - Does not look at non-canvas tasks. Workagent or batch tasks
//     don't carry the canvas binding row, so they're invisible to
//     this query — by design.

package canvas

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"server/model"
)

// MaxContinuityScanRows caps how far back we scan a project's task
// history when building the index. Real projects produce O(tens-
// to-hundreds) tasks; scanning a few hundred is cheap and the
// most-recent-wins semantics mean older rows are noise anyway.
const MaxContinuityScanRows = 200

// ContinuityIndex maps a characterID to the URL of the most recent
// successful generation in the active project that referenced that
// character. Empty entries mean "no prior render"; the caller treats
// that as "no continuity ref to overlay".
type ContinuityIndex map[int]string

// continuityScanRow is the in-memory join target. Anonymous so it
// doesn't leak past this file.
type continuityScanRow struct {
	RequestData model.JSONMap
	ResultData  model.JSONMap
	CompletedAt *time.Time
	CreatedAt   time.Time
}

// BuildContinuityIndex queries the project's recent successful tasks
// and returns the most-recent-render URL per character in
// characterIDs. Characters with no prior render are absent from the
// map (NOT mapped to "") so the caller can `_, ok := idx[id]` to
// branch cleanly.
//
// Posture:
//   - Returns an empty map on nil DB / zero uid / zero projectID /
//     empty characterIDs. Never errors on those — the continuity
//     overlay is a safety-net feature; an unavailable lookup should
//     degrade silently to "no continuity refs", not fail the
//     generation.
//   - Caps the SQL scan at MaxContinuityScanRows. Most recent wins
//     so older rows in the tail can't beat newer ones anyway.
//   - Filters status = completed at the SQL layer.
func BuildContinuityIndex(
	ctx context.Context,
	db *gorm.DB,
	uid uint,
	projectID uint64,
	characterIDs []int,
) (ContinuityIndex, error) {
	if db == nil || uid == 0 || projectID == 0 || len(characterIDs) == 0 {
		return ContinuityIndex{}, nil
	}

	rows := make([]continuityScanRow, 0, 32)
	err := db.WithContext(ctx).
		Table("w_canvas_task_binding AS b").
		Select(
			"t.request_data AS request_data",
			"t.result_data AS result_data",
			"t.completed_at AS completed_at",
			"t.created_at AS created_at",
		).
		Joins("JOIN w_generation_task AS t ON t.task_id = b.task_id").
		Where("b.uid = ? AND b.project_id = ? AND t.status = ?",
			uid, projectID, model.TaskStatusCompleted).
		Order("t.completed_at DESC, t.created_at DESC").
		Limit(MaxContinuityScanRows).
		Find(&rows).Error
	if err != nil {
		return ContinuityIndex{}, err
	}

	requested := make(map[int]struct{}, len(characterIDs))
	for _, id := range characterIDs {
		if id <= 0 {
			continue
		}
		requested[id] = struct{}{}
	}

	return projectContinuityRowsToIndex(rows, requested), nil
}

// projectContinuityRowsToIndex is the pure projection over already-
// loaded rows. Each row's request_data is decoded for
// assetBindings.characterIds; for each requested character present
// in the row, if we haven't yet assigned a URL we take the row's
// first result imageUrl. SQL ORDER BY ensures "first row touching
// a character" = "most recent render for that character".
func projectContinuityRowsToIndex(
	rows []continuityScanRow,
	requested map[int]struct{},
) ContinuityIndex {
	idx := ContinuityIndex{}
	if len(rows) == 0 || len(requested) == 0 {
		return idx
	}
	for _, row := range rows {
		if len(idx) == len(requested) {
			break // every requested character already has a hit
		}
		rowCharacters := extractCharacterIDsFromRequest(row.RequestData)
		if len(rowCharacters) == 0 {
			continue
		}
		mediaURL := firstSuccessfulImageURL(row.ResultData)
		if mediaURL == "" {
			continue
		}
		for _, cid := range rowCharacters {
			if cid <= 0 {
				continue
			}
			if _, want := requested[cid]; !want {
				continue
			}
			if _, has := idx[cid]; has {
				continue
			}
			idx[cid] = mediaURL
		}
	}
	return idx
}

// extractCharacterIDsFromRequest pulls assetBindings.characterIds
// from the loose JSONMap. Returns nil when the path is missing or
// shaped unexpectedly — same posture as the rest of the JSONMap
// decode in this package (the asset binding wire shape has changed
// once before and we want forward tolerance).
func extractCharacterIDsFromRequest(req model.JSONMap) []int {
	if req == nil {
		return nil
	}
	rawBindings, ok := req["assetBindings"]
	if !ok || rawBindings == nil {
		return nil
	}
	bindings, ok := rawBindings.(map[string]interface{})
	if !ok {
		return nil
	}
	rawIDs, ok := bindings["characterIds"]
	if !ok || rawIDs == nil {
		return nil
	}
	arr, ok := rawIDs.([]interface{})
	if !ok {
		return nil
	}
	out := make([]int, 0, len(arr))
	for _, raw := range arr {
		switch v := raw.(type) {
		case float64:
			out = append(out, int(v))
		case int:
			out = append(out, v)
		case int64:
			out = append(out, int(v))
		}
	}
	return out
}

// firstSuccessfulImageURL pulls the first non-empty URL from
// resultData.imageUrls (preferred) or resultData.videoUrls (fallback).
// Returns "" when neither carries a usable URL — the caller skips
// the row in that case.
func firstSuccessfulImageURL(res model.JSONMap) string {
	if res == nil {
		return ""
	}
	for _, key := range []string{"imageUrls", "videoUrls", "outputUrls"} {
		rawList, ok := res[key]
		if !ok || rawList == nil {
			continue
		}
		arr, ok := rawList.([]interface{})
		if !ok {
			continue
		}
		for _, raw := range arr {
			if s, ok := raw.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}
