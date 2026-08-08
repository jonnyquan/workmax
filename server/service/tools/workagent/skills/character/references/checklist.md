# Character Pre-Emit Checklist

> Schema follows the WorkAgent pre-emit checklist contract.

## P0 · MUST PASS（阻塞 emit）

### P0-1 · character_anchor_consistency
- detector: `character_anchor_consistency`
- description: 多个生成 prompt 中提及同一角色时，关键特征（外貌 / 服饰 / 锚点）保持一致
- on_fail: block
- only_if: character-spec.md exists in thread workdir
- rationale: 角色一致性是 character mode 的核心价值；前后变脸 / 换装是 P0 致命缺陷

### P0-2 · honest_data
- detector: `honest_data`
- description: 任何文本说明（角色简介 / 设定）不能编造数据（年龄 / 身高 / 履历类）
- on_fail: block
- rationale: 角色卡的虚构属性 OK，但用户给定数字必须保留

### P0-3 · no_slop
- detector: `regex_scanner` (anti-ai-slop catalog)
- description: 不出现 AI slop 视觉描述（紫粉蓝渐变 / Inter 字体 / 西方面孔默认）
- on_fail: block

## P1 · SHOULD PASS（warning，可 override）

### P1-1 · pose_diversity
- detector: `pose_diversity`（PR-9+ 添加；扫描多个 prompt 的 camera_angle / pose 参数是否过度集中）
- description: 多张产出中至少 3 种不同视角 / 表情
- on_fail: warn_require_ack
- rationale: 全 9 张都是同一正面静态照 → 角色卡不可用

### P1-2 · outfit_lock_respected
- detector: `outfit_keyword_match`（PR-9+ 添加；regex match outfit 关键词）
- description: 用户指定的服饰锁定（如 "navy jacket only"）在所有生成中保留
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · cultural_sensitivity
- detector: `cultural_review`（人工审，仅记录）
- description: 角色身份描述符合用户文化背景（避免默认西方面孔）

### P2-2 · accessory_consistency
- detector: `accessory_keyword_match`（PR-9+）
- description: 配饰（眼镜 / 帽子 / 表）跨张一致
