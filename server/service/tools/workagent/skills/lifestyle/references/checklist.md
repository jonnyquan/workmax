# lifestyle Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS (block emit)

### P0-1 · brand_fit_guard
- detector: `brand_spec_grep`
- description: When brand assets or a brand-spec are present, palette, product styling, mood, and props must stay within the supplied brand world.
- on_fail: block
- only_if: brand-spec.md or brand assets exist in thread workdir
- rationale: lifestyle imagery is often reused as brand campaign material; off-brand worlds are not shippable.

### P0-2 · product_reference_truth
- detector: `asset_contract_guard`
- description: If a product is shown in scene, use the supplied product reference as the source of truth; do not invent SKU shape, colorway, label, or logo.
- on_fail: block
- only_if: product_in_scene

### P0-3 · portrait_rights_and_people_safety
- detector: `identity_safety_review`
- description: Avoid celebrity look-alikes, unrequested demographic stereotypes, and unsupported identity claims for people in the scene.
- on_fail: block

### P0-4 · no_slop
- detector: `regex_scanner` (anti-ai-slop catalog)
- description: No generic stock-photo handshake, over-filtered glow, impossible hands, AI text on props, or perfectly centered sterile lifestyle cliches.
- on_fail: block

## P1 · SHOULD PASS (warning, can override)

### P1-1 · scene_authenticity
- detector: `context_realism_review`
- description: Props, lighting, activity, and body language should feel plausible for the selected home / work / outdoor / social / wellness scene.
- on_fail: warn_require_ack

### P1-2 · brand_presence_balance
- detector: `composition_focal_check`
- description: Brand/product presence is legible but not packshot-dominant unless explicitly requested.
- on_fail: warn_require_ack

### P1-3 · mood_consistency
- detector: `mood_keyword_match`
- description: Warm / aspirational / calm / energetic mood direction is reflected consistently in lighting, palette, and composition.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · crop_variants
- detector: manual review
- description: Provide one crop or framing note for social/feed reuse.

### P2-2 · prop_specificity
- detector: manual review
- description: Include one concrete prop detail that supports the scene without distracting from the brand world.
