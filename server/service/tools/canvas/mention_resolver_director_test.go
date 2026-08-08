// mention_resolver_director_test.go — coverage for the
// `@director/<slug>` mention resolver added on top of the
// w_global_director_style platform table (promoted out of the
// workagent-internal asset table in Sprint-E, 2026-05-11).
//
// Pins four contracts (mirrors brand / product resolvers exactly so a
// future maintainer reading any one of the three files learns the
// pattern in one place):
//
//   1. Guard symmetry — nil DB / uid<=0 / no director targets all
//      short-circuit to nilResolver before any DB call.
//   2. Character-only prompt is a no-op for the director resolver
//      (nil DB must not be reached) — proves the slug-filter holds.
//   3. End-to-end DB-backed lookup: slug → ResolvedMention with
//      PromptSuffix + NegativePrompt; Confirmed=false rows excluded
//      (workagent M4-draft watermark posture, identical to brand).
//   4. Cross-tenant isolation: a director-style owned by another
//      uid must not surface even when the slug matches (IDOR
//      defense in depth).

package canvas

import (
	"context"
	"testing"

	"server/model"
	"server/utils/testutil"
)

func TestBuildDirectorStyleMentionResolver_ReturnsNilResolverWhenDBIsNil(t *testing.T) {
	r := BuildDirectorStyleMentionResolver(context.Background(), nil, 1, "@director/wkw")
	if r == nil {
		t.Fatal("resolver must always be a non-nil fn")
	}
	if r(Mention{Kind: MentionKindDirector, Slug: "wkw"}) != nil {
		t.Errorf("nil-DB resolver should return nil for every mention")
	}
}

func TestBuildDirectorStyleMentionResolver_ReturnsNilResolverWhenUIDNonPositive(t *testing.T) {
	for _, uid := range []int{-1, 0} {
		r := BuildDirectorStyleMentionResolver(context.Background(), nil, uid, "@director/wkw")
		if r == nil {
			t.Fatalf("uid=%d resolver should still be a non-nil fn", uid)
		}
		if r(Mention{Kind: MentionKindDirector, Slug: "wkw"}) != nil {
			t.Errorf("uid=%d resolver should return nil for every mention", uid)
		}
	}
}

func TestBuildDirectorStyleMentionResolver_CharacterOnlyPromptReturnsNilResolver(t *testing.T) {
	// Slug-accumulator filter (Kind != MentionKindDirector) must skip
	// non-director targets, and the `len(slugs) == 0` guard must bail
	// before any DB call. Passing nil DB proves the guard held — a
	// non-nil-skipping path would panic on WithContext.
	r := BuildDirectorStyleMentionResolver(
		context.Background(),
		nil, // nil db — must NOT be reached
		42,
		"@character/林夏 walks past at sunrise",
	)
	if r == nil {
		t.Fatal("resolver must be a non-nil fn even for character-only prompts")
	}
	if r(Mention{Kind: MentionKindDirector, Slug: "anyone"}) != nil {
		t.Errorf("director lookup on character-only prompt should resolve nothing")
	}
}

func TestBuildDirectorStyleMentionResolver_DBBackedHappyPath(t *testing.T) {
	// Seed one Confirmed=true director-style and one draft (Confirmed
	// =false). Build the resolver, assert the confirmed row resolves
	// with both PromptSuffix and NegativePrompt, while the draft row
	// stays excluded — same M4 watermark contract as brand/product.
	db := testutil.NewTestDB(t)

	uid := 42
	confirmed := &model.DirectorStyle{
		UID:            uid,
		Name:           "Wong Kar-wai",
		Slug:           "wong-kar-wai",
		Lang:           "en",
		Status:         model.DirectorStyleStatusActive,
		Confirmed:      true,
		PromptSuffix:   "saturated neon, motion-blur, slow-shutter handheld",
		NegativePrompt: "flat lighting, static framing",
		SourceKind:     model.DirectorStyleSourceManual,
	}
	if err := db.Create(confirmed).Error; err != nil {
		t.Fatalf("seed confirmed director-style: %v", err)
	}
	draft := &model.DirectorStyle{
		UID:        uid,
		Name:       "Christopher Nolan",
		Slug:       "nolan",
		Lang:       "en",
		Status:     model.DirectorStyleStatusActive,
		Confirmed:  false, // workagent draft watermark
		SourceKind: model.DirectorStyleSourceExtracted,
	}
	if err := db.Select("*").Create(draft).Error; err != nil {
		t.Fatalf("seed draft director-style: %v", err)
	}

	r := BuildDirectorStyleMentionResolver(
		context.Background(),
		db,
		uid,
		"shoot it in @director/wong-kar-wai feel, not @director/nolan",
	)

	// Confirmed row resolves.
	hit := r(Mention{Kind: MentionKindDirector, Slug: "wong-kar-wai"})
	if hit == nil {
		t.Fatal("wong-kar-wai (confirmed) should resolve, got nil")
	}
	if hit.Replacement != "Wong Kar-wai" {
		t.Errorf("Replacement = %q, want 'Wong Kar-wai'", hit.Replacement)
	}
	if hit.PromptSuffix != "saturated neon, motion-blur, slow-shutter handheld" {
		t.Errorf("PromptSuffix = %q, want the seeded suffix", hit.PromptSuffix)
	}
	if hit.NegativePrompt != "flat lighting, static framing" {
		t.Errorf("NegativePrompt = %q, want the seeded negative", hit.NegativePrompt)
	}

	// Draft row is excluded — even though it exists and was mentioned.
	if r(Mention{Kind: MentionKindDirector, Slug: "nolan"}) != nil {
		t.Errorf("nolan (draft, Confirmed=false) must NOT resolve — M4 watermark")
	}
}

func TestBuildDirectorStyleMentionResolver_CrossTenantIsolation(t *testing.T) {
	// uid is part of the WHERE clause. A director-style owned by uid
	// =100 must not surface for uid=42 even if the slug matches. Same
	// IDOR posture as brand / product / character — defense in depth.
	db := testutil.NewTestDB(t)

	other := &model.DirectorStyle{
		UID:        100,
		Name:       "Other-user WKW",
		Slug:       "wong-kar-wai",
		Lang:       "en",
		Status:     model.DirectorStyleStatusActive,
		Confirmed:  true,
		SourceKind: model.DirectorStyleSourceManual,
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("seed cross-tenant director-style: %v", err)
	}

	r := BuildDirectorStyleMentionResolver(
		context.Background(), db, 42, "@director/wong-kar-wai at sunrise",
	)
	if r(Mention{Kind: MentionKindDirector, Slug: "wong-kar-wai"}) != nil {
		t.Errorf("cross-tenant director-style must not resolve — IDOR regression")
	}
}
