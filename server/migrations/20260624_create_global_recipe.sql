-- Minimal reusable recipe catalog.
--
-- Recipes are generic tool/workflow presets. This first table keeps only the
-- current publishable row per key; avoid adding separate recipe-version or
-- vertical-solution tables until a concrete product flow needs them.

CREATE TABLE IF NOT EXISTS `w_global_recipe` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`  datetime DEFAULT NULL,

  `recipe_key`  varchar(128) NOT NULL,
  `name`        varchar(128) NOT NULL DEFAULT '',
  `description` varchar(500) NOT NULL DEFAULT '',
  `tool_scope`  varchar(64) NOT NULL DEFAULT '',
  `media_type`  varchar(20) NOT NULL DEFAULT '',
  `status`      tinyint NOT NULL DEFAULT 1,
  `sort_order`  int NOT NULL DEFAULT 0,
  `content`     json DEFAULT NULL,
  `metadata`    json DEFAULT NULL,

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_global_recipe_key` (`recipe_key`),
  KEY `idx_global_recipe_scope_status` (`tool_scope`, `status`),
  KEY `idx_global_recipe_media` (`media_type`),
  KEY `idx_global_recipe_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
