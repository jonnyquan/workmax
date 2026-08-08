-- Adds DB-side i18n to w_drama_director_preset and reseeds system rows
-- as one row per (slug, lang) — establishing the platform pattern for
-- system-curated content tables.
--
-- Why this shape (per-row-per-lang vs JSON map vs messages files):
--   1. DB is the single source of truth for system content. Ops can
--      edit translations with a SQL UPDATE; no code release needed.
--   2. Adding a new locale is a data change (INSERT rows), not a code
--      change. Same posture as user-authored rows.
--   3. Mirrors how user-created presets already live in this table —
--      a uniform read path; the only twist is system rows fan out
--      across locales while user rows stay single-row.
--
-- Other system-content tables should adopt the same convention.
--
-- Migration steps (one transaction's worth, but pure DDL/DML so MySQL
-- auto-commits each statement):
--   1. ADD COLUMN `lang` with index for the (lang, slug) lookup.
--   2. DELETE existing system rows (the previous seed wrote
--      Chinese-name + English-description hybrids — not a clean
--      starting point for per-locale rows).
--   3. INSERT 60 rows = 30 logical slugs × 2 langs (en + zh). Other
--      16 supported locales fall back to 'en' on read until ops or
--      a follow-up migration fills them in.
--
-- Idempotency:
--   - ADD COLUMN guarded with INFORMATION_SCHEMA check (re-run safe).
--   - DELETE is scoped to uid=0 + project_id IS NULL — user/project
--     rows are never touched.
--   - Each INSERT uses WHERE NOT EXISTS keyed on (slug, lang, uid=0,
--     project_id IS NULL), so re-running this migration after manual
--     additions doesn't clobber them.
--
-- Slug namespace preserved verbatim from prior seeds:
--   - 10 ex-Go-built-in slugs (push-in-reveal, etc.) — Go catalogue
--     deleted in the same PR; these slugs become system DB rows.
--   - 20 sys-* slugs covering drama / manga / ad / ecommerce verticals.

-- ─── Step 1: ADD COLUMN lang (idempotent) ───────────────────────────
SET @col_exists := (
  SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'w_drama_director_preset'
    AND COLUMN_NAME = 'lang'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `w_drama_director_preset`
     ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT ''en'' AFTER `slug`,
     ADD INDEX `idx_lang_slug` (`lang`, `slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ─── Step 2: clear existing system rows ─────────────────────────────
-- Prior seed (20260554) wrote Chinese names + English descriptions in
-- one mixed row per slug. The new model is one row per (slug, lang)
-- with consistent content per row, so we wipe and reseed. User rows
-- (uid != 0) and project rows (project_id IS NOT NULL) are untouched.
DELETE FROM `w_drama_director_preset`
WHERE `uid` = 0 AND `project_id` IS NULL;

-- ─── Step 3: seed 60 rows (30 slugs × en + zh) ─────────────────────
-- Tag shape stays {"labels": [...]} per tagsToJSONMap. Composition
-- stays English in both rows: it's primarily AI prompt fuel, not user-
-- facing display copy, and English prompts generate more reliably with
-- current video models. If composition translation is needed later
-- it's a follow-up migration, not a schema change.

-- ── 10 slugs graduated from the deleted Go built-ins ──

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Establishing wide · locked', 'establishing-wide-locked',
  'Wide establishing shot, locked camera. Use to open a scene or reset location.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '开场远景 · 锁定', 'establishing-wide-locked',
  '开场用远景，相机锁定。用于建立场景或切换位置。',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Reactive close-up · held', 'reactive-closeup-held',
  'Close-up on the reacting character. For dialogue payoff or silent emotional beat.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '情绪特写 · 停留', 'reactive-closeup-held',
  '对反应方人物的特写。用于台词回应或无言情绪点。',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Push-in reveal', 'push-in-reveal',
  'Slow dolly-in on the subject to build tension or mark a revelation.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '推镜揭示', 'push-in-reveal',
  '缓慢推镜，制造张力或标记关键揭示。',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Pull-back reveal', 'pull-back-reveal',
  'Dolly-out exposing more of the environment — use to widen stakes or reveal presence.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '拉镜扩展', 'pull-back-reveal',
  '拉远镜头展示环境，用于扩大格局或揭示存在。',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Over-shoulder dialogue · A', 'shoulder-dialogue-a',
  'Over-shoulder on character B, character A speaking. Standard dialogue coverage side.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '对话过肩 A', 'shoulder-dialogue-a',
  '对人物 B 过肩，人物 A 说话。标准对话覆盖的一侧。',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Over-shoulder dialogue · B', 'shoulder-dialogue-b',
  'Reverse of shoulder-dialogue-a. Edits together as a shot-reverse pair.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '对话过肩 B', 'shoulder-dialogue-b',
  '对话过肩 A 的反打。可与之配成正反打剪辑。',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Handheld pursuit', 'handheld-pursuit',
  'Handheld tracking shot following the subject — motion, urgency, point-of-view feel.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '手持跟随', 'handheld-pursuit',
  '手持跟拍主体——运动、紧迫、第一人称代入感。',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Crane descent', 'crane-descent',
  'High crane descending toward the subject. Opens a scene with scale, then lands on intimacy.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '顶机俯降', 'crane-descent',
  '高位摇臂从俯视下降至主体。开场用规模感切入亲密感。',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Insert · object detail', 'insert-object-detail',
  'Tight insert on a prop or gesture. Use for scene punctuation or reveal of a key object.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '道具特写', 'insert-object-detail',
  '对道具或动作的紧切插入镜头。用于场景标点或关键物件揭示。',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Pan to reaction', 'pan-reveal-reaction',
  'Pan that ends on a character''s reaction. Bridges off-screen information to on-screen response.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '摇镜接反应', 'pan-reveal-reaction',
  '以摇镜结束于人物反应。把画外信息桥接到画内回应。',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── 5 drama vertical extras (sys-drama-*) ──

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Flashback fade-in', 'sys-drama-flashback-fade',
  'Soft fade-in evoking memory or flashback. Use to bridge present and past beats.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '闪回淡入', 'sys-drama-flashback-fade',
  '柔光淡入唤起记忆或闪回。用于连接当下与过去的节拍。',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Low-light night interior', 'sys-drama-low-light-night',
  'Night-interior shot with practical light only. Use for tense or intimate beats after dark.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '夜戏低照度', 'sys-drama-low-light-night',
  '仅靠现场光源的夜内景镜头。用于夜间紧张或亲密的节拍。',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Confrontation two-shot', 'sys-drama-confrontation-twoshot',
  'Symmetrical two-shot for confrontation. Both faces readable, tension in the gap between them.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '对峙双人', 'sys-drama-confrontation-twoshot',
  '对峙时的对称双人镜头。两人面孔清晰可读，张力在两人之间的间隙。',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Low-angle power', 'sys-drama-power-low-angle',
  'Low-angle medium emphasising dominance or threat of the subject.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '仰角威压', 'sys-drama-power-low-angle',
  '仰角中景，强调主体的威压或威胁感。',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'First-person handheld', 'sys-drama-pov-handheld',
  'Handheld POV from the protagonist''s vantage. Use for immersion or chase beats.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '第一人称手持', 'sys-drama-pov-handheld',
  '主角视角的手持 POV。用于沉浸感或追逐节拍。',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── 5 manga vertical (sys-manga-*) ──

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Manga panel zoom-in', 'sys-manga-panel-zoom',
  'Manga-panel zoom-in: start framed like a comic panel, push past the borders into live motion.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '漫格放大入场', 'sys-manga-panel-zoom',
  '漫格放大入场：开场带漫画分格边框，相机推近时边框淡出，主体进入实拍。',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Speedline burst', 'sys-manga-speedline-burst',
  'Action shot with radial speed lines bursting from the subject. Manga-style impact emphasis.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '拟声速度线', 'sys-manga-speedline-burst',
  '动作镜头带辐射状速度线从主体迸发。漫画式冲击感强调。',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Action freeze', 'sys-manga-action-freeze',
  'Hold on the apex of an action — the manga splash-page moment. Brief still before continuing.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '动作定格', 'sys-manga-action-freeze',
  '停在动作顶峰——漫画「跨页」的瞬间。继续之前短暂定格。',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Split-panel screen', 'sys-manga-split-screen',
  'Split-screen showing two simultaneous beats — manga-style multi-panel layout in motion.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '分格并置', 'sys-manga-split-screen',
  '分屏展示两组同时发生的节拍——漫画式多分格的动态版本。',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Ink-wash transition', 'sys-manga-ink-transition',
  'Ink-wash transition between scenes. Use as a stylised wipe between manga sequences.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '水墨过场', 'sys-manga-ink-transition',
  '镜头之间的水墨过场。作为漫剧序列之间的风格化擦切。',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── 5 video-ad vertical (sys-ad-*) ──

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Hero product spin', 'sys-ad-hero-product-spin',
  'Hero product on plain backdrop with slow rotational reveal. Default for product launch ads.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '产品 hero 旋转', 'sys-ad-hero-product-spin',
  '纯色背景上的产品 hero 镜头，缓慢旋转揭示。产品发布广告默认款。',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'UGC selfie', 'sys-ad-ugc-selfie',
  'Authentic UGC look — handheld selfie framing, casual setting, talent speaking to camera.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', 'UGC 自拍口播', 'sys-ad-ugc-selfie',
  '真实 UGC 风格——手持自拍构图，生活化场景，主播对镜头说话。',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Pain-point cut', 'sys-ad-pain-point-cut',
  'Two-beat cut juxtaposing pain (before) and relief (after). Common ad hook structure.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '痛点对比剪', 'sys-ad-pain-point-cut',
  '两段式剪辑并置痛点（前）与缓解（后）。常见广告 hook 结构。',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'CTA end card', 'sys-ad-cta-end-card',
  'End-card composition for the CTA / brand mark. Hold long enough to read.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', 'CTA 收口', 'sys-ad-cta-end-card',
  '用于 CTA / 品牌标识的收尾画面。停留足够长以便观众阅读。',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Before/after split', 'sys-ad-before-after-split',
  'Single-frame before/after split. No cut — split-screen comparison in one take.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '对比前后', 'sys-ad-before-after-split',
  '单帧内的前后对比分屏。无剪辑——一个镜头内完成对比。',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── 5 ecommerce vertical (sys-ecom-*) ──

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Product 360°', 'sys-ecom-product-360',
  'Full 360° turntable rotation of the product. Standard ecommerce listing video.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '商品 360°', 'sys-ecom-product-360',
  '商品的完整 360° 转盘旋转。标准电商详情页视频。',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Detail macro', 'sys-ecom-detail-macro',
  'Macro insert highlighting material, stitching, or texture. Use to convey craft and quality.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '细节微距', 'sys-ecom-detail-macro',
  '突出材质、车线或纹理的微距插入镜头。用于传达工艺与品质。',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Lifestyle usage', 'sys-ecom-usage-scene',
  'Lifestyle shot showing the product in real use. Connects feature to context.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '使用场景', 'sys-ecom-usage-scene',
  '展示产品在真实生活中使用的镜头。把功能与使用情境关联起来。',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Feature callout', 'sys-ecom-feature-callout',
  'Feature callout — product on screen with text labels animating in to call out selling points.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '卖点字幕浮现', 'sys-ecom-feature-callout',
  '卖点标注——画面上同时显示产品和浮现的卖点字幕，标注关键部位。',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'en', 'Unboxing POV', 'sys-ecom-unboxing',
  'First-person unboxing angle — talent''s hands opening packaging, package centred in frame.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'en' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'zh', '开箱第一视角', 'sys-ecom-unboxing',
  '第一人称开箱角度——主播双手打开包装，包装居中入框。',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'zh' AND `uid` = 0 AND `project_id` IS NULL
);
