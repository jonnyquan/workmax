# Creative Asset Contract Usage — PPT

Use the active `creative_asset_contract.v1` blocks before choosing colors, type,
logos, product visuals, or character imagery.

## Required Reads
- `asset_kind: brand`: use brand colors, typography, voice, motion, and component
  rules only when `contract_status: confirmed`.
- `asset_kind: product`: use product `sku`, `category`, `specs`, and
  `visual_guidance` for hero/product slides.
- `asset_kind: character`: use identity anchors and `reference_image` for people
  or mascot slides.
- `asset_kind: director_style`: use composition, lighting, color, texture, and
  motion as visual language, not as a fake brand.
- `asset_kind: scene_style`: use layout density, background, setting, and mood
  constraints for each slide family.
- `asset_kind: copy_voice`: use approved terminology, headline tone, CTA voice,
  and forbidden claims across the deck.
- `asset_kind: motion_rules`: use transition, reveal, pacing, and reduced-motion
  rules for HTML/native deck variants.

## PPT Examples
- Brand confirmed: apply approved colors/type to title, section divider, CTA,
  charts, and footer.
- Brand draft/unconfirmed: include a visible "pending brand confirmation" caveat
  on brand-heavy slides; do not present the deck as final.
- Product missing: leave `[product image: pending]` or ask for assets; do not draw
  a made-up product render.
- Character missing reference: use a placeholder silhouette and ask for a
  reference image; do not invent identity anchors.
- Copy voice present: keep executive/technical/consumer wording consistent
  across title, section headers, chart captions, and CTA slides.
- Motion rules present: use the same timing and easing in deck-as-HTML exports;
  never add unrelated decorative animation.
