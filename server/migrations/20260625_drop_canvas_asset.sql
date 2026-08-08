-- Canvas project file assets now live in w_global_asset with
-- source_table='canvas_project_file'. Backfill before dropping the legacy
-- table so old uploads remain available in the unified asset catalog.
SET @schema_name := DATABASE();

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.TABLES
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_canvas_asset'
    ),
    'INSERT INTO `w_global_asset`
      (`uid`, `project_id`, `uuid`, `kind`, `source`, `source_table`, `source_id`, `source_item_key`,
       `url`, `thumb_url`, `mime_type`, `size_bytes`, `status`, `visibility`, `variant_type`, `metadata`,
       `created_at`, `updated_at`, `deleted_at`)
     SELECT
       ca.uid,
       NULLIF(ca.project_id, 0),
       UUID(),
       CASE
         WHEN ca.mime_type LIKE ''video/%'' THEN ''video''
         WHEN ca.mime_type LIKE ''audio/%'' THEN ''audio''
         WHEN ca.mime_type = ''application/pdf'' THEN ''document''
         WHEN ca.kind IN (''image'', ''video'', ''audio'', ''document'', ''archive'') THEN ca.kind
         ELSE ''image''
       END,
       ''upload'',
       ''canvas_project_file'',
       COALESCE(ca.project_id, 0),
       CONCAT(''legacy:'', ca.id),
       COALESCE(ca.url, ''''),
       COALESCE(ca.thumb_url, ''''),
       COALESCE(ca.mime_type, ''''),
       COALESCE(ca.size_bytes, 0),
       CASE WHEN ca.deleted_at IS NULL THEN 1 ELSE 4 END,
       CASE WHEN COALESCE(ca.project_id, 0) > 0 THEN 2 ELSE 1 END,
       ''original'',
       JSON_OBJECT(''legacyCanvasAssetId'', ca.id),
       ca.created_at,
       ca.updated_at,
       ca.deleted_at
     FROM `w_canvas_asset` ca
     WHERE COALESCE(ca.url, '''') <> ''''
     ON DUPLICATE KEY UPDATE
       `url` = VALUES(`url`),
       `thumb_url` = VALUES(`thumb_url`),
       `mime_type` = VALUES(`mime_type`),
       `size_bytes` = VALUES(`size_bytes`),
       `status` = VALUES(`status`),
       `visibility` = VALUES(`visibility`),
       `metadata` = VALUES(`metadata`),
       `updated_at` = VALUES(`updated_at`),
       `deleted_at` = VALUES(`deleted_at`)',
    'SELECT 1'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS w_canvas_asset;
