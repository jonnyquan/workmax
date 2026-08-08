-- Video-ad Phase 2 variant matrix. Each row is one
-- style × duration × aspect × language permutation of a Creative.
-- Created in bulk by the /variants/matrix endpoint, dispatched via
-- the batch engine (#138) so N variants render in one tracked job.
--
-- Denormalised campaign_id + creative_id both on the row so:
--   - "list variants for campaign" is one index scan
--   - "list variants for creative" is another index scan
-- without joining three tables every time the dashboard paints.
--
-- naming_code follows video-ad-solution.md §6:
--   {brand}_{creative}_{style}_{duration}s_{aspect}_{lang}_{v}
-- Stored on the row so the ad ops team can search / filter by the
-- code they see in their delivery platform.
--
-- batch_item_id links back to w_batch_item. When the batch engine
-- completes an item, the worker writes output_url + status on the
-- variant row and the UI paints progress.

CREATE TABLE `w_ad_variant` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uid` int NOT NULL,
  `creative_id` bigint unsigned NOT NULL COMMENT '源创意',
  `campaign_id` bigint unsigned NOT NULL COMMENT '冗余便于按campaign筛选',
  `style` varchar(32) NOT NULL DEFAULT '' COMMENT 'realistic/illustration/motion_graphics',
  `duration_sec` int NOT NULL DEFAULT 15 COMMENT '时长秒,6/15/30/60',
  `aspect_slug` varchar(16) NOT NULL DEFAULT '' COMMENT '9x16/1x1/16x9/...',
  `language` varchar(16) NOT NULL DEFAULT 'zh' COMMENT 'ISO 639-1',
  `naming_code` varchar(200) NOT NULL DEFAULT '' COMMENT '自动生成的命名规范',
  `batch_item_id` bigint unsigned DEFAULT NULL COMMENT '关联的w_batch_item',
  `output_url` varchar(2048) NOT NULL DEFAULT '' COMMENT '完成后的视频URL',
  `thumbnail_url` varchar(2048) NOT NULL DEFAULT '' COMMENT '预览缩略图',
  `size_bytes` bigint NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1
    COMMENT '1=pending 2=generating 3=ready 4=failed 5=approved 6=rejected',
  `error_msg` text COMMENT '失败信息',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_creative_status` (`creative_id`, `status`),
  KEY `idx_campaign` (`campaign_id`),
  KEY `idx_batch_item` (`batch_item_id`),
  KEY `idx_uid` (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='视频广告变体 — Creative的style×duration×aspect×lang排列';
