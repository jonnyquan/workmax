# packaging Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS (block emit)

### P0-1 · brand_fit_guard
- detector: `brand_spec_grep`
- description: Packaging colors, mark placement, finish language, and shelf impression must stay aligned with supplied brand assets or brand-spec.
- on_fail: block
- only_if: brand-spec.md or brand assets exist in thread workdir
- rationale: packaging is a brand system artifact; off-brand packaging creates downstream production waste.

### P0-2 · concept_not_production_dieline
- detector: `delivery_caveat_check`
- description: Delivery must clearly state this is a concept mockup, not a production dieline, print-ready file, or regulatory label.
- on_fail: block

### P0-3 · asset_contract_guard
- detector: `asset_contract_guard`
- description: Do not fake logos, certification marks, barcodes, nutrition panels, legal copy, or production claims that the user did not provide.
- on_fail: block

### P0-4 · material_feasibility
- detector: `production_feasibility_review`
- description: Material, closure, transparency, foil, embossing, cutout, and structural choices must be plausible for the selected packaging type.
- on_fail: block

## P1 · SHOULD PASS (warning, can override)

### P1-1 · shelf_readability
- detector: `composition_focal_check`
- description: Product category, brand mark area, and primary visual cue are readable at shelf distance.
- on_fail: warn_require_ack

### P1-2 · brand_color_compliance
- detector: `brand_spec_grep`
- description: Dominant palette should use approved brand colors when a brand-spec is available.
- on_fail: warn_require_ack

### P1-3 · surface_layout_handoff
- detector: `layout_handoff_review`
- description: Notes distinguish front panel, side panel, cap/closure, label area, and optional secondary surfaces.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · material_variant
- detector: manual review
- description: Suggest one feasible material or finish variant for later exploration.

### P2-2 · sustainability_note
- detector: manual review
- description: Mention sustainability tradeoffs only when compatible with the chosen material and user brief.
