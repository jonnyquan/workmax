# PPT Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.
> Each item references a detector by registry name (see server/service/tools/workagent/detectors/).
> The gate (PR-9) reads this file and dispatches detector.Run() per item.

## P0 · MUST PASS（阻塞 emit）

### P0-1 · honest_data
- detector: `honest_data`
- description: 所有数字 / 百分比能溯源到 source 或标记为 `—`
- on_fail: block
- rationale: PPT 是企业付费用户主战场；编造数字一旦被客户 / 老板拿去用会爆雷

### P0-2 · brand_color_compliance
- detector: `brand_spec_grep`
- description: 输出含 hex 时必须能溯源到 `brand-spec.md`
- on_fail: block
- only_if: brand-spec.md exists in thread workdir
- rationale: 模型偶尔凭记忆猜品牌色（典型："Anthropic 红色" 猜成 "Stripe 蓝"）

### P0-2a · brand_spec_confirmation
- detector: `brand_spec_confirmation`
- description: 若 brand-spec.md 标记为 unconfirmed / 待确认，deck 必须显式带“待品牌方确认” caveat 或水印
- on_fail: block

### P0-2b · asset_contract_guard
- detector: `asset_contract_guard`
- description: deck 中不允许用 fake/generic logo、CSS/text/SVG 伪造品牌标识，或声明角色/商品一致性却没有 @character / product source / reference anchor
- on_fail: block

### P0-3 · slide_count_compliance
- detector: slide_count_within_range
- description: 实际生成的 slide 数 ≤ 用户在 question_form 指定的 scale 上限（≤10 / 10-20 / 20+）
- on_fail: block
- rationale: 用户明确说 "10 页 PPT"，模型生成 25 页 = 直接违反约束。注：本检测器尚未注册（PR-9b 跟进），gate 当前 fallback 到 skipped

### P0-4 · html_static_validation
- detector: `html_static_validation`
- description: HTML deck handoff must include viewport metadata, visible text, no external script, no iframe, and no remote unbundled assets.
- on_fail: block
- only_if: html output is requested

## P1 · SHOULD PASS（warning，可 override）

### P1-1 · color_contrast
- detector: `contrast_analyzer`
- description: 文字与背景对比度 ≥ WCAG AA (4.5:1)
- on_fail: warn_require_ack
- rationale: 投屏 / 印刷 / 远距离观看时低对比度文字不可读

### P1-2 · caption_orphan_lines
- detector: `orphan_detector`
- description: 无单字成行的孤儿行（破坏视觉节奏）
- on_fail: warn_require_ack

### P1-3 · two_font_max
- detector: font_family_count
- description: 单文档使用的字体家族 ≤ 2（display + body）
- on_fail: warn_require_ack
- rationale: 业余感 PPT 最常见的特征是 3+ 字体混用。注：detector PR-9b 跟进

### P1-4 · responsive_export_readiness
- detector: `browser_validation`
- description: HTML deck preview/export readiness requires responsive slide bounds with no cropped text, overflow, or layout shift before PDF/PNG/ZIP handoff.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE（不阻塞，仅记录到 trace）

### P2-1 · consistent_grid
- detector: grid_alignment
- description: 元素对齐到 8pt 或 12pt 网格（detector 未来添加）

### P2-2 · ample_negative_space
- detector: negative_space_ratio
- description: 单页元素密度不超过 60%（detector 未来添加）
