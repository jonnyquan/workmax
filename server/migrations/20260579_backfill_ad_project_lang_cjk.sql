-- SD-P2-3: backfill lang='zh' for ad projects authored in Chinese.
--
-- Background: the lang column was added in 20260578 with default 'en'.
-- Pre-migration projects all got 'en' regardless of actual content.
-- For users running zh ad campaigns this means their ad-script /
-- ad-scene-rewrite prompts silently use the English system prompt.
--
-- This migration is idempotent: it only touches rows where lang='en'
-- and the content / target platform strongly indicates Chinese.
--
-- Heuristic: any row where ANY of
--   - title contains a CJK Unified Ideograph
--   - synopsis contains a CJK Unified Ideograph
--   - target_audience contains a CJK Unified Ideograph
-- becomes lang='zh'. False positives (Chinese title in an English
-- campaign) are vanishingly rare; if a user explicitly wants 'en'
-- they can change it back via the campaign editor.
--
-- The CJK character class [一-龯] covers Unicode 4E00-9FAF, the
-- main Unified Ideographs block. Works on MySQL 5.7+ and 8.0+
-- without ICU regex.

UPDATE `w_ad_project`
SET `lang` = 'zh'
WHERE `lang` = 'en'
  AND `deleted_at` IS NULL
  AND (
    `title` REGEXP '[一-龯]'
    OR `synopsis` REGEXP '[一-龯]'
    OR `target_audience` REGEXP '[一-龯]'
  );
