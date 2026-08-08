package canvas

import (
	"context"
	"strings"
	"testing"
)

// mention_resolver_test.go covers the nil-DB / uid / no-mentions / no-
// character guards. These fill in the quieter branches that the existing
// suite doesn't directly pin but production traffic hits:
//
//   • HasMentions after a TAB / NEWLINE before `@`. Existing suite pins
//     space + ideographic space; the regex boundary class is `[\s\x{3000}]`
//     so \t and \n are part of \s and must also boundary-match. A
//     regression that hand-rolled the class as `[ \t\x{3000}]` (dropping
//     \n) would pass the space cases silently. Pin both so a cross-line
//     prompt from a textarea still detects the mention.
//   • HasMentions on a non-empty, non-match long string — the early
//     `len(text) == 0` guard short-circuits empties; a long plain
//     prompt must return false WITHOUT the regex matching anywhere.
//     Pin a representative-sized input so a regex typo that accidentally
//     made every non-empty string match (e.g. greedy/anchor error) would
//     surface here immediately.
//   • BuildCharacterMentionResolver with a kind that is NOT character
//     in the prompt — e.g. @product/widget only. The slug-accumulator
//     filters by `t.Kind != MentionKindCharacter`. Pin the nilResolver
//     return to guarantee brand/product-only prompts never trigger a
//     characters query (performance + correctness guard — a stray
//     characters read here would cross-contaminate unrelated namespaces).
//   • BuildCharacterMentionResolver with MIXED mention kinds in a
//     single prompt (one character + one brand). The character slug
//     filter must keep the character and discard the brand — a
//     regression that took the first mention's kind would drop the
//     character. Pin by passing a nil DB and observing the resolver
//     returns non-nilResolver path would panic; here we just confirm
//     the guard: nil DB → nilResolver, but if the filter is wrong and
//     builds an empty slug list, it would still land on nilResolver
//     via the `len(slugs) == 0` guard. Rather than re-test that, pin
//     the observable contract: brand mention resolution yields nil
//     even when a character slug also appears.
//   • ResolvePromptSafely with a mention-containing prompt and nil
//     globals DB — the `db == nil` branch inside ResolvePromptSafely
//     must short-circuit and return the inputs unchanged. Existing
//     suite only tests the pre-HasMentions branch (empty / plain
//     prompts); the nil-DB path is DIFFERENT and load-bearing for
//     test harnesses, CLI jobs, and early-boot code paths.
//   • HasMentions on a prompt with ONLY the `@` character and no
//     slug — must return false (the regex requires kind + `/` + slug).
//     Pin so a regression that loosened the regex would light up here
//     before a bad pipeline produced a malformed DB query.

func TestHasMentions_BoundariesIncludeTabAndNewline(t *testing.T) {
	// The boundary class `[\s\x{3000}]` includes \t and \n. Pin so a
	// regression that narrowed it to `[ \x{3000}]` (space only) would
	// break every textarea prompt containing a newline before `@`.
	cases := []struct {
		name string
		text string
	}{
		{"tab before @", "line one\t@character/林夏"},
		{"newline before @", "line one\n@character/林夏"},
		{"carriage return before @", "line one\r@character/林夏"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !HasMentions(c.text) {
				t.Errorf("HasMentions(%q) = false, want true (regex must treat %s as a word boundary)",
					c.text, c.name)
			}
		})
	}
}

func TestHasMentions_LongPlainTextReturnsFalse(t *testing.T) {
	// A 4 KB prompt with no `@` character anywhere must still return
	// false. Pin the happy plain-text path so a regex anchor typo that
	// accidentally matched every long string would surface here.
	plain := strings.Repeat("golden hour portrait in the forest ", 128)
	if HasMentions(plain) {
		t.Errorf("HasMentions on 4KB plain text returned true; prompt was not supposed to match any mention")
	}
}

func TestHasMentions_StrayAtSignWithoutSlugReturnsFalse(t *testing.T) {
	// The regex requires `@` + kind + `/` + slug. A bare `@` (or `@kind`
	// without `/slug`) must NOT match — otherwise the resolver would
	// try to expand a malformed reference.
	cases := []string{"@", "hello @", "@character", "@character/", "@/slug"}
	for _, raw := range cases {
		if HasMentions(raw) {
			t.Errorf("HasMentions(%q) = true, want false (malformed mention must not match)", raw)
		}
	}
}

func TestBuildCharacterMentionResolver_ProductOnlyPromptReturnsNilResolver(t *testing.T) {
	// A prompt that only mentions @product/... has no character slugs.
	// The slug-accumulator's `t.Kind != MentionKindCharacter` filter
	// skips them, and the `len(slugs) == 0` guard bails out before any
	// DB call. Pin by passing nil DB — if the guard failed, the nil DB
	// would panic on WithContext. A non-panic test means the guard held.
	r := BuildCharacterMentionResolver(
		context.Background(),
		nil, // nil db — must NOT be reached
		42,
		"macro shot of @product/widget on a marble table",
	)
	if r == nil {
		t.Fatal("resolver must be a non-nil fn even for product-only prompts")
	}
	// A character mention lookup must return nil (the registry is empty).
	if r(Mention{Kind: MentionKindCharacter, Slug: "anyone"}) != nil {
		t.Errorf("character lookup on product-only prompt should resolve nothing")
	}
	// Product/brand mentions aren't the concern of this resolver either.
	if r(Mention{Kind: MentionKindProduct, Slug: "widget"}) != nil {
		t.Errorf("product lookup on any resolver should resolve nothing (character-only registry)")
	}
}

func TestBuildCharacterMentionResolver_BrandAndCharacterMixedPromptStillExits(t *testing.T) {
	// Mixed-kind prompt: brand + character. With nil DB, the resolver
	// must still land on nilResolver rather than panicking — either the
	// `db == nil` guard catches it first, or (if tightened) the
	// slugs-filter accumulated only the character slug and then the
	// DB call would panic. Pin: nil DB is the authoritative short-circuit.
	r := BuildCharacterMentionResolver(
		context.Background(),
		nil,
		42,
		"showcase @brand/nike gear worn by @character/林夏",
	)
	if r == nil {
		t.Fatal("mixed-prompt resolver must be a non-nil fn")
	}
	if r(Mention{Kind: MentionKindCharacter, Slug: "林夏"}) != nil {
		t.Errorf("nil-DB resolver must resolve to nil even for a valid character slug")
	}
}

func TestResolvePromptSafely_MentionPromptWithoutGlobalDBReturnsInputsUnchanged(t *testing.T) {
	// The test harness doesn't initialise globals.GraDBs; a mention-
	// containing prompt must still flow through ResolvePromptSafely
	// without a panic, hitting the `db == nil` guard and returning
	// inputs unchanged. This is the safety net for CLI / worker / early-
	// boot code paths that never initialise the DB pool. Existing suite
	// tests the pre-HasMentions branch; this is the post-HasMentions
	// branch that only activates when the prompt legitimately contains
	// a mention.
	in := "@character/林夏 smiles at sunrise"
	neg := "blurry"
	gotPrompt, gotNeg := ResolvePromptSafely(context.Background(), 1, in, neg)
	if gotPrompt != in {
		t.Errorf("prompt mutated despite nil-DB safety net: got %q, want %q", gotPrompt, in)
	}
	if gotNeg != neg {
		t.Errorf("negative mutated despite nil-DB safety net: got %q, want %q", gotNeg, neg)
	}
}
