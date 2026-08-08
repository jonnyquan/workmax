// DB-backed mention resolvers. Couple the pure parser to the platform
// asset tables so the server can expand `@character/<slug>`,
// `@brand/<slug>`, and `@product/<slug>` tokens using the same data
// the frontend reads from GET /api/character, /api/brand, /api/product.
//
// P1 #5 closed the deferred product path: w_global_product landed
// alongside w_global_brand / w_global_character, so the product
// resolver mirrors the brand path directly — no more nilResolver
// degradation for product mentions.

package canvas

import (
	"context"
	"strings"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// BuildCharacterMentionResolver returns a MentionResolver backed by the
// w_global_character table. Characters must belong to the given user
// and be active (status=1). Results are cached in-memory for the lifetime of
// the returned resolver — the expected caller pattern is one resolver
// per generation request.
//
// The lookup is restricted to slugs that actually appear in `prompt`
// so we only hit the DB when necessary and never surface characters
// the prompt didn't ask for.
func BuildCharacterMentionResolver(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	prompt string,
) MentionResolver {
	if db == nil || uid <= 0 {
		return nilResolver
	}
	targets := ExtractMentionTargets(prompt)
	if len(targets) == 0 {
		return nilResolver
	}
	slugs := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.Kind != MentionKindCharacter {
			continue
		}
		slug := strings.TrimSpace(t.Slug)
		if slug == "" {
			continue
		}
		slugs = append(slugs, slug)
	}
	if len(slugs) == 0 {
		return nilResolver
	}

	// Sprint-E: filter Confirmed=true so workagent-extracted drafts
	// (M4 protocol pre-Vocalize) don't leak into canvas @-mentions.
	// Canvas-created characters default Confirmed=true at the DDL
	// level (DEFAULT 1), so this gate catches workagent drafts
	// without breaking the canvas creation path.
	var rows []model.Character
	err := db.WithContext(ctx).
		Model(&model.Character{}).
		Where("uid = ? AND deleted_at IS NULL AND status = ? AND confirmed = ? AND slug IN ?",
			uid, model.CharacterStatusActive, true, slugs).
		Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nilResolver
	}

	registry := make(map[string]ResolvedMention, len(rows))
	for _, c := range rows {
		slug := strings.TrimSpace(c.Slug)
		if slug == "" {
			continue
		}
		replacement := strings.TrimSpace(c.Name)
		if replacement == "" {
			replacement = slug
		}
		registry["character|"+slug] = ResolvedMention{
			Replacement:    replacement,
			PromptSuffix:   strings.TrimSpace(c.PromptSuffix),
			NegativePrompt: strings.TrimSpace(c.NegativePrompt),
		}
	}

	return func(mention Mention) *ResolvedMention {
		key := string(mention.Kind) + "|" + mention.Slug
		hit, ok := registry[key]
		if !ok {
			return nil
		}
		return &hit
	}
}

// BuildBrandMentionResolver mirrors BuildCharacterMentionResolver
// against the platform `w_global_brand` table. Resolves `@brand/<slug>`
// tokens into prompt suffixes + negative fragments, same shape as the
// character path — the consumer (ResolveMentions) doesn't care which
// kind a row came from, only that the lookup returns a ResolvedMention.
//
// Filters Confirmed=true so workagent's M4-extracted drafts (pre-
// Vocalize) don't leak into canvas generation prompts. Canvas-created
// brands default Confirmed=true at the DDL level (DEFAULT 1).
//
// Restricting the IN list to slugs actually present in `prompt` keeps
// the DB round-trip O(mentioned-brands), not O(user-brands).
func BuildBrandMentionResolver(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	prompt string,
) MentionResolver {
	if db == nil || uid <= 0 {
		return nilResolver
	}
	targets := ExtractMentionTargets(prompt)
	if len(targets) == 0 {
		return nilResolver
	}
	slugs := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.Kind != MentionKindBrand {
			continue
		}
		slug := strings.TrimSpace(t.Slug)
		if slug == "" {
			continue
		}
		slugs = append(slugs, slug)
	}
	if len(slugs) == 0 {
		return nilResolver
	}

	var rows []model.Brand
	err := db.WithContext(ctx).
		Model(&model.Brand{}).
		Where("uid = ? AND deleted_at IS NULL AND status = ? AND confirmed = ? AND slug IN ?",
			uid, model.BrandStatusActive, true, slugs).
		Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nilResolver
	}

	registry := make(map[string]ResolvedMention, len(rows))
	for _, b := range rows {
		slug := strings.TrimSpace(b.Slug)
		if slug == "" {
			continue
		}
		replacement := strings.TrimSpace(b.Name)
		if replacement == "" {
			replacement = slug
		}
		registry["brand|"+slug] = ResolvedMention{
			Replacement:    replacement,
			PromptSuffix:   strings.TrimSpace(b.PromptSuffix),
			NegativePrompt: strings.TrimSpace(b.NegativePrompt),
		}
	}

	return func(mention Mention) *ResolvedMention {
		key := string(mention.Kind) + "|" + mention.Slug
		hit, ok := registry[key]
		if !ok {
			return nil
		}
		return &hit
	}
}

// BuildProductMentionResolver mirrors BuildBrandMentionResolver
// against the platform `w_global_product` table. P1 #5 — closes
// the previously-deferred product mention path.
//
// Same posture as brand: Confirmed=true filter (workagent draft
// extractions don't leak), per-slug batch fetch (no DB hit when
// the prompt carries no product mentions).
//
// Product-specific note: SKU is not part of the @-mention slug
// (the mention parser uses @kind/<slug>, not @kind/<sku>). If
// a user types "@product/sku-cr-2042" the slug literal must
// match the row's slug column — SKU-based addressing would
// need a separate mention syntax (e.g. @sku/CR-2042) and a
// new MentionKind. Out of scope here.
func BuildProductMentionResolver(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	prompt string,
) MentionResolver {
	if db == nil || uid <= 0 {
		return nilResolver
	}
	targets := ExtractMentionTargets(prompt)
	if len(targets) == 0 {
		return nilResolver
	}
	slugs := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.Kind != MentionKindProduct {
			continue
		}
		slug := strings.TrimSpace(t.Slug)
		if slug == "" {
			continue
		}
		slugs = append(slugs, slug)
	}
	if len(slugs) == 0 {
		return nilResolver
	}

	var rows []model.Product
	err := db.WithContext(ctx).
		Model(&model.Product{}).
		Where("uid = ? AND deleted_at IS NULL AND status = ? AND confirmed = ? AND slug IN ?",
			uid, model.ProductStatusActive, true, slugs).
		Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nilResolver
	}

	registry := make(map[string]ResolvedMention, len(rows))
	for _, p := range rows {
		slug := strings.TrimSpace(p.Slug)
		if slug == "" {
			continue
		}
		replacement := strings.TrimSpace(p.Name)
		if replacement == "" {
			replacement = slug
		}
		registry["product|"+slug] = ResolvedMention{
			Replacement:    replacement,
			PromptSuffix:   strings.TrimSpace(p.PromptSuffix),
			NegativePrompt: strings.TrimSpace(p.NegativePrompt),
		}
	}

	return func(mention Mention) *ResolvedMention {
		key := string(mention.Kind) + "|" + mention.Slug
		hit, ok := registry[key]
		if !ok {
			return nil
		}
		return &hit
	}
}

// BuildDirectorStyleMentionResolver mirrors BuildBrandMentionResolver
// against the platform `w_global_director_style` table. Resolves
// `@director/<slug>` tokens — the short form is intentional because
// the mention parser regex restricts the kind segment to [a-zA-Z]+
// (no hyphens), so `@director-style/...` would not parse as a single
// kind. `@director` reads naturally in prompts and the resolver still
// hits the full `w_global_director_style` row.
//
// Same posture as brand/product: Confirmed=true filter (workagent
// M4-extracted drafts must not leak into canvas generation), per-slug
// batch fetch scoped to the slugs actually mentioned in the prompt
// (no DB hit when no director mentions are present).
//
// Director-style-specific: the row carries 5 cinematic axes
// (composition / color / lighting / motion / texture) but mention
// resolution only consumes PromptSuffix + NegativePrompt — the axes
// are for richer downstream injectors (e.g. preflight composer) that
// can slice them per medium. Mentions are intentionally a coarse
// "apply this director's whole prompt fingerprint" gesture; finer
// per-axis control lives at the workagent surface, not the prompt
// shorthand.
func BuildDirectorStyleMentionResolver(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	prompt string,
) MentionResolver {
	if db == nil || uid <= 0 {
		return nilResolver
	}
	targets := ExtractMentionTargets(prompt)
	if len(targets) == 0 {
		return nilResolver
	}
	slugs := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.Kind != MentionKindDirector {
			continue
		}
		slug := strings.TrimSpace(t.Slug)
		if slug == "" {
			continue
		}
		slugs = append(slugs, slug)
	}
	if len(slugs) == 0 {
		return nilResolver
	}

	var rows []model.DirectorStyle
	err := db.WithContext(ctx).
		Model(&model.DirectorStyle{}).
		Where("uid = ? AND deleted_at IS NULL AND status = ? AND confirmed = ? AND slug IN ?",
			uid, model.DirectorStyleStatusActive, true, slugs).
		Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nilResolver
	}

	registry := make(map[string]ResolvedMention, len(rows))
	for _, d := range rows {
		slug := strings.TrimSpace(d.Slug)
		if slug == "" {
			continue
		}
		replacement := strings.TrimSpace(d.Name)
		if replacement == "" {
			replacement = slug
		}
		registry["director|"+slug] = ResolvedMention{
			Replacement:    replacement,
			PromptSuffix:   strings.TrimSpace(d.PromptSuffix),
			NegativePrompt: strings.TrimSpace(d.NegativePrompt),
		}
	}

	return func(mention Mention) *ResolvedMention {
		key := string(mention.Kind) + "|" + mention.Slug
		hit, ok := registry[key]
		if !ok {
			return nil
		}
		return &hit
	}
}

// ComposeMentionResolvers returns a resolver that tries each input in
// order, returning the first non-nil result. Lets ResolvePromptSafely
// fan out to per-kind resolvers without growing a per-kind switch into
// the parser. Nil and nilResolver inputs are skipped — passing
// `Compose(a, nilResolver, b)` collapses cleanly to `Compose(a, b)`.
//
// First-match wins; per-kind resolvers don't overlap in practice
// (Brand resolver only ever resolves `@brand/...`, Character only
// `@character/...`) so order doesn't matter for correctness — but it
// pins the precedence in case a future kind ever does collide.
func ComposeMentionResolvers(resolvers ...MentionResolver) MentionResolver {
	live := make([]MentionResolver, 0, len(resolvers))
	for _, r := range resolvers {
		if r == nil {
			continue
		}
		live = append(live, r)
	}
	if len(live) == 0 {
		return nilResolver
	}
	if len(live) == 1 {
		return live[0]
	}
	return func(mention Mention) *ResolvedMention {
		for _, r := range live {
			if hit := r(mention); hit != nil {
				return hit
			}
		}
		return nil
	}
}

// ResolvePromptSafely is the call-site helper used by the generator.
// It silently returns the inputs unchanged if the prompt has no
// mentions or the DB is unavailable — the server-side resolver is a
// safety net, never a gate.
//
// Composes the character + brand + product + director resolvers so
// prompts with mixed `@character/...`, `@brand/...`, `@product/...`,
// `@director/...` tokens expand all four in one pass. P1 #5 closed
// the product gap; the director gap closed when w_global_director_style
// was promoted out of the workagent-internal asset table (Sprint-E,
// 2026-05-11) — see model/director_style.go header.
func ResolvePromptSafely(
	ctx context.Context,
	uid uint,
	prompt string,
	negativePrompt string,
) (string, string) {
	if prompt == "" {
		return prompt, negativePrompt
	}
	if !HasMentions(prompt) {
		return prompt, negativePrompt
	}
	db := globals.GraDBs["system"]
	if db == nil {
		return prompt, negativePrompt
	}
	resolver := ComposeMentionResolvers(
		BuildCharacterMentionResolver(ctx, db, int(uid), prompt),
		BuildBrandMentionResolver(ctx, db, int(uid), prompt),
		BuildProductMentionResolver(ctx, db, int(uid), prompt),
		BuildDirectorStyleMentionResolver(ctx, db, int(uid), prompt),
	)
	resolved := ResolvePromptForGenerator(prompt, negativePrompt, resolver, ", ")
	return resolved.PromptForGenerator, resolved.NegativePromptForGenerator
}

// HasMentions is a cheap pre-check so the resolver can skip the DB
// round-trip on prompts that clearly have no mentions.
func HasMentions(text string) bool {
	if len(text) == 0 {
		return false
	}
	return mentionRegex.MatchString(text)
}

var nilResolver MentionResolver = func(Mention) *ResolvedMention { return nil }
