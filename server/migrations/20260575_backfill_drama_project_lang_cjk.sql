-- SD-PR7: backfill lang='zh' for projects authored in Chinese.
--
-- Background: the lang column was added in 20260574 with default 'en'.
-- Pre-migration projects all got 'en' regardless of actual content.
-- For users writing zh dramas this means their outline / script /
-- storyboard prompts silently switched from "the LLM picks up your
-- language from the input" to "force-set to en system prompt" —
-- visible only as subtly anglicised drama beats.
--
-- This migration is idempotent: it only touches rows where lang='en'
-- and the content / target platform strongly indicates Chinese.
--
-- Heuristic: any row where ANY of
--   - title contains a CJK Unified Ideograph
--   - synopsis contains a CJK Unified Ideograph
--   - target_platform is a Chinese-domestic platform (Douyin /
--     Kuaishou / RedNote)
-- becomes lang='zh'. False positives (Chinese title in an English
-- drama project) are vanishingly rare; if a user explicitly wants
-- 'en' they can change it back via the project editor.
--
-- The CJK character class [一-龯] covers Unicode 4E00-9FAF, the
-- main Unified Ideographs block. Works on MySQL 5.7+ and 8.0+
-- without ICU regex.

UPDATE `w_drama_project`
SET `lang` = 'zh'
WHERE `lang` = 'en'
  AND `deleted_at` IS NULL
  AND (
    `title` REGEXP '[一-龯]'
    OR `synopsis` REGEXP '[一-龯]'
    OR `target_platform` IN ('douyin', 'kuaishou', 'rednote')
  );
