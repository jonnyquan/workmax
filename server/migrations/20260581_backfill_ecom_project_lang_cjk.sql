-- SD-P2-5: backfill ecommerce projects authored in Chinese.
--
-- Mirrors short-drama's 20260575 + video-ad's 20260579 backfill.
-- Idempotent: only touches lang='en' rows. CJK heuristic catches
-- title / synopsis with Han characters OR target_platform set to
-- a Chinese-domestic surface (douyin / kuaishou / rednote).

UPDATE `w_ecom_project`
SET `lang` = 'zh'
WHERE `lang` = 'en'
  AND `deleted_at` IS NULL
  AND (
    `title` REGEXP '[一-龯]'
    OR `synopsis` REGEXP '[一-龯]'
    OR `target_platform` IN ('douyin', 'kuaishou', 'rednote')
  );
