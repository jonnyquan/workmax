// mention_resolver_brand_test.go — coverage for the brand mention
// resolver added alongside the platform brand library (M3-W8-01).
// Pins three contracts:
//
//   1. Guard symmetry with the character resolver — nil DB / uid<=0 /
//      no brand targets all short-circuit to nilResolver before any
//      DB call. Same performance posture and same correctness shape.
//   2. ComposeMentionResolvers — pure function combinator that fans
//      out per-kind resolvers without growing a switch in the parser.
//   3. End-to-end DB-backed lookup — slug → ResolvedMention with
//      PromptSuffix + NegativePrompt; Confirmed=false rows are
//      excluded (workagent M4 draft watermark posture).

package canvas

import (
	"context"
	"testing"

	"server/model"
	"server/utils/testutil"
)

func TestBuildBrandMentionResolver_ReturnsNilResolverWhenDBIsNil(t *testing.T) {
	r := BuildBrandMentionResolver(context.Background(), nil, 1, "@brand/nike")
	if r == nil {
		t.Fatal("resolver must always be a non-nil fn")
	}
	if r(Mention{Kind: MentionKindBrand, Slug: "nike"}) != nil {
		t.Errorf("nil-DB resolver should return nil for every mention")
	}
}

func TestBuildBrandMentionResolver_ReturnsNilResolverWhenUIDNonPositive(t *testing.T) {
	// Same uid-guard contract as BuildCharacterMentionResolver. uid=0
	// must short-circuit BEFORE building a DB query — querying with
	// uid=0 could leak another user's brand row if a system-seeded
	// brand with uid=0 ever existed.
	for _, uid := range []int{-1, 0} {
		r := BuildBrandMentionResolver(context.Background(), nil, uid, "@brand/nike")
		if r == nil {
			t.Fatalf("uid=%d resolver should still be a non-nil fn", uid)
		}
		if r(Mention{Kind: MentionKindBrand, Slug: "nike"}) != nil {
			t.Errorf("uid=%d resolver should return nil for every mention", uid)
		}
	}
}

func TestBuildBrandMentionResolver_CharacterOnlyPromptReturnsNilResolver(t *testing.T) {
	// A prompt that only mentions @character/... has no brand slugs.
	// The slug-accumulator's `t.Kind != MentionKindBrand` filter skips
	// them and the `len(slugs) == 0` guard bails before any DB call.
	// Passing nil DB proves the guard held — a non-nil-skipping path
	// would panic on WithContext.
	r := BuildBrandMentionResolver(
		context.Background(),
		nil, // nil db — must NOT be reached
		42,
		"@character/林夏 walks past at sunrise",
	)
	if r == nil {
		t.Fatal("resolver must be a non-nil fn even for character-only prompts")
	}
	if r(Mention{Kind: MentionKindBrand, Slug: "anyone"}) != nil {
		t.Errorf("brand lookup on character-only prompt should resolve nothing")
	}
}

func TestBuildBrandMentionResolver_DBBackedHappyPath(t *testing.T) {
	// End-to-end: seed two brands (one Confirmed=true, one draft),
	// build the resolver, and assert the confirmed row resolves
	// while the draft is excluded. Mirrors the M4 Vocalize lifecycle
	// — workagent M4-extracted drafts (Confirmed=false) must not
	// leak into canvas generation prompts.
	db := testutil.NewTestDB(t)

	uid := 42
	confirmed := &model.Brand{
		UID:            uid,
		Name:           "Nike",
		Slug:           "nike",
		Status:         model.BrandStatusActive,
		Confirmed:      true,
		Lang:           "en",
		PromptSuffix:   "swoosh logo, athletic apparel",
		NegativePrompt: "off-brand, knockoff",
		SourceKind:     model.BrandSourceManual,
	}
	if err := db.Create(confirmed).Error; err != nil {
		t.Fatalf("seed confirmed brand: %v", err)
	}
	draft := &model.Brand{
		UID:        uid,
		Name:       "Adidas",
		Slug:       "adidas",
		Status:     model.BrandStatusActive,
		Confirmed:  false, // workagent draft watermark
		Lang:       "en",
		SourceKind: model.BrandSourceExtracted,
	}
	if err := db.Select("*").Create(draft).Error; err != nil {
		t.Fatalf("seed draft brand: %v", err)
	}

	r := BuildBrandMentionResolver(
		context.Background(),
		db,
		uid,
		"showcase @brand/nike alongside @brand/adidas competitors",
	)

	// Confirmed row resolves.
	hit := r(Mention{Kind: MentionKindBrand, Slug: "nike"})
	if hit == nil {
		t.Fatal("nike (confirmed) should resolve, got nil")
	}
	if hit.Replacement != "Nike" {
		t.Errorf("Replacement = %q, want 'Nike'", hit.Replacement)
	}
	if hit.PromptSuffix != "swoosh logo, athletic apparel" {
		t.Errorf("PromptSuffix = %q, want the seeded suffix", hit.PromptSuffix)
	}
	if hit.NegativePrompt != "off-brand, knockoff" {
		t.Errorf("NegativePrompt = %q, want the seeded negative", hit.NegativePrompt)
	}

	// Draft row is excluded — even though it exists and was mentioned.
	if r(Mention{Kind: MentionKindBrand, Slug: "adidas"}) != nil {
		t.Errorf("adidas (draft, Confirmed=false) must NOT resolve — M4 watermark")
	}
}

func TestBuildBrandMentionResolver_CrossTenantIsolation(t *testing.T) {
	// uid is part of the WHERE clause. A brand owned by uid=100 must
	// not surface for uid=42 even if the slug matches. Same IDOR
	// posture as the rest of the platform — defense in depth, the
	// frontend never sends another user's slug but a bug in upstream
	// resolution could.
	db := testutil.NewTestDB(t)

	other := &model.Brand{
		UID:        100,
		Name:       "Other-User Nike",
		Slug:       "nike",
		Status:     model.BrandStatusActive,
		Confirmed:  true,
		Lang:       "en",
		SourceKind: model.BrandSourceManual,
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("seed cross-tenant brand: %v", err)
	}

	r := BuildBrandMentionResolver(context.Background(), db, 42, "@brand/nike at sunrise")
	if r(Mention{Kind: MentionKindBrand, Slug: "nike"}) != nil {
		t.Errorf("cross-tenant brand must not resolve — IDOR regression")
	}
}

// ---------------------------------------------------------------------
// ComposeMentionResolvers
// ---------------------------------------------------------------------

func TestComposeMentionResolvers_EmptyInputReturnsNilResolver(t *testing.T) {
	// Zero resolvers compose into nilResolver — the canonical "resolve
	// nothing" fn. Saves callers from a nil-check at the call site.
	r := ComposeMentionResolvers()
	if r == nil {
		t.Fatal("ComposeMentionResolvers() must return a non-nil fn")
	}
	if r(Mention{Kind: MentionKindCharacter, Slug: "anything"}) != nil {
		t.Errorf("zero-input composite should resolve nothing")
	}
}

func TestComposeMentionResolvers_FiltersNilInputs(t *testing.T) {
	// Real-world: BuildBrandMentionResolver returns nilResolver when
	// the prompt has no brand mentions. The composer must treat that
	// the same as the upstream resolver returning nil — pass-through
	// to the next resolver in line. Pin the contract by mixing a
	// productive resolver with nilResolver / nil entries.
	hit := ResolvedMention{Replacement: "ACME", PromptSuffix: "logo"}
	productive := func(m Mention) *ResolvedMention {
		if m.Slug == "acme" {
			return &hit
		}
		return nil
	}
	r := ComposeMentionResolvers(nil, nilResolver, productive, nilResolver)
	if got := r(Mention{Kind: MentionKindBrand, Slug: "acme"}); got == nil || got.Replacement != "ACME" {
		t.Errorf("composite should reach the productive resolver, got %+v", got)
	}
	if r(Mention{Kind: MentionKindBrand, Slug: "missing"}) != nil {
		t.Errorf("composite should return nil when no resolver matches")
	}
}

func TestComposeMentionResolvers_FirstMatchWins(t *testing.T) {
	// Per-kind resolvers don't overlap in practice, but the composer's
	// precedence contract is "first non-nil wins". Pin so a future
	// kind that DID overlap (or a configuration bug that registered the
	// same kind twice) behaves predictably.
	first := func(m Mention) *ResolvedMention {
		return &ResolvedMention{Replacement: "first"}
	}
	second := func(m Mention) *ResolvedMention {
		return &ResolvedMention{Replacement: "second"}
	}
	r := ComposeMentionResolvers(first, second)
	got := r(Mention{Kind: MentionKindCharacter, Slug: "x"})
	if got == nil || got.Replacement != "first" {
		t.Errorf("composite should return 'first', got %+v", got)
	}
}
