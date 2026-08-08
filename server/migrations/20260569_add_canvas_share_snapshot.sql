-- Canvas share links should expose an explicit published snapshot, not
-- the owner's live autosave draft.

CREATE TABLE IF NOT EXISTS `w_canvas_share_snapshot` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  `project_id` bigint unsigned NOT NULL COMMENT '画布项目ID',
  `published_version` int NOT NULL DEFAULT 0 COMMENT '发布时的项目版本号',
  `thumbnail_url` longtext COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '发布缩略图',
  `document` json NOT NULL COMMENT '发布时冻结的画布文档',
  `schema_version` int NOT NULL COMMENT '发布文档schema版本',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_canvas_share_project` (`project_id`),
  KEY `idx_canvas_share_updated_at` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Canvas共享链接发布快照';

INSERT INTO `w_canvas_share_snapshot` (
  `created_at`,
  `updated_at`,
  `project_id`,
  `published_version`,
  `thumbnail_url`,
  `document`,
  `schema_version`
)
SELECT
  NOW(),
  NOW(),
  p.`id`,
  p.`latest_version`,
  p.`thumbnail_url`,
  p.`document`,
  COALESCE(p.`schema_version`, JSON_UNQUOTE(JSON_EXTRACT(p.`document`, '$.schemaVersion')), 2)
FROM `w_canvas_project` p
LEFT JOIN `w_canvas_share_snapshot` s ON s.`project_id` = p.`id`
WHERE p.`deleted_at` IS NULL
  AND p.`visibility` IN (1, 2)
  AND p.`document` IS NOT NULL
  AND s.`id` IS NULL;
