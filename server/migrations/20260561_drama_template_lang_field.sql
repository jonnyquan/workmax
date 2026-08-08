-- Adds DB-side i18n to w_drama_template — second adopter of the
-- platform pattern after w_drama_director_preset (20260560).
--
-- Convention recap:
--
--   System rows (is_system=1, uid=0) fan out as one row per (slug, lang).
--   User rows (is_system=0) keep lang as authoring metadata only.
--   Read path: List handler resolves locale and queries lang IN
--   (userLocale, 'en'), dedupes by slug preferring user locale.
--
-- Existing data:
--   Migration 20260506 seeded 5 system rows (slugs: sweet-romance,
--   ceo-drama, mystery, time-travel, urban-reversal) with Chinese
--   content. Until now the frontend translated those slugs back to
--   English via web/messages/en.json — typical mixed-source mess
--   that this migration cleans up.
--
-- Strategy (non-destructive, unlike 20260560 which DELETE'd because
-- preset rows were mixed-language; here the existing rows are
-- consistently Chinese so we just retag and add):
--   1. ADD COLUMN lang (idempotent INFORMATION_SCHEMA guard).
--   2. UPDATE existing system rows: SET lang='zh' (their content is
--      Chinese; previously they implicitly relied on the en.json key
--      lookup to render English).
--   3. INSERT 5 'en' rows from web/messages/en.json values.
--
-- After this migration the messages namespace shortDramaPage.templates.
-- content can be removed; templates/page.tsx + project-editor-dialog.tsx
-- render API-returned strings directly.

-- ─── Step 1: ADD COLUMN lang (idempotent) ───────────────────────────
SET @col_exists := (
  SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'w_drama_template'
    AND COLUMN_NAME = 'lang'
);
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `w_drama_template`
     ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT ''en'' AFTER `slug`,
     ADD INDEX `idx_lang_slug` (`lang`, `slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ─── Step 2: retag existing system rows as zh ───────────────────────
-- The original 20260506 seed wrote Chinese content but lang defaults
-- to 'en' — so any row whose lang is still the post-ALTER default
-- AND was inserted as a system row gets corrected to 'zh'. User rows
-- (is_system=0) aren't fanned out per locale; their lang stays as the
-- default 'en' (inaccurate for zh-authored user rows but harmless —
-- user rows aren't filtered by lang on read).
UPDATE `w_drama_template`
SET `lang` = 'zh'
WHERE `is_system` = 1
  AND `lang` = 'en'
  AND `slug` IN (
    'sweet-romance', 'ceo-drama', 'mystery',
    'time-travel', 'urban-reversal'
  );

-- ─── Step 3: insert 'en' rows for the 5 system slugs ────────────────
-- Idempotent via WHERE NOT EXISTS keyed on (slug, lang, is_system=1).
-- Each EN row mirrors the ZH row's structural fields (genre, episode
-- counts, aspect ratio, style preset, target platform, emotional
-- template) so the only difference is the translated text columns.

INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'en', 'Sweet Romance',
  'Light, sweet daily romance — each episode has a small conflict that resolves into a sweet moment. Great for daily-update matrix accounts.',
  'sweet_love', 15, 90, '9:16', 'warm-soft', 'douyin,kuaishou',
  'Per episode: misunderstanding → resolution → sweet ending',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template`
  WHERE `slug` = 'sweet-romance' AND `lang` = 'en' AND `is_system` = 1
);

INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'en', 'Boss Romance',
  'Angsty romance built on identity contrasts — cold CEO meets fated heroine, reversal lands on happy ending. Hot genre on short-drama apps.',
  'domineering', 20, 90, '9:16', 'cinematic-cool', 'douyin,short-drama-app',
  'Identity contrast → angst builds → truth reversal → happy ending',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template`
  WHERE `slug` = 'ceo-drama' AND `lang` = 'en' AND `is_system` = 1
);

INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'en', 'Mind-Bending Mystery',
  'Case-clue-per-episode — misdirects and reversals keep viewers hooked. Works for all-age mystery fans.',
  'suspense', 12, 90, '9:16', 'dark-cinematic', 'all-platforms',
  'Case happens → clues gather → misdirect and reversal → truth revealed',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template`
  WHERE `slug` = 'mystery' AND `lang` = 'en' AND `is_system` = 1
);

INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'en', 'Time-Travel Comeback',
  'Modern protagonist thrown into an older era, using modern knowledge to claw their way to the top. Fast-paced and immediately satisfying.',
  'time_travel', 18, 90, '9:16', 'period-warm', 'douyin,kuaishou',
  'Time jump → adaptation → modern-knowledge leverage → gradual rise',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template`
  WHERE `slug` = 'time-travel' AND `lang` = 'en' AND `is_system` = 1
);

INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'en', 'Urban Underdog Rising',
  'Bullied protagonist awakens and steamrolls their adversaries — dense satisfaction beats, emotional-release drama.',
  'urban', 15, 90, '9:16', 'urban-cool', 'all-platforms',
  'Oppressed → awakening → crush opponents → satisfying payoff',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template`
  WHERE `slug` = 'urban-reversal' AND `lang` = 'en' AND `is_system` = 1
);
