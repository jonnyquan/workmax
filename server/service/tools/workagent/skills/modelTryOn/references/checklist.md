# modelTryOn Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS (block emit)

### P0-1 · garment_reference_required
- detector: `asset_contract_guard`
- description: A garment reference image must exist before generating final try-on output; without it, deliver only a question or explicit concept placeholder.
- on_fail: block
- rationale: modelTryOn is garment-as-subject work; generating clothing from text alone breaks the source-fidelity contract.

### P0-2 · garment_fidelity_lock
- detector: `reference_fidelity_review`
- description: Preserve reference garment color, pattern, cut, logo placement, fabric weight, and distinctive seams; do not simplify prints or invent brand marks.
- on_fail: block
- rationale: fidelity failures make the output unusable for SKU or lookbook review.

### P0-3 · portrait_rights_and_identity
- detector: `identity_safety_review`
- description: Avoid celebrity look-alike faces, unrequested demographic narrowing, and identity claims that the user did not supply.
- on_fail: block

### P0-4 · no_slop
- detector: `regex_scanner` (anti-ai-slop catalog)
- description: No perfect skin-tight print warping, broken fingers, AI-rendered garment text, waxy skin, or generic fashion-studio cliches.
- on_fail: block

## P1 · SHOULD PASS (warning, can override)

### P1-1 · fit_and_drape_realism
- detector: `visual_fidelity_review`
- description: Fabric drape, wrinkles, sleeve openings, hems, and body contact should look physically plausible for the selected pose.
- on_fail: warn_require_ack

### P1-2 · pose_coverage
- detector: `pose_keyword_match`
- description: Requested front / side / back / walking / no-face pose is reflected in the generated prompt and delivery note.
- on_fail: warn_require_ack

### P1-3 · lighting_material_readability
- detector: `composition_focal_check`
- description: Lighting must keep garment texture and silhouette readable; mood lighting cannot hide the SKU details.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · secondary_crop
- detector: manual review
- description: Provide one crop suggestion for ecommerce listing or lookbook detail view.

### P2-2 · caveat_quality
- detector: manual review
- description: Delivery caveats clearly state model realism, portrait-rights, and garment-fidelity review limits.
