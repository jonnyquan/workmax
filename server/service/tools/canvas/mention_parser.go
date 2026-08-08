// Server-owned mention parser and character registry contract. Desktop may
// expand @character/<slug> before hitting the API,
// so this acts as a safety net: if a raw mention slips through (API
// integration, stale client, internal caller), the server expands it
// before the prompt reaches the provider. Grammar and dedup rules remain
// authoritative here and should be projected into Desktop tests.

package canvas

import (
	"regexp"
	"strings"
)

type MentionKind string

const (
	MentionKindCharacter MentionKind = "character"
	MentionKindBrand     MentionKind = "brand"
	MentionKindProduct   MentionKind = "product"
	// MentionKindDirector resolves to a row in w_global_director_style.
	// The mention syntax is `@director/<slug>` (NOT `@director-style/…`
	// — the parser regex restricts the kind segment to [a-zA-Z]+, so a
	// hyphenated kind would not parse; the short form matches the
	// kind segment grammar and reads naturally in prompts).
	MentionKindDirector MentionKind = "director"
)

var knownMentionKinds = map[string]MentionKind{
	"character": MentionKindCharacter,
	"brand":     MentionKindBrand,
	"product":   MentionKindProduct,
	"director":  MentionKindDirector,
}

// Equivalent to the TS MENTION_REGEX. Group 1 captures the preceding
// boundary (start-of-string or whitespace/ideographic-space) so we can
// restore it after a Replace. The stop-char class excludes ASCII and
// CJK punctuation so `@character/林夏，` terminates at `，`.
var mentionRegex = regexp.MustCompile(
	`(^|[\s\x{3000}])@([a-zA-Z]+)/([^\s@,.;!?()\[\]{}"'，。；！？（）【】「」、\x{3000}]+)`,
)

type Mention struct {
	Kind  MentionKind
	Slug  string
	Raw   string
	Start int
	End   int
}

// ResolvedMention is the registry value side of the parse/resolve split.
// A nil *ResolvedMention from the resolver leaves the mention in place
// and reports it as unresolved.
type ResolvedMention struct {
	Replacement    string
	PromptSuffix   string
	NegativePrompt string
}

type MentionResolver func(Mention) *ResolvedMention

type ResolvedPrompt struct {
	ResolvedText               string
	PromptForGenerator         string
	NegativePromptForGenerator string
	Unresolved                 []Mention
}

// ParseMentions returns mentions in source order. Invalid kinds are
// dropped so the literal text stays in-place when the user types
// `@scene/foo` or other unknown namespaces.
func ParseMentions(text string) []Mention {
	if text == "" {
		return nil
	}
	matches := mentionRegex.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]Mention, 0, len(matches))
	for _, m := range matches {
		// m[0:2] full, m[2:4] boundary, m[4:6] kind, m[6:8] slug
		boundary := text[m[2]:m[3]]
		kindRaw := strings.ToLower(text[m[4]:m[5]])
		kind, ok := knownMentionKinds[kindRaw]
		if !ok {
			continue
		}
		slug := text[m[6]:m[7]]
		start := m[0] + len(boundary)
		raw := "@" + string(kind) + "/" + slug
		out = append(out, Mention{
			Kind:  kind,
			Slug:  slug,
			Raw:   raw,
			Start: start,
			End:   start + len(raw),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExtractMentionTargets dedups by (kind, slug) preserving first-seen
// order — mirrors the TS helper used for pre-fetching registry rows.
func ExtractMentionTargets(text string) []Mention {
	mentions := ParseMentions(text)
	if len(mentions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(mentions))
	out := make([]Mention, 0, len(mentions))
	for _, m := range mentions {
		key := string(m.Kind) + "|" + m.Slug
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	return out
}

// ResolveMentions walks mentions back-to-front so a substitution in the
// middle of the string doesn't invalidate the byte offsets recorded for
// earlier matches. Suffix / negative fragments are deduped by
// `${kind}|${slug}|${fragment}` and appended in original mention order.
func ResolveMentions(text string, resolver MentionResolver, joiner string) ResolvedPrompt {
	if joiner == "" {
		joiner = ", "
	}
	mentions := ParseMentions(text)
	if len(mentions) == 0 {
		return ResolvedPrompt{ResolvedText: text, PromptForGenerator: text}
	}

	// Process back-to-front to keep offsets valid for pending substitutions.
	order := make([]int, len(mentions))
	for i := range mentions {
		order[i] = i
	}
	// Reverse walk — mentions are already source-ordered, so iterate from
	// the end.
	seenSuffix := make(map[string]struct{})
	seenNegative := make(map[string]struct{})
	// Collect fragments in reverse iteration, then flip at the end so the
	// appended tail matches the user's reading order.
	var suffixesRev []string
	var negativesRev []string
	var unresolvedRev []Mention

	resolvedText := text
	for i := len(mentions) - 1; i >= 0; i-- {
		mention := mentions[i]
		hit := resolver(mention)
		if hit == nil {
			unresolvedRev = append(unresolvedRev, mention)
			continue
		}
		if hit.Replacement != "" {
			resolvedText = resolvedText[:mention.Start] + hit.Replacement + resolvedText[mention.End:]
		}
		if suffix := strings.TrimSpace(hit.PromptSuffix); suffix != "" {
			key := string(mention.Kind) + "|" + mention.Slug + "|" + suffix
			if _, dup := seenSuffix[key]; !dup {
				seenSuffix[key] = struct{}{}
				suffixesRev = append(suffixesRev, suffix)
			}
		}
		if neg := strings.TrimSpace(hit.NegativePrompt); neg != "" {
			key := string(mention.Kind) + "|" + mention.Slug + "|" + neg
			if _, dup := seenNegative[key]; !dup {
				seenNegative[key] = struct{}{}
				negativesRev = append(negativesRev, neg)
			}
		}
	}

	suffixes := reverseStrings(suffixesRev)
	negatives := reverseStrings(negativesRev)
	unresolved := reverseMentions(unresolvedRev)

	appendedSuffix := strings.Join(suffixes, joiner)
	appendedNegative := strings.Join(negatives, joiner)

	promptForGenerator := resolvedText
	if appendedSuffix != "" {
		promptForGenerator = resolvedText + joiner + appendedSuffix
	}
	return ResolvedPrompt{
		ResolvedText:               resolvedText,
		PromptForGenerator:         promptForGenerator,
		NegativePromptForGenerator: appendedNegative,
		Unresolved:                 unresolved,
	}
}

// ResolvePromptForGenerator layers the user's existing negative prompt
// onto the mention-derived negatives. Matches the TS
// composePromptForGenerator seam so the server and client produce byte-
// identical outputs for the same inputs.
func ResolvePromptForGenerator(
	prompt, negativePrompt string,
	resolver MentionResolver,
	joiner string,
) ResolvedPrompt {
	if joiner == "" {
		joiner = ", "
	}
	resolved := ResolveMentions(prompt, resolver, joiner)
	existingNegative := strings.TrimSpace(negativePrompt)
	parts := make([]string, 0, 2)
	if existingNegative != "" {
		parts = append(parts, existingNegative)
	}
	if resolved.NegativePromptForGenerator != "" {
		parts = append(parts, resolved.NegativePromptForGenerator)
	}
	resolved.NegativePromptForGenerator = strings.Join(parts, joiner)
	return resolved
}

func reverseStrings(in []string) []string {
	if len(in) < 2 {
		if in == nil {
			return nil
		}
		return in
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func reverseMentions(in []Mention) []Mention {
	if len(in) < 2 {
		if in == nil {
			return nil
		}
		return in
	}
	out := make([]Mention, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}
