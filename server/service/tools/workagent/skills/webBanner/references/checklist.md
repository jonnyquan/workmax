# webBanner Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS (block emit)

### P0-1 · platform_compliance
- detector: `platform_policy_check`
- description: Banner size, file format, max file-size note, destination consistency, and non-deceptive CTA must match the selected ad network.
- on_fail: block
- rationale: compliance failures can cause rejected display campaigns or account risk.

### P0-2 · html_static_validation
- detector: `html_static_validation`
- description: HTML output must include viewport metadata, visible text, no external script, no iframe, and no remote unbundled assets.
- on_fail: block
- only_if: html output is requested

### P0-3 · honest_data
- detector: `honest_data`
- description: Claims such as discount, ranking, review count, delivery speed, or scarcity must come from user-provided facts.
- on_fail: block

### P0-4 · no_ai_rendered_text
- detector: `asset_contract_guard`
- description: Do not ask the image model to render final headline, CTA, logo, or legal text; add text as layout/handoff content.
- on_fail: block

## P1 · SHOULD PASS (warning, can override)

### P1-1 · hierarchy_under_400ms
- detector: `composition_focal_check`
- description: Product/benefit, headline, and CTA are scannable in roughly 0.4 seconds for the selected IAB size.
- on_fail: warn_require_ack

### P1-2 · landing_page_match
- detector: `landing_page_reference_check`
- description: Visual promise, offer, and CTA align with landing_page_reference when supplied.
- on_fail: warn_require_ack

### P1-3 · responsive_export_readiness
- detector: `browser_validation`
- description: HTML/GIF export should not overflow, crop CTA text, or shift layout across declared viewport sizes.
- on_fail: warn_require_ack

### P1-4 · reduced_motion_fallback
- detector: `motion_review`
- description: Animated GIF variants must keep motion pacing restrained and include reduced-motion or static fallback behavior that preserves CTA readability.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · alternate_size_note
- detector: manual review
- description: Suggest one adaptation note for the nearest sibling IAB size.
