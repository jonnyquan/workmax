package workagent

// mention_expansion.go — @-mention resolution for the work-agent
// chat surface.
//
// What it does: before a user message is passed to the model, walk
// every `@brand/<slug>`, `@character/<slug>`, `@product/<slug>`,
// `@director/<slug>` token and replace each with the asset's
// human-readable Name. The model then sees "Use Nike, with Lin-Xia"
// instead of "@brand/nike, with @character/lin-xia".
//
// What it deliberately does NOT do (yet):
//
//   - Inject the asset spec into the system prompt. The work-agent
//     preflight already injects brand-spec for *bound* assets via
//     formatBrandSpecXML; ad-hoc mentions could plausibly do the
//     same, but that's a follow-up. v1 of mention expansion is a
//     pure name-substitution pass — same posture as the canvas
//     `ResolvedText` field.
//
//   - Append per-mention prompt suffixes (the canvas
//     ResolvePromptForGenerator path concatenates "blue eyes,
//     athletic" hints onto the prompt). Chat is conversation, not
//     image-generator input — appended hints would read as garbled
//     trailers in the user's message. We only consume the
//     ResolvedText field.
//
//   - Substitute mentions inside tool inputs. The agent re-emits
//     the user's mentions in its own tool_use blocks; mention
//     resolution at the tool boundary is canvas's domain (canvas
//     calls ResolvePromptSafely from the generator path). work-
//     agent's expansion runs once at message ingest.
//
// Reuse — the canvas package already owns the parser
// (ParseMentions / HasMentions / ResolveMentions / Mention*) and
// the four per-kind DB-backed resolvers (BuildBrandMentionResolver,
// BuildCharacterMentionResolver, etc.). These are pure platform-
// data lookups (read w_global_brand / w_global_character / etc.),
// not canvas-specific. Importing them from here avoids duplicating
// 400+ lines of resolver code. The package name "canvas" is
// historical — when (if) a second consumer needs the parser the
// resolver should move to service/mention, but that refactor is
// not in scope for the workagent slice.

import (
	"context"

	"server/globals"
	canvasMentions "server/service/tools/canvas"
)

// ExpandMentionsForChat returns the user's text with every
// resolvable @-mention replaced by the asset's Name. Unresolved
// mentions (slug not in the user's library / soft-deleted /
// cross-tenant) pass through untouched — the agent will simply
// see the raw `@brand/foo` token, same as if the user typed any
// other unknown noun.
//
// Hot-path posture:
//   - Empty text → return unchanged
//   - No `@` in the text → return unchanged (HasMentions skips
//     the DB round-trip)
//   - DB unavailable (globals.GraDBs["system"] nil — every other
//     workagent path also fails-soft here) → return unchanged
//
// The function is always called from the per-turn ingest path
// (one call per user message), so the cost is bounded and
// independent of conversation length.
func ExpandMentionsForChat(ctx context.Context, uid uint, text string) string {
	if text == "" || uid == 0 {
		return text
	}
	if !canvasMentions.HasMentions(text) {
		return text
	}
	db := globals.GraDBs["system"]
	if db == nil {
		return text
	}
	resolver := canvasMentions.ComposeMentionResolvers(
		canvasMentions.BuildCharacterMentionResolver(ctx, db, int(uid), text),
		canvasMentions.BuildBrandMentionResolver(ctx, db, int(uid), text),
		canvasMentions.BuildProductMentionResolver(ctx, db, int(uid), text),
		canvasMentions.BuildDirectorStyleMentionResolver(ctx, db, int(uid), text),
	)
	resolved := canvasMentions.ResolveMentions(text, resolver, ", ")
	return resolved.ResolvedText
}
