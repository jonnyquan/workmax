# socialAd Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS (block emit)

### P0-1 · platform_format_compliance
- detector: `platform_policy_check`
- description: Aspect ratio, format, safe-zone, CTA, and claim style must match the selected social platform and ad objective.
- on_fail: block

### P0-2 · honest_data
- detector: `honest_data`
- description: Claims about discounts, rankings, reviews, delivery speed, scarcity, or performance must come from user-provided facts.
- on_fail: block

### P0-3 · no_ai_rendered_text
- detector: `asset_contract_guard`
- description: Do not ask the image or video model to bake final headline, CTA, logo, or legal text when the platform-native text layer should own it.
- on_fail: block

## P1 · SHOULD PASS (warning, can override)

### P1-1 · first_frame_hook
- detector: `composition_focal_check`
- description: First frame should communicate product, benefit, or visual hook before a user scrolls away.
- on_fail: warn_require_ack

### P1-2 · motion_pacing
- detector: `motion_review`
- description: MP4 variants should use clear motion pacing, preserve CTA readability, and include reduced-motion or static fallback behavior.
- on_fail: warn_require_ack

### P1-3 · brand_fit
- detector: `brand_spec_grep`
- description: Palette, typography, logo usage, and product treatment should align with active brand assets when supplied.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · platform_variant_note
- detector: manual review
- description: Include one adaptation note for the nearest sibling platform or aspect ratio.
