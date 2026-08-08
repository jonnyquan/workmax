-- Phase 5: variant rows linking a w_ad_test to its candidate
-- AdCreatives. One row per (test, creative) participation.
--
-- Unique on (test_id, creative_id, deleted_at) — a creative can't
-- appear twice in one active test. Unique on (test_id, label,
-- deleted_at) — labels (A/B/Control/etc.) are unique within a test.
-- traffic_weight is informational only (the platform doesn't drive
-- the actual ad-platform split); summing to 100 across active rows
-- is conventional but not enforced.
--
-- kpi_data holds the manual KPI entry as JSON. Schema is fixed
-- top-level fields + a `custom` bag for platform-specific metrics.
-- See spec §3.3.

CREATE TABLE `w_ad_test_variant` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `test_id` BIGINT UNSIGNED NOT NULL COMMENT 'fk w_ad_test',
  `creative_id` BIGINT UNSIGNED NOT NULL COMMENT 'fk w_ad_creative',
  `label` VARCHAR(40) NOT NULL DEFAULT 'A' COMMENT 'A/B/Control — minted, user-editable',
  `position` INT NOT NULL DEFAULT 0 COMMENT 'sort order in compare view',
  `traffic_weight` INT NOT NULL DEFAULT 0 COMMENT '0-100, informational',
  `kpi_data` JSON COMMENT 'fixed top-level + custom bag',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME DEFAULT NULL,
  UNIQUE KEY `uk_test_creative` (`test_id`, `creative_id`, `deleted_at`),
  UNIQUE KEY `uk_test_label` (`test_id`, `label`, `deleted_at`),
  KEY `idx_creative` (`creative_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='video-ad A/B test variants';
