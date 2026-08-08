-- Structured character identity anchors for the short-drama module.
-- Borrowed from moyin-creator's 6-layer anchor system: bone structure /
-- facial features / unique marks / color anchors / skin texture / hair.
-- Injected into image+video prompts at generation time to hold cross-
-- shot appearance consistency (same face across 20+ panels of an
-- episode).
--
-- The existing `appearance` TEXT column stays for user-authored free
-- prose (high-level "tall, lean, wears a grey trench coat"); the new
-- `identity_anchors` JSON column is the AI-calibrated structured form
-- the generation pipeline consumes. Both coexist — the calibrator reads
-- `appearance` as input and produces `identity_anchors` as output.
--
-- `negative_anchors` parallels that split: the existing `negative_prompt`
-- TEXT stays for user freeform negatives; `negative_anchors` JSON is the
-- structured { avoid: [...], styleExclusions: [...] } the calibrator
-- produces.
--
-- See server/prompts/short-drama/character-calibrate.md for the
-- calibration prompt.

ALTER TABLE `w_character`
  ADD COLUMN `identity_anchors` JSON DEFAULT NULL
    COMMENT '6层身份锚点JSON: 骨相/五官/辨识标记/色彩/皮肤/发型',
  ADD COLUMN `negative_anchors` JSON DEFAULT NULL
    COMMENT '结构化负面提示词 { avoid[], styleExclusions[] }',
  ADD COLUMN `calibrated_at` DATETIME DEFAULT NULL
    COMMENT '上次 AI 校准完成时间 (NULL 表示尚未校准过)';
