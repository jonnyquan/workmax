package canvas

import (
	"reflect"
	"testing"
)

func TestParseMentions_Basics(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []Mention
	}{
		{
			name: "empty returns nil",
			text: "",
			want: nil,
		},
		{
			name: "plain text has no mentions",
			text: "a lone pine tree in the rain",
			want: nil,
		},
		{
			name: "leading mention",
			text: "@character/林夏 at dawn",
			want: []Mention{{
				Kind: MentionKindCharacter, Slug: "林夏", Raw: "@character/林夏",
				Start: 0, End: len("@character/林夏"),
			}},
		},
		{
			name: "mention after whitespace",
			text: "close-up of @character/林夏 smiling",
			want: []Mention{{
				Kind: MentionKindCharacter, Slug: "林夏", Raw: "@character/林夏",
				Start: len("close-up of "), End: len("close-up of ") + len("@character/林夏"),
			}},
		},
		{
			name: "rejects email-like",
			text: "contact foo@character/bar.com",
			want: nil,
		},
		{
			name: "rejects unknown kinds",
			text: "@scene/street-night please",
			want: nil,
		},
		{
			name: "lowercases kind",
			text: "@Character/alice",
			want: []Mention{{
				Kind: MentionKindCharacter, Slug: "alice", Raw: "@character/alice",
				Start: 0, End: len("@character/alice"),
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseMentions(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseMentions(%q) =\n  %#v\nwant\n  %#v", tc.text, got, tc.want)
			}
		})
	}
}

func TestParseMentions_StopsAtPunctuation(t *testing.T) {
	got := ParseMentions("hero is @character/alice, calm and bright.")
	if len(got) != 1 {
		t.Fatalf("expected 1 mention, got %d: %#v", len(got), got)
	}
	if got[0].Slug != "alice" {
		t.Errorf("slug = %q, want 'alice'", got[0].Slug)
	}
}

func TestParseMentions_StopsAtCJKPunctuation(t *testing.T) {
	got := ParseMentions("主角是 @character/林夏，在黄昏时候。")
	if len(got) != 1 {
		t.Fatalf("expected 1 mention, got %d: %#v", len(got), got)
	}
	if got[0].Slug != "林夏" {
		t.Errorf("slug = %q, want '林夏'", got[0].Slug)
	}
}

func TestExtractMentionTargets_DedupsPreservingOrder(t *testing.T) {
	got := ExtractMentionTargets("@character/alice and @character/bob then @character/alice again")
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped mentions, got %d: %#v", len(got), got)
	}
	if got[0].Slug != "alice" || got[1].Slug != "bob" {
		t.Errorf("order = [%s, %s], want [alice, bob]", got[0].Slug, got[1].Slug)
	}
}

func mapResolver(m map[string]ResolvedMention) MentionResolver {
	return func(mention Mention) *ResolvedMention {
		key := string(mention.Kind) + "|" + mention.Slug
		if hit, ok := m[key]; ok {
			return &hit
		}
		return nil
	}
}

func TestResolveMentions_SubstitutesAndCollectsSuffixes(t *testing.T) {
	resolver := mapResolver(map[string]ResolvedMention{
		"character|林夏": {
			Replacement:    "Lin Xia",
			PromptSuffix:   "young woman with shoulder-length black hair",
			NegativePrompt: "blonde, western features",
		},
	})
	got := ResolveMentions("close-up of @character/林夏 at dawn", resolver, ", ")
	if got.ResolvedText != "close-up of Lin Xia at dawn" {
		t.Errorf("resolvedText = %q", got.ResolvedText)
	}
	if got.PromptForGenerator != "close-up of Lin Xia at dawn, young woman with shoulder-length black hair" {
		t.Errorf("promptForGenerator = %q", got.PromptForGenerator)
	}
	if got.NegativePromptForGenerator != "blonde, western features" {
		t.Errorf("negativePrompt = %q", got.NegativePromptForGenerator)
	}
	if len(got.Unresolved) != 0 {
		t.Errorf("unresolved = %#v, want []", got.Unresolved)
	}
}

func TestResolveMentions_DedupsAcrossRepeatedMentions(t *testing.T) {
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "Alice", PromptSuffix: "freckles, hazel eyes"},
	})
	got := ResolveMentions("@character/alice walks. then @character/alice turns.", resolver, ", ")
	if got.ResolvedText != "Alice walks. then Alice turns." {
		t.Errorf("resolvedText = %q", got.ResolvedText)
	}
	if got.PromptForGenerator != "Alice walks. then Alice turns., freckles, hazel eyes" {
		t.Errorf("promptForGenerator = %q", got.PromptForGenerator)
	}
}

func TestResolveMentions_PreservesAppendOrderAcrossKinds(t *testing.T) {
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "Alice", PromptSuffix: "freckles"},
		"character|bob":   {Replacement: "Bob", PromptSuffix: "tall beard"},
	})
	got := ResolveMentions("@character/alice and @character/bob", resolver, ", ")
	want := "Alice and Bob, freckles, tall beard"
	if got.PromptForGenerator != want {
		t.Errorf("promptForGenerator = %q\nwant                        %q", got.PromptForGenerator, want)
	}
}

func TestResolveMentions_LeavesUnresolvedInPlaceAndReports(t *testing.T) {
	resolver := mapResolver(nil)
	got := ResolveMentions("scene with @character/ghost", resolver, ", ")
	if got.ResolvedText != "scene with @character/ghost" {
		t.Errorf("resolvedText = %q", got.ResolvedText)
	}
	if got.PromptForGenerator != "scene with @character/ghost" {
		t.Errorf("promptForGenerator = %q", got.PromptForGenerator)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0].Slug != "ghost" {
		t.Errorf("unresolved = %#v, want 1 ghost", got.Unresolved)
	}
}

func TestResolveMentions_NoMentionsReturnsInputUntouched(t *testing.T) {
	got := ResolveMentions("plain prompt with no mentions", mapResolver(nil), ", ")
	if got.ResolvedText != "plain prompt with no mentions" {
		t.Errorf("resolvedText = %q", got.ResolvedText)
	}
	if got.PromptForGenerator != "plain prompt with no mentions" {
		t.Errorf("promptForGenerator = %q", got.PromptForGenerator)
	}
	if got.NegativePromptForGenerator != "" {
		t.Errorf("negative = %q, want empty", got.NegativePromptForGenerator)
	}
}

func TestResolvePromptForGenerator_ConcatenatesExistingNegative(t *testing.T) {
	resolver := mapResolver(map[string]ResolvedMention{
		"character|alice": {Replacement: "Alice", NegativePrompt: "blurry"},
	})
	got := ResolvePromptForGenerator("portrait of @character/alice", "lowres, deformed", resolver, ", ")
	if got.NegativePromptForGenerator != "lowres, deformed, blurry" {
		t.Errorf("negativePrompt = %q", got.NegativePromptForGenerator)
	}
}

func TestResolvePromptForGenerator_UntouchedWhenNoMentions(t *testing.T) {
	got := ResolvePromptForGenerator("plain landscape", "watermark", mapResolver(nil), ", ")
	if got.NegativePromptForGenerator != "watermark" {
		t.Errorf("negativePrompt = %q", got.NegativePromptForGenerator)
	}
	if got.PromptForGenerator != "plain landscape" {
		t.Errorf("promptForGenerator = %q", got.PromptForGenerator)
	}
}
