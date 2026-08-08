# Creative Asset Contract Usage — Product Shot

Use `creative_asset_contract.v1` before making any product image, product HTML
mock, or ad-ready export.

## Required Reads
- `asset_kind: product`: product name, SKU/category, specs, description, and
  `visual_guidance`; this is the source of truth for what appears in frame.
- `asset_kind: brand`: packaging/logo/color/type rules when the shot includes
  brand marks.
- `asset_kind: director_style`: optional lighting, composition, and texture.
- `asset_kind: scene_style`: surface, background, prop, crop, and setting rules.
- `asset_kind: copy_voice`: label/callout/benefit wording and forbidden claims.
- `asset_kind: motion_rules`: turntable/reveal pacing, loop behavior, and
  reduced-motion fallback for HTML/MP4 variants.

## Product Shot Examples
- Product asset confirmed: render the supplied product/reference, preserve
  proportions and visible details from specs/visual guidance.
- Product missing: label as `concept only, not real product representation` or
  ask for product photos; do not create a fake SKU/product render.
- Brand unconfirmed: include `pending brand confirmation` caveat when brand marks
  or packaging appear.
- HTML/PNG export: bundle local product/logo assets and avoid remote-only images.
- Scene style present: use the declared background/surface/crop instead of
  inventing a generic ecommerce studio.
- Copy voice present: use only approved callout language and avoid fabricated
  ratings, sales, or material claims.
