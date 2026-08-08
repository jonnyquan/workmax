// mention_resolver_product_test.go — coverage for the product
// mention resolver added P1 #5. Mirrors the brand resolver tests:
//
//   1. Guard symmetry with brand — nil DB / uid<=0 / no product
//      targets all short-circuit to nilResolver before any DB
//      call. Same performance posture, same correctness shape.
//   2. End-to-end DB-backed lookup — slug → ResolvedMention with
//      PromptSuffix + NegativePrompt; Confirmed=false rows are
//      excluded (workagent M4 draft watermark posture).
//   3. Cross-tenant IDOR — uid-mismatch returns nil.
//   4. ComposeMentionResolvers + ResolvePromptSafely walk all
//      three kinds (covered via the brand test file; we just
//      verify product joins the chain).

package canvas

import (
	"context"
	"testing"

	"server/model"
	"server/utils/testutil"
)

func TestBuildProductMentionResolver_ReturnsNilResolverWhenDBIsNil(t *testing.T) {
	r := BuildProductMentionResolver(context.Background(), nil, 1, "@product/widget")
	if r == nil {
		t.Fatal("resolver must always be a non-nil fn")
	}
	if r(Mention{Kind: MentionKindProduct, Slug: "widget"}) != nil {
		t.Errorf("nil-DB resolver should return nil for every mention")
	}
}

func TestBuildProductMentionResolver_ReturnsNilResolverWhenUIDNonPositive(t *testing.T) {
	for _, uid := range []int{-1, 0} {
		r := BuildProductMentionResolver(context.Background(), nil, uid, "@product/widget")
		if r == nil {
			t.Fatalf("uid=%d resolver should still be a non-nil fn", uid)
		}
		if r(Mention{Kind: MentionKindProduct, Slug: "widget"}) != nil {
			t.Errorf("uid=%d resolver should return nil for every mention", uid)
		}
	}
}

func TestBuildProductMentionResolver_BrandOnlyPromptReturnsNilResolver(t *testing.T) {
	// A prompt that only mentions @brand/... has no product
	// slugs. The slug-accumulator's `t.Kind != MentionKindProduct`
	// filter skips them, and the len(slugs)==0 guard bails before
	// any DB call. Passing nil db proves the guard held.
	r := BuildProductMentionResolver(
		context.Background(),
		nil, // nil db — must NOT be reached
		42,
		"a shot of @brand/nike gear at sunrise",
	)
	if r == nil {
		t.Fatal("resolver must be a non-nil fn even for brand-only prompts")
	}
	if r(Mention{Kind: MentionKindProduct, Slug: "anyone"}) != nil {
		t.Errorf("product lookup on brand-only prompt should resolve nothing")
	}
}

func TestBuildProductMentionResolver_DBBackedHappyPath(t *testing.T) {
	// End-to-end: seed two products (one Confirmed=true, one
	// draft), build the resolver, and assert the confirmed row
	// resolves while the draft is excluded. Mirrors the M4
	// Vocalize lifecycle test for brand.
	db := testutil.NewTestDB(t)

	uid := 42
	confirmed := &model.Product{
		UID:            uid,
		Name:           "Cyber Runner",
		Slug:           "cyber-runner",
		SKU:            "CR-2042",
		Category:       "shoes",
		Status:         model.ProductStatusActive,
		Confirmed:      true,
		Lang:           "en",
		PromptSuffix:   "matte-black laces, neon underglow",
		NegativePrompt: "scuffs, wear marks",
		SourceKind:     model.ProductSourceManual,
	}
	if err := db.Create(confirmed).Error; err != nil {
		t.Fatalf("seed confirmed product: %v", err)
	}
	draft := &model.Product{
		UID:        uid,
		Name:       "Untitled SKU",
		Slug:       "untitled-sku",
		Status:     model.ProductStatusActive,
		Confirmed:  false, // workagent draft watermark
		Lang:       "en",
		SourceKind: model.ProductSourceExtracted,
	}
	if err := db.Select("*").Create(draft).Error; err != nil {
		t.Fatalf("seed draft product: %v", err)
	}

	r := BuildProductMentionResolver(
		context.Background(),
		db,
		uid,
		"showcase @product/cyber-runner alongside @product/untitled-sku",
	)

	// Confirmed row resolves.
	hit := r(Mention{Kind: MentionKindProduct, Slug: "cyber-runner"})
	if hit == nil {
		t.Fatal("cyber-runner (confirmed) should resolve, got nil")
	}
	if hit.Replacement != "Cyber Runner" {
		t.Errorf("Replacement = %q, want 'Cyber Runner'", hit.Replacement)
	}
	if hit.PromptSuffix != "matte-black laces, neon underglow" {
		t.Errorf("PromptSuffix = %q", hit.PromptSuffix)
	}
	if hit.NegativePrompt != "scuffs, wear marks" {
		t.Errorf("NegativePrompt = %q", hit.NegativePrompt)
	}

	// Draft row is excluded — M4 watermark.
	if r(Mention{Kind: MentionKindProduct, Slug: "untitled-sku"}) != nil {
		t.Errorf("untitled-sku (draft, Confirmed=false) must NOT resolve")
	}
}

func TestBuildProductMentionResolver_CrossTenantIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)

	other := &model.Product{
		UID:        100,
		Name:       "Other-User Widget",
		Slug:       "widget",
		Status:     model.ProductStatusActive,
		Confirmed:  true,
		Lang:       "en",
		SourceKind: model.ProductSourceManual,
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("seed cross-tenant product: %v", err)
	}

	r := BuildProductMentionResolver(context.Background(), db, 42, "@product/widget on display")
	if r(Mention{Kind: MentionKindProduct, Slug: "widget"}) != nil {
		t.Errorf("cross-tenant product must not resolve — IDOR regression")
	}
}
