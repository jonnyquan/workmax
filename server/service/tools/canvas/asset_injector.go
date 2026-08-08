// Asset context injection for canvas generation (§8.4). When a canvas
// element carries an AssetBinding (character/brand/product IDs), the
// injector expands those into prompt suffixes and reference images so
// the downstream image/video provider sees the resolved context — not
// just opaque IDs.
//
// Split in two layers so the pure merge logic is testable without a
// database:
//
//   ApplyAssetContext(basePrompt, baseNegative, bindings, loaded)
//       — pure, deterministic. Callers that already have the asset
//         rows (e.g. a batch that pre-loaded them) can call this
//         directly.
//
//   InjectAssetContext(ctx, db, uid, ...)
//       — DB-backed wrapper. Fetches the characters the bindings
//         reference (scoped to uid + active status) and hands off to
//         ApplyAssetContext. Brand/product channels are stubbed until
//         their models land.
//
// Never fails hard: missing rows, nil DB, or stale IDs all degrade to
// "don't inject that entry" rather than erroring the generation call.

package canvas

import (
	"context"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"server/model"
)

// Weight bounds are shared with the model package and the frontend
// mention-parser so all three layers produce the same multiplier for
// a given `@kind/slug:weight` tail.
const (
	assetWeightDefault = model.AssetWeightDefault
	assetWeightMin     = model.AssetWeightMin
	assetWeightMax     = model.AssetWeightMax
)

// weightFor returns the clamped multiplier for `id` from an optional
// weight map. Defaults to 1 when the map is nil, the key is missing, or
// the stored value is non-finite / out-of-range. Never panics — a bad
// value degrades to the default, same as the frontend clamp and the
// AssetBinding.Sanitize parse-time cleanup.
func weightFor(weights model.AssetWeightMap, id int) float64 {
	if len(weights) == 0 {
		return assetWeightDefault
	}
	raw, ok := weights[strconv.Itoa(id)]
	if !ok {
		return assetWeightDefault
	}
	if raw != raw { // NaN check
		return assetWeightDefault
	}
	if raw < assetWeightMin {
		return assetWeightMin
	}
	if raw > assetWeightMax {
		return assetWeightMax
	}
	return raw
}

// ReferenceImage is a single visual reference the generator should
// condition on (character sheet, brand mood board, product SKU photo).
// Weight is 0-1 and defaults to 1 when absent.
type ReferenceImage struct {
	URL    string
	Label  string
	Weight float64
}

// AssetContext is the output of the injector. EnrichedPrompt and
// EnrichedNegativePrompt always contain the base text; the injector
// never drops user input.
type AssetContext struct {
	EnrichedPrompt         string
	EnrichedNegativePrompt string
	ReferenceImages        []ReferenceImage
}

// ApplyAssetContext is the pure merge. Order of characters in the
// output mirrors the order of IDs in bindings.CharacterIDs, not the
// order rows came back from the database — the caller's binding list
// is the source of truth for composition.
func ApplyAssetContext(
	basePrompt string,
	baseNegative string,
	bindings *AssetBinding,
	loadedCharacters []model.Character,
) AssetContext {
	ctx := AssetContext{
		EnrichedPrompt:         basePrompt,
		EnrichedNegativePrompt: baseNegative,
	}
	if bindings == nil {
		return ctx
	}

	characters := indexCharactersByID(loadedCharacters)

	promptParts := make([]string, 0, len(bindings.CharacterIDs))
	negativeParts := make([]string, 0, len(bindings.CharacterIDs))
	seenRefURLs := make(map[string]struct{})

	for _, id := range bindings.CharacterIDs {
		c, ok := characters[id]
		if !ok {
			continue
		}
		if suffix := strings.TrimSpace(c.PromptSuffix); suffix != "" {
			promptParts = append(promptParts, suffix)
		}
		if neg := strings.TrimSpace(c.NegativePrompt); neg != "" {
			negativeParts = append(negativeParts, neg)
		}
		if url := strings.TrimSpace(c.AvatarImageURL); url != "" {
			if _, dup := seenRefURLs[url]; !dup {
				seenRefURLs[url] = struct{}{}
				ctx.ReferenceImages = append(ctx.ReferenceImages, ReferenceImage{
					URL:    url,
					Label:  "character:" + nonEmpty(c.Slug, c.Name),
					Weight: weightFor(bindings.CharacterWeights, id),
				})
			}
		}
	}

	ctx.EnrichedPrompt = joinPromptSegments(basePrompt, promptParts)
	ctx.EnrichedNegativePrompt = joinPromptSegments(baseNegative, negativeParts)
	return ctx
}

// ApplyContinuityOverlay prepends the most-recent-render URL for
// each character (per continuity) to the front of the AssetContext's
// reference list. The result list reads as:
//
//   [latest-render-of-char-A, latest-render-of-char-B, avatar-A,
//    avatar-B, …existing refs…]
//
// Existing refs are preserved verbatim; URLs already in the list
// (e.g. the same image is both the latest render AND the static
// avatar) are deduped so the generator doesn't see the same URL
// twice.
//
// Pure: no DB access. Caller is responsible for building the
// ContinuityIndex via BuildContinuityIndex.
//
// No-op when bindings is nil, has no CharacterIDs, or index is
// empty. This is deliberate: the overlay is a SAFETY-NET
// enhancement. If continuity lookup failed or returned nothing,
// generation should proceed with the avatar-only ref list, never
// fail because continuity was unavailable.
func ApplyContinuityOverlay(
	context AssetContext,
	bindings *AssetBinding,
	loadedCharacters []model.Character,
	index ContinuityIndex,
) AssetContext {
	if bindings == nil || len(index) == 0 || len(bindings.CharacterIDs) == 0 {
		return context
	}
	characters := indexCharactersByID(loadedCharacters)

	// Walk requested character IDs in order so the continuity refs
	// appear in the same priority as the binding's character order
	// (the character that appears first in the prompt's
	// AssetBinding is the one whose continuity ref ranks first).
	prepended := make([]ReferenceImage, 0, len(bindings.CharacterIDs))
	seen := make(map[string]struct{}, len(context.ReferenceImages))
	for _, ref := range context.ReferenceImages {
		seen[ref.URL] = struct{}{}
	}
	for _, id := range bindings.CharacterIDs {
		url, ok := index[id]
		if !ok {
			continue
		}
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		label := "continuity"
		if c, ok := characters[id]; ok {
			label = "continuity:" + nonEmpty(c.Slug, c.Name)
		}
		prepended = append(prepended, ReferenceImage{
			URL:    url,
			Label:  label,
			Weight: weightFor(bindings.CharacterWeights, id),
		})
	}
	if len(prepended) == 0 {
		return context
	}
	// Stitch prepended + existing. Allocate exact-size slice so we
	// don't trail capacity slack.
	merged := make([]ReferenceImage, 0, len(prepended)+len(context.ReferenceImages))
	merged = append(merged, prepended...)
	merged = append(merged, context.ReferenceImages...)
	context.ReferenceImages = merged
	return context
}

// InjectAssetContext loads the characters referenced by bindings and
// applies them. brand/product channels are no-ops until those models
// exist; once they do this function fans out in parallel fetches.
func InjectAssetContext(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	basePrompt string,
	baseNegative string,
	bindings *AssetBinding,
) (AssetContext, error) {
	if bindings == nil || db == nil || uid <= 0 {
		return ApplyAssetContext(basePrompt, baseNegative, bindings, nil), nil
	}

	// Sprint-E: filter Confirmed=true (mirror mention_resolver.go).
	// Workagent draft characters shouldn't ride into canvas
	// generation prompts until the user vocalizes.
	var characters []model.Character
	if ids := dedupIDs(bindings.CharacterIDs); len(ids) > 0 {
		if err := db.WithContext(ctx).
			Model(&model.Character{}).
			Where("uid = ? AND deleted_at IS NULL AND status = ? AND confirmed = ? AND id IN ?",
				uid, model.CharacterStatusActive, true, ids).
			Find(&characters).Error; err != nil {
			return AssetContext{
				EnrichedPrompt:         basePrompt,
				EnrichedNegativePrompt: baseNegative,
			}, err
		}
	}

	// Brand context flows through the @brand/<slug> mention path
	// (see BuildBrandMentionResolver + ResolvePromptSafely in
	// mention_resolver.go), not through this AssetBinding loader —
	// the earlier `BrandIDs` slot on AssetBinding was retired with
	// the verticals (see model/canvas_asset_binding.go header) and
	// re-adding it would duplicate the mention machinery.
	//
	// Product `@product/<slug>` mentions now route through
	// BuildProductMentionResolver in mention_resolver.go (P1 #5).
	// AssetBinding's BrandIDs / ProductIDs slot path stays
	// retired — the @-mention path is the canonical surface.

	out := ApplyAssetContext(basePrompt, baseNegative, bindings, characters)

	// Continuity overlay (Task #13). When the binding carries a
	// canvasProjectId, query the project's prior successful renders
	// and prepend the most-recent-per-character image to the ref
	// list — gives the generator a stronger conditioning signal
	// than the static avatar for projects with prior history.
	//
	// Errors here are non-fatal: a continuity lookup failure
	// degrades to "no continuity refs", same way a character DB
	// fetch failure earlier in this function would have. The
	// generation should never fail because the safety-net was
	// unavailable.
	if bindings.CanvasProjectID != nil && *bindings.CanvasProjectID > 0 && len(bindings.CharacterIDs) > 0 {
		index, err := BuildContinuityIndex(ctx, db, uint(uid), *bindings.CanvasProjectID, bindings.CharacterIDs)
		if err == nil && len(index) > 0 {
			out = ApplyContinuityOverlay(out, bindings, characters, index)
		}
	}

	return out, nil
}

func indexCharactersByID(rows []model.Character) map[int]model.Character {
	if len(rows) == 0 {
		return nil
	}
	out := make(map[int]model.Character, len(rows))
	for _, c := range rows {
		out[int(c.Id)] = c
	}
	return out
}

func dedupIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// joinPromptSegments appends suffixes to a base prompt with ", " —
// matches the separator the mention resolver already uses so two
// pipelines produce the same text.
func joinPromptSegments(base string, parts []string) string {
	base = strings.TrimSpace(base)
	if len(parts) == 0 {
		return base
	}
	addition := strings.Join(parts, ", ")
	if base == "" {
		return addition
	}
	return base + ", " + addition
}

func nonEmpty(candidates ...string) string {
	for _, c := range candidates {
		if s := strings.TrimSpace(c); s != "" {
			return s
		}
	}
	return ""
}
