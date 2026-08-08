# marketingPoster Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS（阻塞 emit）

### P0-1 · honest_data
- detector: `honest_data`
- description: CTA / offer 数字必须真实（"立省 30%" 必须用户给出基数）
- on_fail: block
- rationale: 营销海报最容易出现编造折扣 / 客户数 / 增长率

### P0-2 · brand_color_compliance
- detector: `brand_spec_grep`
- description: 海报色彩必须用 brand-spec.md 已定义的色彩
- on_fail: block
- only_if: brand-spec.md exists in thread workdir
- rationale: 海报是品牌曝光载体，色错 = 品牌伤害

### P0-3 · no_slop
- detector: `regex_scanner` (anti-ai-slop catalog)
- description: 不出现常见营销 slop（emoji 装饰 + 紫粉渐变 + "limited time only"）
- on_fail: block

### P0-4 · asset_contract_guard
- detector: `asset_contract_guard`
- description: 不允许用 fake/generic logo、CSS/text/SVG 伪造品牌标识，或声明角色/商品一致性却没有 @character / product source / reference anchor；缺素材时必须问用户、跳过或显式 placeholder
- on_fail: block

### P0-5 · brand_spec_confirmation
- detector: `brand_spec_confirmation`
- description: 若 brand-spec.md 标记为 unconfirmed / 待确认，海报必须显式带“待品牌方确认” caveat 或水印
- on_fail: block

### P0-6 · html_static_validation
- detector: `html_static_validation`
- description: HTML poster handoff must include viewport metadata, visible text, no external script, no iframe, and no remote unbundled assets.
- on_fail: block
- only_if: html output is requested

## P1 · SHOULD PASS（warning，可 override）

### P1-1 · cta_visibility
- detector: `cta_prominence_check`（PR-9+；扫描 cta_style 字段 + prompt 中 "primary CTA" 标记）
- description: 主 CTA 在视觉上 dominant（位置 / 颜色 / 字号最强）
- on_fail: warn_require_ack

### P1-2 · color_contrast
- detector: `contrast_analyzer`
- description: 文字与背景对比度 ≥ AA（投放期间用户在 feed 流中视线短，对比度低 = 流失）
- on_fail: warn_require_ack

### P1-3 · text_density
- detector: `text_word_count`（PR-9+）
- description: 海报正文 ≤ 30 词（信息过载会让用户秒滑过）
- on_fail: warn_require_ack

### P1-4 · responsive_export_readiness
- detector: `browser_validation`
- description: HTML/PDF/PNG export readiness requires a responsive poster artboard with no cropped headline, CTA overflow, or viewport-specific layout shift.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · visual_hook
- detector: 视觉检查
- description: 1 个明确的视觉钩子（人物表情 / 动态构图 / 大字号数字）

### P2-2 · platform_aspect_ratio
- detector: `aspect_ratio_check`（PR-9+）
- description: 比例匹配目标平台（1:1 IG / 9:16 Story / 1.91:1 X）
