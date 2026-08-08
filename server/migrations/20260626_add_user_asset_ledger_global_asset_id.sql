SET @schema_name := DATABASE();

-- Explicit bridge from the user-facing asset ledger projection back to the
-- canonical cross-tool asset row. Keep source/source_id/item_key as the
-- idempotency key for legacy callers.
SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND COLUMN_NAME = 'global_asset_id'
    ),
    'SELECT 1',
    'ALTER TABLE `w_user_asset_ledger` ADD COLUMN `global_asset_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''optional w_global_asset.id bridge'' AFTER `source_id`'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND INDEX_NAME = 'idx_uid_global_asset'
    ),
    'SELECT 1',
    'ALTER TABLE `w_user_asset_ledger` ADD INDEX `idx_uid_global_asset` (`uid`, `global_asset_id`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

UPDATE `w_user_asset_ledger` l
JOIN `w_global_asset` ga ON ga.id = l.source_id AND ga.uid = l.uid
SET l.global_asset_id = ga.id
WHERE l.source = 'canvas'
  AND l.global_asset_id = 0;

UPDATE `w_user_asset_ledger` l
JOIN `w_generation_object` go ON go.id = l.source_id AND go.uid = l.uid
SET l.global_asset_id = go.global_asset_id
WHERE l.source = 'generated'
  AND l.global_asset_id = 0
  AND go.global_asset_id <> 0;

UPDATE `w_user_asset_ledger` l
JOIN `w_workagent_thread_file` wf ON wf.id = l.source_id AND wf.uid = l.uid
SET l.global_asset_id = wf.global_asset_id
WHERE l.source IN ('thread_upload', 'thread_output')
  AND l.global_asset_id = 0
  AND wf.global_asset_id <> 0;

UPDATE `w_user_asset_ledger` l
JOIN `w_generation_object` go
  ON go.uid = l.uid
 AND go.global_asset_id <> 0
 AND (go.public_url = l.url OR go.source_url = l.url)
SET l.global_asset_id = go.global_asset_id
WHERE l.source = 'generation_input'
  AND l.global_asset_id = 0
  AND l.url <> '';

INSERT INTO `w_user_asset_ledger`
  (`uid`, `source`, `source_id`, `global_asset_id`, `item_key`, `visibility_status`,
   `container_type`, `container_key`, `container_title`, `container_uuid`, `title`, `kind`,
   `mime_type`, `size_bytes`, `width`, `height`, `url`, `thumb_url`, `preview_url`,
   `project_id`, `project_title`, `project_uuid`, `object_key`, `storage_path`,
   `is_attached`, `has_managed_object`, `created_at`, `updated_at`)
SELECT
  ga.uid,
  'canvas',
  ga.id,
  ga.id,
  '',
  1,
  'project',
  CONCAT('canvas:', COALESCE(NULLIF(gp.uuid, ''), CONCAT('project-', COALESCE(ga.project_id, 0)), CONCAT('asset-', ga.id))),
  COALESCE(NULLIF(gp.title, ''), 'canvas-project'),
  COALESCE(gp.uuid, ''),
  COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.originalName')), ''), NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.title')), ''), ga.kind, 'canvas-asset'),
  ga.kind,
  ga.mime_type,
  ga.size_bytes,
  ga.width,
  ga.height,
  ga.url,
  ga.thumb_url,
  CASE WHEN ga.mime_type LIKE 'image/%' THEN COALESCE(NULLIF(ga.thumb_url, ''), ga.url) ELSE ga.thumb_url END,
  COALESCE(ga.project_id, 0),
  COALESCE(gp.title, ''),
  COALESCE(gp.uuid, ''),
  COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.objectKey')), ''), ''),
  COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.objectKey')), ''), ga.url),
  CASE WHEN ga.project_id IS NULL OR ga.project_id = 0 THEN 0 ELSE 1 END,
  CASE WHEN COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.objectKey')), ''), '') <> '' THEN 1 ELSE 0 END,
  ga.created_at,
  ga.updated_at
FROM `w_global_asset` ga
LEFT JOIN `w_global_project` gp ON gp.id = ga.project_id
LEFT JOIN `w_user_asset_ledger` existing
  ON existing.uid = ga.uid
 AND existing.source = 'canvas'
 AND existing.source_id = ga.id
 AND existing.item_key = ''
WHERE ga.source_table = 'canvas_project_file'
  AND ga.status <> 4
  AND ga.deleted_at IS NULL
  AND existing.id IS NULL
ON DUPLICATE KEY UPDATE
  `global_asset_id` = VALUES(`global_asset_id`),
  `container_type` = VALUES(`container_type`),
  `container_key` = VALUES(`container_key`),
  `container_title` = VALUES(`container_title`),
  `container_uuid` = VALUES(`container_uuid`),
  `title` = VALUES(`title`),
  `kind` = VALUES(`kind`),
  `mime_type` = VALUES(`mime_type`),
  `size_bytes` = VALUES(`size_bytes`),
  `width` = VALUES(`width`),
  `height` = VALUES(`height`),
  `url` = VALUES(`url`),
  `thumb_url` = VALUES(`thumb_url`),
  `preview_url` = VALUES(`preview_url`),
  `project_id` = VALUES(`project_id`),
  `project_title` = VALUES(`project_title`),
  `project_uuid` = VALUES(`project_uuid`),
  `object_key` = VALUES(`object_key`),
  `storage_path` = VALUES(`storage_path`),
  `is_attached` = VALUES(`is_attached`),
  `has_managed_object` = VALUES(`has_managed_object`),
  `updated_at` = VALUES(`updated_at`);

INSERT INTO `w_user_asset_ledger`
  (`uid`, `source`, `source_id`, `global_asset_id`, `item_key`, `visibility_status`,
   `container_type`, `container_key`, `container_title`, `title`, `kind`, `mime_type`,
   `size_bytes`, `width`, `height`, `url`, `thumb_url`, `preview_url`, `object_key`,
   `storage_path`, `is_attached`, `has_managed_object`, `created_at`, `updated_at`)
SELECT
  ga.uid,
  'reference_upload',
  ga.id,
  ga.id,
  '',
  1,
  'upload',
  'reference_upload',
  'reference uploads',
  COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.uploadId')), ''), ga.kind, 'reference-upload'),
  ga.kind,
  ga.mime_type,
  ga.size_bytes,
  ga.width,
  ga.height,
  ga.url,
  ga.thumb_url,
  CASE WHEN ga.mime_type LIKE 'image/%' THEN COALESCE(NULLIF(ga.thumb_url, ''), ga.url) ELSE ga.thumb_url END,
  COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.objectKey')), ''), ''),
  COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.objectKey')), ''), ga.url),
  0,
  CASE WHEN COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(ga.metadata, '$.objectKey')), ''), '') <> '' THEN 1 ELSE 0 END,
  ga.created_at,
  ga.updated_at
FROM `w_global_asset` ga
LEFT JOIN `w_user_asset_ledger` existing
  ON existing.uid = ga.uid
 AND existing.source = 'reference_upload'
 AND existing.source_id = ga.id
 AND existing.item_key = ''
WHERE ga.source_table = 'reference_upload'
  AND ga.status <> 4
  AND ga.deleted_at IS NULL
  AND existing.id IS NULL
ON DUPLICATE KEY UPDATE
  `global_asset_id` = VALUES(`global_asset_id`),
  `container_type` = VALUES(`container_type`),
  `container_key` = VALUES(`container_key`),
  `container_title` = VALUES(`container_title`),
  `title` = VALUES(`title`),
  `kind` = VALUES(`kind`),
  `mime_type` = VALUES(`mime_type`),
  `size_bytes` = VALUES(`size_bytes`),
  `width` = VALUES(`width`),
  `height` = VALUES(`height`),
  `url` = VALUES(`url`),
  `thumb_url` = VALUES(`thumb_url`),
  `preview_url` = VALUES(`preview_url`),
  `object_key` = VALUES(`object_key`),
  `storage_path` = VALUES(`storage_path`),
  `is_attached` = VALUES(`is_attached`),
  `has_managed_object` = VALUES(`has_managed_object`),
  `updated_at` = VALUES(`updated_at`);
