# flashCard Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS（阻塞 emit）

### P0-1 · honest_data
- detector: `honest_data`
- description: 教育内容里的事实数字（统计 / 历史日期 / 公式参数）必须真实
- on_fail: block
- rationale: 教学场景，错误事实会被孩子背下来；负面学习损害远超 PPT 数据虚构

### P0-2 · age_appropriate_content
- detector: `regex_scanner` (age_inappropriate catalog — 包含成人词汇 / 政治敏感 / 暴力描述)
- description: 内容符合用户在 question_form 选定的 age_group
- on_fail: block

### P0-3 · no_slop
- detector: `regex_scanner` (anti-ai-slop catalog)
- description: 卡片正反面不出现陈词滥调（"learning is fun" / "let's explore"）
- on_fail: block

### P0-4 · html_static_validation
- detector: `html_static_validation`
- description: HTML flashcard sets must include viewport metadata, visible text for each card face, no external script, no iframe, and no remote unbundled assets.
- on_fail: block
- only_if: html output is requested

## P1 · SHOULD PASS（warning，可 override）

### P1-1 · density_per_card
- detector: `text_word_count`（PR-9+）
- description: 单卡正面 ≤ 15 词，反面解释 ≤ 50 词（认知负荷不超阈值）
- on_fail: warn_require_ack

### P1-2 · clarity
- detector: `readability_score`（PR-9+；Flesch / 中文等价指标）
- description: 文本可读性达到 age_group 对应等级
- on_fail: warn_require_ack

### P1-3 · color_contrast
- detector: `contrast_analyzer`
- description: 文字 / 背景对比度 ≥ AA（学龄前儿童视觉发育中，更需要高对比）
- on_fail: warn_require_ack

### P1-4 · responsive_export_readiness
- detector: `browser_validation`
- description: HTML/PDF/PNG export readiness requires responsive cards with no cropped prompts, answer text overflow, or layout shift across target sizes.
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · spaced_repetition_metadata
- detector: 结构检查（PR-9+）
- description: 卡片包含 spaced repetition 调度建议（recall difficulty / interval）

### P2-2 · cross_card_consistency
- detector: 视觉检查
- description: 卡组内字体 / 排版 / 风格一致
