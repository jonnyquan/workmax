# Creative Asset Contract Usage — Web Banner

Use `creative_asset_contract.v1` before creating HTML, PNG, JPG, or GIF banner
assets. Banners are brand-sensitive because the logo, CTA, product image, and
landing page must match.

## Required Reads
- `asset_kind: brand`: logo, colors, typography, voice, and component rules.
- `asset_kind: product`: product image, SKU/category, and `visual_guidance`.
- `asset_kind: director_style`: optional composition/motion language.
- `asset_kind: scene_style`: banner setting, crop, safe area, and background
  treatment.
- `asset_kind: copy_voice`: headline, CTA, offer wording, and claims boundary.
- `asset_kind: motion_rules`: GIF/HTML timing, hover/reveal behavior, and
  reduced-motion fallback.

## Banner Examples
- Confirmed brand: use the real logo image/SVG and approved color/type tokens.
- Missing logo: place `[logo: pending]` or ask the user; never draw a CSS/text
  logo or generic wordmark.
- Missing product photo: use `[product image: pending]`, not a fake render.
- HTML export: bundle local logo/product images and avoid remote-only assets.
- GIF/motion: keep brand marks static and legible; motion can support hierarchy
  but cannot distort logo or product identity.
- Copy voice: keep CTA text inside the approved voice and avoid inventing offer
  numbers.
- Scene style: preserve the declared crop/safe-area rules for all responsive
  banner sizes.
