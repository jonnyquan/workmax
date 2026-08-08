-- 20260538: w_ecommerce_render — render artifact accumulator for
-- ecommerce_sku_batch typed handler (gap #1 step 4 / 5).
--
-- One row per successful render produced by the SKU batch
-- pipeline. The handler creates rows on completion only —
-- failures land in BatchItem.error_msg, not here, so the table
-- stays clean (no broken-candidate rows polluting the editor's
-- "renders for product X" listing).
--
-- Why a separate table from w_ecommerce_video:
-- - w_ecommerce_video is the SCRIPT artifact (one per concept,
--   carries script_json + end_card_json).
-- - w_ecommerce_render is the RENDER artifact (one per
--   (product × videoType × aspect × language) tuple from a batch
--   submit).
-- The script flow + render flow are independent: scripts can
-- exist without renders (Phase 2 manual creation) and renders can
-- exist without scripts (this batch path produces video output
-- directly from product + selling points).
--
-- batch_item_id is the link back to w_batch_item for ops
-- traceability (which batch run produced this render). Indexed
-- for retry-aware dedupe in the handler.

CREATE TABLE `w_ecommerce_render` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL DEFAULT '',
  `uid` int NOT NULL,
  `project_id` bigint unsigned DEFAULT NULL COMMENT 'NULL=ad-hoc render outside any campaign',
  `product_id` bigint unsigned NOT NULL,
  `video_type` varchar(32) NOT NULL DEFAULT 'showcase',
  `aspect_slug` varchar(32) NOT NULL DEFAULT '9x16',
  `language` varchar(16) NOT NULL DEFAULT 'zh' COMMENT 'ISO 639-1',
  `batch_item_id` bigint unsigned DEFAULT NULL COMMENT 'w_batch_item.id — ops traceability',
  `output_url` varchar(2048) NOT NULL DEFAULT '',
  `thumbnail_url` varchar(2048) NOT NULL DEFAULT '',
  `size_bytes` bigint NOT NULL DEFAULT 0,
  `duration_ms` int NOT NULL DEFAULT 0,
  `credits_used` int NOT NULL DEFAULT 0,
  `created_at` datetime DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ecomm_render_uuid` (`uuid`),
  KEY `idx_uid` (`uid`),
  KEY `idx_product` (`product_id`),
  KEY `idx_project` (`project_id`),
  KEY `idx_batch_item` (`batch_item_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Ecommerce SKU batch render output (gap #1 step 4)';
