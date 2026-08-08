-- Phase 5: A/B test entity for video-ad creatives.
--
-- One row per test plan. A test is a named comparison of N creatives
-- (the variants live in w_ad_test_variant) running through the
-- draft → live → concluded lifecycle. Platform tracks the test;
-- it does NOT drive the actual ad-platform traffic split — KPIs
-- are entered manually from external dashboards (Phase 5 Option B).
--
-- See docs/superpowers/specs/2026-04-28-video-ad-creative-editor-phase5-design.md §3.2

CREATE TABLE `w_ad_test` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `uuid` VARCHAR(36) NOT NULL DEFAULT '' COMMENT 'external handle',
  `project_id` BIGINT UNSIGNED NOT NULL COMMENT 'fk w_project (campaign)',
  `uid` INT NOT NULL DEFAULT 0 COMMENT 'creator',
  `name` VARCHAR(160) NOT NULL DEFAULT '' COMMENT 'test name',
  `hypothesis` TEXT COMMENT 'why am I running this test',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '1=draft 2=live 3=concluded',
  `winner_variant_id` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'fk w_ad_test_variant.id, set on conclude',
  `concluded_note` TEXT COMMENT 'post-mortem',
  `launched_at` DATETIME DEFAULT NULL,
  `concluded_at` DATETIME DEFAULT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME DEFAULT NULL,
  UNIQUE KEY `uk_test_uuid` (`uuid`),
  KEY `idx_project_status` (`project_id`, `status`),
  KEY `idx_uid` (`uid`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='video-ad A/B test plans';
