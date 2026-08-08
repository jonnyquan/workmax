-- Veo provider rotation: only veo-3.1-generate-preview and
-- veo-3.1-fast-generate-preview are supported going forward. The
-- legacy veo-3.0-generate-001 row is soft-deleted (deleted_at set,
-- enabled=0) so it can't be selected at runtime but the historic row
-- remains for audit.
--
-- Idempotent:
--   - the soft-delete UPDATE is a no-op once deleted_at is non-null
--   - the INSERT for veo31_fast_video guards on NOT EXISTS

-- 1. Soft-delete + disable the legacy veo-3.0 row.
UPDATE `w_generator_provider`
   SET `enabled` = 0, `deleted_at` = NOW(3)
 WHERE `name` = 'veo_video'
   AND `model` = 'veo-3.0-generate-001'
   AND `deleted_at` IS NULL;

-- 2. Insert the veo-3.1-fast row, mirroring the existing veo-3.1
-- (veo31_video) row's endpoint / api_key / extra_config so secrets
-- never appear in the migration. The model field swaps to
-- veo-3.1-fast-generate-preview.
INSERT INTO `w_generator_provider`
  (`name`, `type`, `media_type`, `enabled`, `is_default`, `priority`,
   `endpoint`, `api_key`, `model`,
   `daily_quota`, `monthly_quota`, `concurrent_limit`,
   `extra_config`, `description`, `created_at`, `updated_at`)
SELECT
  'veo31_fast_video',
  'veo31',
  'video',
  1,
  0,
  75,
  `endpoint`,
  `api_key`,
  'veo-3.1-fast-generate-preview',
  `daily_quota`,
  `monthly_quota`,
  `concurrent_limit`,
  `extra_config`,
  'Veo 3.1 Fast async video provider',
  NOW(),
  NOW()
FROM `w_generator_provider`
WHERE `name` = 'veo31_video'
  AND `deleted_at` IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM `w_generator_provider`
     WHERE `name` = 'veo31_fast_video'
       AND `deleted_at` IS NULL
  )
LIMIT 1;
