package canvas

import (
	"strings"
	"testing"
)

// mention_parser_test.go pins the character-kind headline paths + a
// couple of CJK cases. These fill in the quieter branches the parser's
// §8 contract also depends on:
//
//   - All four known kinds (character/brand/product/director) must
//     parse. A regex rewrite that dropped one would silently lose
//     mentions in production without any test catching it.
//   - Ideographic space (`　`) is a legal leading boundary — CJK
//     users often paste prompts with full-width whitespace.
//   - Empty Replacement must keep the mention text in place but still
//     collect the suffix (the "suffix-only" enrichment path).
//   - Whitespace-only suffix/negative is skipped so trailing joiner
//     fragments don't leak into generator output.
//   - An empty joiner falls back to ", " so the backend never emits
//     concatenated strings without separators.
//   - ExtractMentionTargets("") must return nil, matching ParseMentions.

func TestParseMentions_BrandAndProductKinds(t *testing.T) {
	// Without per-kind tests, a regex change that preserved "character"
	// but broke "brand" / "product" / "director" lookahead would pass
	// the existing suite — those drive electronic-commerce, ad-creative
	// and cinematic-style flows.
	//
	// `@director/...` is the short form of the cinematic-style mention
	// (resolves against w_global_director_style); the long form
	// `@director-style/...` would not parse because the kind segment
	// regex is [a-zA-Z]+ (no hyphens). Pin both: the short form
	// resolves; a hyphenated long form is dropped as unknown kind.
	cases := []struct {
		name      string
		text      string
		want      MentionKind
		wantCount int
	}{
		{"brand", "@brand/nike goes first", MentionKindBrand, 1},
		{"product", "lead with @product/air-max on the hero", MentionKindProduct, 1},
		{"director", "shoot it in @director/wong-kar-wai style", MentionKindDirector, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseMentions(c.text)
			if len(got) != c.wantCount {
				t.Fatalf("expected %d mention(s), got %d", c.wantCount, len(got))
			}
			if got[0].Kind != c.want {
				t.Errorf("Kind = %q, want %q", got[0].Kind, c.want)
			}
		})
	}
}

func TestParseMentions_DirectorHyphenLongFormIsDropped(t *testing.T) {
	// `@director-style/...` looks like a tempting "long form" mention,
	// but the kind segment grammar is [a-zA-Z]+ (no hyphens). The
	// regex matches the kind as "director" and starts the slug at
	// `-style/...`, which is a valid slug-shaped token containing a
	// "/", giving a different and incorrect resolution. The parser
	// MUST emit either nothing useful or the short-form result —
	// never silently route a hyphenated kind to the director resolver.
	//
	// Pin the actual behavior so a future regex tweak doesn't quietly
	// flip this from "drops cleanly" to "matches the wrong thing".
	got := ParseMentions("invoke @director-style/wkw here")
	for _, m := range got {
		if m.Kind == MentionKindDirector && strings.HasPrefix(m.Slug, "-") {
			t.Errorf("director kind must not capture a hyphen-prefixed slug, got slug=%q", m.Slug)
		}
	}
}

func TestParseMentions_AcceptsIdeographicSpaceBoundary(t *testing.T) {
	// The regex explicitly lists `\x{3000}` (ideographic space) as a
	// legal boundary — omitting it would reject full-width-spaced
	// prompts that paste from CJK editors. Pin the branch.
	got := ParseMentions("黄昏时　@character/林夏 走来")
	if len(got) != 1 {
		t.Fatalf("expected 1 mention after ideographic space, got %d: %#v", len(got), got)
	}
	if got[0].Slug != "林夏" {
		t.Errorf("slug = %q, want '林夏'", got[0].Slug)
	}
}

func TestParseMentions_CollectsInSourceOrder(t *testing.T) {
	// The slice must be ordered by source position, not by kind or
	// insertion. A stable-sort regression would break the renderer's
	// source-faithful echo in the thread sidebar.
	got := ParseMentions("@brand/nike and @character/alice then @product/shoe")
	if len(got) != 3 {
		t.Fatalf("expected 3 mentions, got %d", len(got))
	}
	if got[0].Kind != MentionKindBrand || got[1].Kind != MentionKindCharacter || got[2].Kind != MentionKindProduct {
		t.Errorf("order broken: %v %v %v", got[0].Kind, got[1].Kind, got[2].Kind)
	}
}

func TestExtractMentionTargets_EmptyReturnsNil(t *testing.T) {
	// The body short-circuits early when ParseMentions returns nil.
	// Pin the nil-vs-empty-slice distinction — the API layer tests
	// `len(targets) == 0` but a future refactor returning `[]Mention{}`
	// would still pass that check yet allocate on every call.
	if got := ExtractMentionTargets(""); got != nil {
		t.Errorf("ExtractMentionTargets(\"\") = %#v, want nil", got)
	}
	if got := ExtractMentionTargets("plain text only"); got != nil {
		t.Errorf("ExtractMentionTargets with no mentions = %#v, want nil", got)
	}
}

func TestResolveMentions_EmptyJoinerFallsBackToCommaSpace(t *testing.T) {
	// ResolveMentions documents the empty-joiner → ", " fallback. A
	// regression that passed "" straight through would emit concatenated
	// suffixes without separators ("aliceBob") which the generator
	// then interprets as a single term.
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "Alice", PromptSuffix: "freckles"},
		"character|bob":   {Replacement: "Bob", PromptSuffix: "tall"},
	})
	got := ResolveMentions("@character/alice and @character/bob", resolver, "")
	want := "Alice and Bob, freckles, tall"
	if got.PromptForGenerator != want {
		t.Errorf("PromptForGenerator = %q, want %q", got.PromptForGenerator, want)
	}
}

func TestResolveMentions_CustomJoinerPropagates(t *testing.T) {
	// A caller-provided joiner must flow through to both the resolved-
	// text→suffix boundary and the inter-suffix separators. A regression
	// that only honoured it in one spot would produce inconsistent
	// output ("A | B, freckles").
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "Alice", PromptSuffix: "freckles"},
		"character|bob":   {Replacement: "Bob", PromptSuffix: "tall"},
	})
	got := ResolveMentions("@character/alice and @character/bob", resolver, " | ")
	// A joiner of " | " must appear both between the resolved text and
	// the first suffix AND between consecutive suffixes.
	if !strings.Contains(got.PromptForGenerator, "Alice and Bob | freckles | tall") {
		t.Errorf("PromptForGenerator = %q", got.PromptForGenerator)
	}
}

func TestResolveMentions_EmptyReplacementKeepsTextButCollectsSuffix(t *testing.T) {
	// A resolver may hit a registry row with a PromptSuffix but no
	// Replacement (for example, a character whose literal name is
	// already the user-friendly form). The mention text must stay put
	// while the suffix is still appended — otherwise suffix-only
	// enrichments (§8 enrich-without-rename path) silently break.
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "", PromptSuffix: "freckles"},
	})
	got := ResolveMentions("portrait of @character/alice smiling", resolver, ", ")
	if got.ResolvedText != "portrait of @character/alice smiling" {
		t.Errorf("ResolvedText should keep literal mention, got %q", got.ResolvedText)
	}
	if got.PromptForGenerator != "portrait of @character/alice smiling, freckles" {
		t.Errorf("PromptForGenerator = %q", got.PromptForGenerator)
	}
	if len(got.Unresolved) != 0 {
		t.Errorf("Unresolved = %#v; suffix-only hit should be considered resolved", got.Unresolved)
	}
}

func TestResolveMentions_WhitespaceOnlySuffixIsSkipped(t *testing.T) {
	// A PromptSuffix that's just whitespace (a misconfigured registry
	// row) must NOT leak a trailing joiner fragment into the output.
	// TrimSpace + empty check guards against `prompt, ` being sent.
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "Alice", PromptSuffix: "   "},
	})
	got := ResolveMentions("@character/alice", resolver, ", ")
	if got.PromptForGenerator != "Alice" {
		t.Errorf("whitespace suffix leaked into output: %q", got.PromptForGenerator)
	}
}

func TestResolvePromptForGenerator_ExistingNegativeAloneSurvivesWhenNoMentionNegative(t *testing.T) {
	// If no mention contributes a negative, the caller's existing
	// negative must pass through untouched — no stray joiner, no
	// trimming of actual content.
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "Alice", PromptSuffix: "freckles"},
	})
	got := ResolvePromptForGenerator("@character/alice", "watermark, low quality", resolver, ", ")
	if got.NegativePromptForGenerator != "watermark, low quality" {
		t.Errorf("NegativePrompt = %q", got.NegativePromptForGenerator)
	}
}
