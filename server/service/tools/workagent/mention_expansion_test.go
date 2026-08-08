package workagent

import (
	"context"
	"strings"
	"testing"

	"server/model"
	"server/utils/testutil"
)

// Chat-side @-mention expansion fixture — pins the agent's
// resolution of `@brand/<slug>` and `@character/<slug>`. The
// canvas-side resolver tests already pin per-kind DB shape
// (Confirmed gates, cross-tenant isolation); here we exercise
// the workagent-side composition + the contract "ResolvedText
// is what the model sees".

const expansionTestUID = 42

func seedBrandRow(t *testing.T, uid int, slug, name string, confirmed bool) {
	t.Helper()
	row := &model.Brand{
		UID:        uid,
		Name:       name,
		Slug:       slug,
		Status:     model.BrandStatusActive,
		Confirmed:  confirmed,
		Lang:       "en",
		SourceKind: model.BrandSourceManual,
	}
	if err := DefaultThreadRepository().db.Select("*").Create(row).Error; err != nil {
		t.Fatalf("seed brand %q: %v", slug, err)
	}
}

func seedCharacterRow(t *testing.T, uid int, slug, name string, confirmed bool) {
	t.Helper()
	row := &model.Character{
		UID:       uid,
		Name:      name,
		Slug:      slug,
		Status:    model.CharacterStatusActive,
		Confirmed: confirmed,
		Lang:      "en",
	}
	if err := DefaultThreadRepository().db.Select("*").Create(row).Error; err != nil {
		t.Fatalf("seed character %q: %v", slug, err)
	}
}

// TestExpandMentionsForChat_NoMentionsShortCircuits — plain
// text with no `@` characters should return identically (the
// DB is never queried; HasMentions skips ahead).
func TestExpandMentionsForChat_NoMentionsShortCircuits(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	in := "Please make a short ad about running shoes"
	got := ExpandMentionsForChat(context.Background(), expansionTestUID, in)
	if got != in {
		t.Errorf("plain text changed: got %q, want %q", got, in)
	}
}

// TestExpandMentionsForChat_ResolvesBrandSlugToName — happy path.
// A `@brand/<slug>` token resolves to the brand's Name.
func TestExpandMentionsForChat_ResolvesBrandSlugToName(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrandRow(t, expansionTestUID, "nike", "Nike", true)

	in := "Use @brand/nike in this spot"
	got := ExpandMentionsForChat(context.Background(), expansionTestUID, in)
	if !strings.Contains(got, "Nike") {
		t.Errorf("expected 'Nike' in output, got %q", got)
	}
	if strings.Contains(got, "@brand/nike") {
		t.Errorf("expected @brand/nike to be expanded out, got %q", got)
	}
}

// TestExpandMentionsForChat_MixesKindsInOneText — character +
// brand in one message both resolve. Pins the composed-resolver
// path.
func TestExpandMentionsForChat_MixesKindsInOneText(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrandRow(t, expansionTestUID, "nike", "Nike", true)
	seedCharacterRow(t, expansionTestUID, "lin-xia", "Lin-Xia", true)

	in := "Have @character/lin-xia wear @brand/nike"
	got := ExpandMentionsForChat(context.Background(), expansionTestUID, in)
	if !strings.Contains(got, "Lin-Xia") || !strings.Contains(got, "Nike") {
		t.Errorf("expected both names in output, got %q", got)
	}
}

// TestExpandMentionsForChat_UnresolvedMentionPassesThrough — a
// slug not in the user's library is left as the raw token. The
// agent still sees something, just not a translated name.
func TestExpandMentionsForChat_UnresolvedMentionPassesThrough(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	in := "Pair @brand/never-existed with our product"
	got := ExpandMentionsForChat(context.Background(), expansionTestUID, in)
	if !strings.Contains(got, "@brand/never-existed") {
		t.Errorf("unresolved mention should pass through verbatim, got %q", got)
	}
}

// TestExpandMentionsForChat_CrossTenantSlugNotResolved — a slug
// that exists but is owned by a different uid resolves to nothing.
// IDOR defence in depth: even if a bug elsewhere shipped a foreign
// slug to this user, the resolver would refuse it.
func TestExpandMentionsForChat_CrossTenantSlugNotResolved(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	// Seed under a DIFFERENT uid than the caller.
	seedBrandRow(t, 99, "nike", "Nike", true)

	in := "Use @brand/nike here"
	got := ExpandMentionsForChat(context.Background(), expansionTestUID, in)
	if !strings.Contains(got, "@brand/nike") {
		t.Errorf("cross-tenant slug should remain unresolved, got %q", got)
	}
}

// TestExpandMentionsForChat_DraftBrandStaysUnexpanded — workagent
// extracts brands as drafts (Confirmed=false) during the M4
// Vocalize protocol; those must not surface in chat expansion
// until the user confirms them. This mirrors the canvas-side
// posture from mention_resolver_brand_test.go.
func TestExpandMentionsForChat_DraftBrandStaysUnexpanded(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrandRow(t, expansionTestUID, "ghost-brand", "Ghost Brand", false /* draft */)

	in := "Promote @brand/ghost-brand to the front"
	got := ExpandMentionsForChat(context.Background(), expansionTestUID, in)
	if !strings.Contains(got, "@brand/ghost-brand") {
		t.Errorf("draft brand should not expand; got %q", got)
	}
}

// TestExpandMentionsForChat_EmptyAndZeroUIDAreNoOps — the cheap
// guards at the top of the function. uid=0 should never reach
// the DB layer; empty text returns immediately.
func TestExpandMentionsForChat_EmptyAndZeroUIDAreNoOps(t *testing.T) {
	if got := ExpandMentionsForChat(context.Background(), expansionTestUID, ""); got != "" {
		t.Errorf("empty text changed: got %q", got)
	}
	if got := ExpandMentionsForChat(context.Background(), 0, "@brand/nike here"); got != "@brand/nike here" {
		t.Errorf("uid=0 must short-circuit: got %q", got)
	}
}
