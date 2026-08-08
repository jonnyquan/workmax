# Creative Asset Contract Usage — Social Ad

Use `creative_asset_contract.v1` before generating image or motion ad concepts.
Social ads may be single-frame image, HTML draft, or MP4 variant, but brand and
product assets stay source-bound.

## Required Reads
- `asset_kind: brand`: colors, typography, logo rules, voice, and anti-patterns.
- `asset_kind: product`: product image/specs/visual guidance for product-led ads.
- `asset_kind: character`: identity anchors when a person/mascot is used.
- `asset_kind: director_style`: optional visual treatment for composition/motion.
- `asset_kind: scene_style`: feed crop, background, platform safe zones, and
  setting.
- `asset_kind: copy_voice`: hook, primary text, CTA wording, and claims boundary.
- `asset_kind: motion_rules`: first-frame hold, transition pacing, loop behavior,
  and reduced-motion fallback.

## Social Ad Examples
- Product-led ad: real product image first; no made-up product render.
- Brand-led ad: use confirmed brand tokens; if unconfirmed, add a visible caveat.
- Character ad: preserve identity anchors across feed image and video variant.
- Motion variant: use the same brand/product/character contract as the still
  version; do not treat MP4 as a separate visual system.
- Copy voice present: keep the hook/CTA inside the approved tone and do not
  invent discount, ROI, or urgency claims.
- Scene style present: preserve platform crop and safe-zone constraints across
  image, HTML, and MP4 variants.
