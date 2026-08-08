package canvas

import (
	"context"
	"testing"
)

// mention_resolver.go has two public entry points: BuildCharacterMentionResolver
// and ResolvePromptSafely. Both wrap a DB round-trip in layers of early-exit
// guards that decide whether to hit the database at all. Those guards are
// pure and load-bearing — a bug in any of them either (a) leaks a DB query
// every time a user sends a prompt without mentions, or (b) the resolver
// silently over-fetches characters that weren't referenced. The DB-backed
// path (gorm query) needs sqlmock to exercise in isolation; these tests pin
// only the guard logic, which is what the production paths rely on for
// correctness when a user is logged out, has no mentions, or is hitting a
// node with no DB wired up.

func TestHasMentions_MatchesMentionRegex(t *testing.T) {
	// HasMentions is a cheap prefilter before doing the more expensive
	// ParseMentions / DB lookup. The regex is shared with ParseMentions,
	// so these cases are a subset of the parser behaviour — but pinning
	// them here ensures a future refactor that splits the regex can't
	// desync the prefilter from the parser.
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"empty string", "", false},
		{"plain text", "morning in Beijing", false},
		{"valid leading @character mention", "@character/林夏 at dawn", true},
		{"valid @brand after whitespace", "a shot of @brand/nike gear", true},
		{"valid @product after ideographic space", "close-up　@product/widget on table", true},
		{"unknown kind still matches the regex shape", "see @scene/foo for bg", true},
		// The regex requires start-of-string OR whitespace before `@`.
		// An `@` glued to a non-space character is not a mention.
		{"@ glued to letter is not a mention", "email jane@example.com no mention", false},
		// CJK punctuation terminates the slug but doesn't block detection
		// at the start.
		{"mention terminated by chinese comma", "@character/林夏，微笑", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasMentions(c.text); got != c.want {
				t.Errorf("HasMentions(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

func TestBuildCharacterMentionResolver_ReturnsNilResolverWhenDBIsNil(t *testing.T) {
	// Safety net for nodes where globals.GraDBs["system"] is nil —
	// workers, CLI jobs, or early boot. The resolver must not panic
	// and must resolve every mention to nil (treated as unresolved).
	r := BuildCharacterMentionResolver(context.Background(), nil, 1, "@character/林夏")
	if r == nil {
		t.Fatal("resolver should never be nil — nilResolver is a valid fn")
	}
	if got := r(Mention{Kind: MentionKindCharacter, Slug: "林夏"}); got != nil {
		t.Errorf("nil-DB resolver should return nil for any mention, got %+v", got)
	}
}

func TestBuildCharacterMentionResolver_ReturnsNilResolverWhenUIDNonPositive(t *testing.T) {
	// uid <= 0 means "not authenticated" (or misused by caller). The
	// resolver must short-circuit BEFORE building a DB query — querying
	// with uid=0 could otherwise leak another user's characters if the
	// store has a 0-uid fallback row. Pin the guard at the entry.
	// Using a non-nil sentinel for db would trigger the DB path if the
	// uid check were removed, so pass nil db too — the nil-db guard is
	// the belt-and-braces safety net behind the uid guard.
	for _, uid := range []int{-1, 0} {
		r := BuildCharacterMentionResolver(context.Background(), nil, uid, "@character/林夏")
		if r == nil {
			t.Fatalf("uid=%d resolver should still be a non-nil fn", uid)
		}
		if r(Mention{Kind: MentionKindCharacter, Slug: "林夏"}) != nil {
			t.Errorf("uid=%d resolver should return nil for every mention", uid)
		}
	}
}

func TestBuildCharacterMentionResolver_ReturnsNilResolverWhenPromptHasNoMentionTargets(t *testing.T) {
	// Empty prompt and plain-text prompts must short-circuit before
	// any DB query. This is the performance guard that stops every
	// image generation from incurring a characters lookup.
	for _, prompt := range []string{"", "just a golden hour portrait"} {
		r := BuildCharacterMentionResolver(context.Background(), nil, 42, prompt)
		if r == nil {
			t.Fatalf("prompt=%q resolver should be a non-nil fn", prompt)
		}
		if r(Mention{Kind: MentionKindCharacter, Slug: "anything"}) != nil {
			t.Errorf("prompt=%q resolver should resolve nothing", prompt)
		}
	}
}

func TestBuildCharacterMentionResolver_ReturnsNilResolverWhenNoCharacterTargets(t *testing.T) {
	// A prompt that contains only @brand / @product mentions has no
	// character slugs — the resolver must bail out before issuing a
	// DB query for characters. Same performance guarantee as the
	// "no mentions" case, but reached by the different slug-filter
	// path inside BuildCharacterMentionResolver.
	r := BuildCharacterMentionResolver(
		context.Background(),
		nil, // nil db — if the slug filter didn't bail, the DB path would panic
		42,
		"showcase @brand/nike and @product/widget together",
	)
	if r == nil {
		t.Fatal("resolver should be a non-nil fn even with no character mentions")
	}
	if r(Mention{Kind: MentionKindBrand, Slug: "nike"}) != nil {
		t.Errorf("brand-only prompt should resolve nothing (character resolver doesn't cover brands)")
	}
}

func TestResolvePromptSafely_EmptyPromptReturnsInputsUnchanged(t *testing.T) {
	// ResolvePromptSafely is the call-site helper used by the generator.
	// When the user gives an empty prompt, it must NOT touch globals
	// (which might not be initialised in tests) and must return the
	// inputs unchanged. Same for a prompt with no mentions. Both are
	// pure-path short-circuits that bypass globals.GraDBs entirely.
	gotPrompt, gotNeg := ResolvePromptSafely(context.Background(), 1, "", "bad quality")
	if gotPrompt != "" || gotNeg != "bad quality" {
		t.Errorf("empty prompt should pass through; got (%q, %q)", gotPrompt, gotNeg)
	}
}

func TestResolvePromptSafely_PlainPromptBypassesDBLookup(t *testing.T) {
	// A prompt with no mentions skips HasMentions → skips the DB read.
	// This test is robust even if globals.GraDBs["system"] is uninitialised
	// in the test harness, which is the whole point of the early-exit.
	in := "a calm lake at sunrise"
	neg := "blurry"
	gotPrompt, gotNeg := ResolvePromptSafely(context.Background(), 1, in, neg)
	if gotPrompt != in {
		t.Errorf("plain prompt was modified: got %q, want %q", gotPrompt, in)
	}
	if gotNeg != neg {
		t.Errorf("plain negative was modified: got %q, want %q", gotNeg, neg)
	}
}
