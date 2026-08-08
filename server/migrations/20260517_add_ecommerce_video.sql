-- Adds w_ecommerce_video — one row per generated ecommerce video,
-- child of w_product_asset. Multiple videos per product (different
-- platforms, languages, video-types) so the table hangs off
-- product_id rather than replacing fields on the product row.
--
-- script_json holds the scenes[] + endCard the LLM generates.
-- Shape (scene is one beat):
--   {
--     "scenes": [
--       { "sceneNumber", "durationSec", "visual", "productShot",
--         "onScreenText", "voiceover", "transition" }
--     ],
--     "endCard": {
--       "priceDisplay", "discountDisplay", "cta", "qrCode": bool
--     }
--   }
-- Mirrors the AdScript pattern from video-ad but with
-- productShot + endCard (ad-specific hooks) replaced by
-- product-specific field names.

CREATE TABLE `w_ecommerce_video` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL DEFAULT '' COMMENT '对外UUID',
  `product_id` bigint unsigned NOT NULL COMMENT '关联商品',
  `project_id` bigint unsigned DEFAULT NULL COMMENT '冗余便于项目筛选',
  `uid` int NOT NULL COMMENT '创建者',
  `title` varchar(200) NOT NULL DEFAULT '' COMMENT '视频名',
  `video_type` varchar(32) NOT NULL DEFAULT 'showcase'
    COMMENT 'showcase/demo/scene/unboxing/compare/promo/testimonial',
  `target_platform` varchar(64) NOT NULL DEFAULT '' COMMENT '主投放平台',
  `duration_target` int NOT NULL DEFAULT 15 COMMENT '目标时长秒',
  `language` varchar(16) NOT NULL DEFAULT 'zh' COMMENT 'ISO 639-1 lang code',
  `script_json` json DEFAULT NULL COMMENT '生成的分镜+endCard',
  `end_card_json` json DEFAULT NULL COMMENT '片尾信息 (redundant with script.endCard for筛选)',
  `script_version` int NOT NULL DEFAULT 0 COMMENT '脚本生成次数',
  `status` tinyint NOT NULL DEFAULT 1
    COMMENT '1=draft 2=scripting 3=generating 4=reviewing 5=approved',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ecomm_video_uuid` (`uuid`),
  KEY `idx_product_status` (`product_id`, `status`),
  KEY `idx_project` (`project_id`),
  KEY `idx_uid` (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='电商视频 — 一个商品可产出多个视频(不同平台/语种/类型)';
