# mobileStory Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS (block emit)

### P0-1 · safe_zone_compliance
- detector: `safe_zone_check`
- description: Primary subject, headline, CTA, and platform UI affordances must stay outside the selected platform's top/bottom overlay zones.
- on_fail: block

### P0-2 · aspect_ratio_compliance
- detector: `aspect_ratio_check`
- description: Story output must preserve strict 9:16 composition unless the user selected a non-story cover format.
- on_fail: block

### P0-3 · html_static_validation
- detector: `html_static_validation`
- description: HTML story handoff must include viewport metadata, visible text, no external script, no iframe, and no remote unbundled assets.
- on_fail: block
- only_if: html output is requested

### P0-4 · motion_timeline_contract
- detector: `html_static_validation`
- description: MP4 or animated HTML output must declare stable motion duration, fps, width, and height metadata before export.
- on_fail: block
- only_if: motion output is requested

## P1 · SHOULD PASS (warning, can override)

### P1-1 · hierarchy_under_swipe
- detector: `composition_focal_check`
- description: The main visual promise should be readable before a user swipes away.
- on_fail: warn_require_ack

### P1-2 · responsive_export_readiness
- detector: `browser_validation`
- description: HTML/MP4 export readiness requires responsive story bounds with no cropped text, offscreen CTA, scroll bleed, or viewport-specific layout shift.
- on_fail: warn_require_ack

### P1-3 · reduced_motion_fallback
- detector: `motion_review`
- description: Motion variants should include reduced-motion behavior or a static fallback that keeps the same hierarchy and CTA.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · platform_variant_note
- detector: manual review
- description: Include one adaptation note for the nearest sibling platform surface.
