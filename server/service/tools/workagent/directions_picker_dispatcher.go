// Package workagent — directions_picker_dispatcher.go.
//
// Sprint-B M2 server-side. Detects the conditions under which the
// runtime should emit a directions_picker block instead of calling
// the SDK, builds the SSE block payload, and exposes a tiny
// imperative API the API handler (agent_api.go) can call after the
// question_form short-circuit.
//
// Trigger contract:
//
//   - skill declares directions_fallback.enabled = true
//   - feature flag IsDirectionsFallbackEnabledFor(uid) is on
//   - thread has no <brand-spec> in scope (no brand_assets row +
//     no brand-spec.md in workdir; today we approximate via the
//     "no PersistedBrandRef metadata" heuristic — caller passes
//     HasBrandContext)
//   - thread has no direction picked yet (FindLatestByMetadataKind
//     "visual_direction_selected" returns nil)
//   - this turn isn't already a directions_form_result follow-up
package workagent

import (
	"encoding/json"
	"fmt"

	"server/config"
	"server/globals"
	"server/service/tools/workagent/i18n"
	"server/service/tools/workagent/skills"
)

// DirectionsPickerDispatcher mirrors the QuestionFormDispatcher
// shape — stateless past construction, safe to share across
// goroutines. Built once via NewDirectionsPickerDispatcher and
// exposed as a package-level singleton.
type DirectionsPickerDispatcher struct {
	resolver *i18n.Resolver
}

// NewDirectionsPickerDispatcher returns a dispatcher with the
// supplied locale resolver. Tests can swap by passing a custom
// resolver via i18n.Load().
func NewDirectionsPickerDispatcher(resolver *i18n.Resolver) *DirectionsPickerDispatcher {
	return &DirectionsPickerDispatcher{resolver: resolver}
}

// DefaultDirectionsPickerDispatcher is the singleton wired to
// i18n.Default(). API handlers consume this; tests construct their
// own via NewDirectionsPickerDispatcher.
var DefaultDirectionsPickerDispatcher = NewDirectionsPickerDispatcher(i18n.Default())

// DirectionsDispatchInput bundles the per-turn state. UID for the
// feature gate, ThreadID for the "already-picked?" lookup,
// SkillBundle for the directions_fallback config, HasBrandContext
// for the "skip when brand-spec is active" check.
type DirectionsDispatchInput struct {
	UID             uint
	ThreadID        uint
	SkillBundle     *skills.SkillBundle
	Locale          string
	HasBrandContext bool // true when brand-spec.md / brand_assets row exists
	TempMessageID   string
	SendEvent       SendEventFunc // reuses the QuestionForm dispatcher's adapter
}

// DirectionsDispatchResult mirrors the question-form result shape
// so the API handler reads consistently — same Emitted bool +
// Reason trace.
type DirectionsDispatchResult struct {
	Emitted bool
	Reason  string
}

// Dispatch is the single entry point. Returns Emitted=true when
// the dispatcher emitted SSE events and the API handler should
// release credits + return. Otherwise the handler continues to the
// SDK call.
//
// The trigger condition AND-chain is intentionally explicit so a
// future feature flag flip behaves predictably:
//
//  1. Feature flag on for this UID
//  2. Skill declares directions_fallback + Enabled=true
//  3. No brand context (caller supplies HasBrandContext)
//  4. No prior direction selection on this thread
//
// Any false → DispatchResult{Emitted:false, Reason:"..."} and
// the API handler proceeds to the SDK call as normal.
func (d *DirectionsPickerDispatcher) Dispatch(in DirectionsDispatchInput) DirectionsDispatchResult {
	if !config.GetWorkAgentFeatures().IsDirectionsFallbackEnabledFor(in.UID) {
		return DirectionsDispatchResult{Reason: "feature disabled or uid not in whitelist"}
	}
	if in.SkillBundle == nil || in.SkillBundle.DirectionsFallback == nil {
		return DirectionsDispatchResult{Reason: "skill has no directions_fallback configured"}
	}
	if !in.SkillBundle.DirectionsFallback.Enabled {
		return DirectionsDispatchResult{Reason: "skill directions_fallback.enabled=false"}
	}
	if in.HasBrandContext {
		return DirectionsDispatchResult{Reason: "brand context active — skip fallback picker"}
	}
	if in.ThreadID > 0 && hasPriorDirectionSelection(in.ThreadID) {
		return DirectionsDispatchResult{Reason: "direction already selected for this thread"}
	}

	vd, err := skills.LoadVisualDirections()
	if err != nil || vd == nil {
		globals.Warn(fmt.Sprintf("[DirectionsPicker] load yaml failed: %v", err))
		return DirectionsDispatchResult{Reason: "visual-directions.yaml load failed"}
	}
	if len(vd.Fallback5) == 0 {
		return DirectionsDispatchResult{Reason: "no directions in fallback_5"}
	}

	resolvedDirections := d.resolveDirections(vd.Fallback5, in.Locale)
	if len(resolvedDirections) == 0 {
		globals.Warn("[DirectionsPicker] all directions failed to resolve")
		return DirectionsDispatchResult{Reason: "no directions resolved"}
	}

	skipLabel := ""
	if key := in.SkillBundle.DirectionsFallback.SkipKey; key != "" {
		skipLabel = d.resolver.Resolve(in.Locale, key)
	} else {
		skipLabel = d.resolver.Resolve(in.Locale, "form.skip")
	}

	// Block schema is the Server-owned DirectionsPicker wire contract consumed
	// by the bundled Desktop renderer.
	// skip_button carries the localized Skip CTA — previously this
	// label was being hijacked into the `title` slot because the
	// schema lacked a skip_button field; the FE renderer then
	// displayed "Skip" as the picker heading. Caught by the
	// post-Stage-D wire-shape audit (2026-05-12).
	blockPayload := map[string]interface{}{
		"type":      "directions_picker",
		"picker_id": "fallback_5",
		"schema": map[string]interface{}{
			"directions":  resolvedDirections,
			"skip_button": map[string]string{"label": skipLabel},
		},
	}
	blockJSON, err := json.Marshal(blockPayload)
	if err != nil {
		globals.Error(fmt.Sprintf("[DirectionsPicker] marshal block failed: %v", err))
		return DirectionsDispatchResult{Reason: "block marshal error"}
	}

	// stop_reason=directions_emitted mirrors question_form_dispatcher's
	// "form_emitted" — see that file's comment for the audited
	// contract. tl;dr: FE doesn't branch on stop_reason today
	// (block.type carries the signal), but the field stays on the
	// wire for server-side log parsing + FE forwards-compat.
	resultPayload := map[string]interface{}{
		"type":        "result",
		"subtype":     "success",
		"is_error":    false,
		"stop_reason": "directions_emitted",
		"kind":        "directions_picker",
		"picker_id":   "fallback_5",
	}
	resultJSON, _ := json.Marshal(resultPayload)

	in.SendEvent("block", in.TempMessageID, 0, json.RawMessage(blockJSON), nil)
	in.SendEvent("done", in.TempMessageID, 0, nil, json.RawMessage(resultJSON))

	globals.Info(fmt.Sprintf("[DirectionsPicker] short-circuit: skill=%s uid=%d locale=%s directions=%d",
		in.SkillBundle.Name, in.UID, in.Locale, len(resolvedDirections)))

	return DirectionsDispatchResult{
		Emitted: true,
		Reason:  fmt.Sprintf("skill=%s, %d directions emitted", in.SkillBundle.Name, len(resolvedDirections)),
	}
}

// resolveDirections converts VisualDirection structs into the
// wire-shape the frontend's normalizer expects. Each label /
// description is resolved against the active locale.
//
// Drops malformed entries silently (matches the frontend
// normalizer's defensive parsing one-to-one).
func (d *DirectionsPickerDispatcher) resolveDirections(raw []skills.VisualDirection, locale string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(raw))
	for _, dir := range raw {
		if dir.ID == "" {
			continue
		}
		entry := map[string]interface{}{
			"id":    dir.ID,
			"label": resolveOrFallback(d.resolver, locale, dir.LabelKey, dir.ID),
			"palette": map[string]string{
				"bg":     dir.Palette.Bg.Hex,
				"fg":     dir.Palette.Fg.Hex,
				"accent": dir.Palette.Accent.Hex,
				"muted":  dir.Palette.Muted.Hex,
			},
		}
		if dir.DescriptionKey != "" {
			if desc := d.resolver.Resolve(locale, dir.DescriptionKey); desc != "" && desc != dir.DescriptionKey {
				entry["description"] = desc
			}
		}
		if dir.FontStack.Display != "" || dir.FontStack.Body != "" {
			entry["font_stack"] = map[string]string{
				"display": dir.FontStack.Display,
				"body":    dir.FontStack.Body,
			}
		}
		if dir.LayoutPosture != "" {
			entry["layout_posture"] = dir.LayoutPosture
		}
		// Sample MP4 + thumbnail — Sprint-C M2 deliverable. Both are
		// optional and skip cleanly when empty so the DirectionsPicker
		// renders the palette card without the hover preview / poster.
		// The Desktop renderer reads these snake_case wire keys.
		if dir.SampleVideoURL != "" {
			entry["sample_video_url"] = dir.SampleVideoURL
		}
		if dir.ThumbnailURL != "" {
			entry["thumbnail_url"] = dir.ThumbnailURL
		}
		out = append(out, entry)
	}
	return out
}

// resolveOrFallback resolves a key against the locale, returning
// fallback when the key isn't found (the resolver returns the key
// verbatim on miss). Local helper so the dispatcher doesn't depend
// on globals for warn-once tracking; the resolver itself does that.
func resolveOrFallback(r *i18n.Resolver, locale, key, fallback string) string {
	got := r.Resolve(locale, key)
	if got == "" || got == key {
		return fallback
	}
	return got
}

// hasPriorDirectionSelection returns true when the thread already
// carries a visual_direction_selected metadata row. Used to suppress
// the picker on subsequent turns once the user has picked.
func hasPriorDirectionSelection(threadID uint) bool {
	repo := DefaultMessageRepository()
	if repo == nil {
		return false
	}
	msg, err := repo.FindMostRecentByMetadataKind(threadID, "visual_direction_selected")
	if err != nil {
		return false
	}
	return msg != nil
}
