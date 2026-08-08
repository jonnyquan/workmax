# productShot Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS（阻塞 emit）

### P0-1 · honest_data
- detector: `honest_data`
- description: 商品文案不能编造销量 / 评分 / 节省 (e.g. "10 万销量") / 时效声称
- on_fail: block
- rationale: 电商场景下虚构数字 → 法律风险（虚假宣传）

### P0-2 · brand_color_compliance
- detector: `brand_spec_grep`
- description: 输出含 hex（背景 / 装饰文字 / logo 区域）必须溯源到 brand-spec.md
- on_fail: block
- only_if: brand-spec.md exists in thread workdir

### P0-3 · no_slop
- detector: `regex_scanner` (anti-ai-slop catalog)
- description: 不出现 AI slop 描述（柔光 + 紫粉蓝渐变 + emoji 装饰）
- on_fail: block
- rationale: 商品图最高频 slop —— "时尚 + 清新 + 简约"模板感重

### P0-4 · asset_contract_guard
- detector: `asset_contract_guard`
- description: 不允许用 fake/generic logo、CSS/text/SVG 伪造 logo，把 made-up product render 当真实产品图，或在缺商品源图时仍交付 final/official product shot
- on_fail: block
- rationale: 商品图必须由真实产品/品牌资产驱动；没有素材时只能问用户、跳过或显式 concept/placeholder

### P0-5 · brand_spec_confirmation
- detector: `brand_spec_confirmation`
- description: 若 brand-spec.md 标记为 unconfirmed / 待确认，输出必须显式带“待品牌方确认” caveat 或水印
- on_fail: block

## P1 · SHOULD PASS（warning，可 override）

### P1-1 · product_focal_point
- detector: `composition_focal_check`（PR-9+；扫描 prompt 是否有 "product centered" / "main subject" 类约束）
- description: 商品占据视觉焦点（不被装饰元素淹没）
- on_fail: warn_require_ack

### P1-2 · color_contrast
- detector: `contrast_analyzer`
- description: 商品名 / 价格信息文本与背景对比度 ≥ AA
- on_fail: warn_require_ack

### P1-3 · context_realism
- detector: `regex_scanner` (unrealistic_setting catalog)
- description: 无超现实背景（商品悬浮天空 / 火山口拍美妆等不可信场景）
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · shadow_realism
- detector: 视觉检查（人工 / 未来视觉模型）
- description: 阴影 / 反光物理合理

### P2-2 · ecom_aspect_ratio
- detector: `aspect_ratio_check`（PR-9+）
- description: 输出比例匹配主流电商（1:1 / 4:5）
