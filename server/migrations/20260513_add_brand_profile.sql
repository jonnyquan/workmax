-- Adds w_brand_profile + w_brand_reference — the brand-identity
-- counterpart to character/location assets. Declared as step 4 in
-- vertical-solutions-overview §6.4. Serves video-ad + ecommerce +
-- future enterprise-promo; each of those pipelines will resolve
-- `project.brand_profile_id` (added in a later migration when
-- needed) and fold its anchors into every shot.
--
-- Structurally parallel to w_character and w_location so the
-- panel-prompt resolver can fold brand anchors using the same
-- formatIdentityCue helper path. Scope rules match:
--   uid=0 + project_id=NULL → system/template brand
--   uid!=0 + project_id=NULL → personal brand library
--   project_id!=NULL → project-scoped brand (shared via team)
--
-- The 6-layer identity schema breaks a brand into:
--   visual_identity (logo, wordmark, iconography)
--   color_system    (primary/secondary/accent palette as hex)
--   typography      (font families + weights + usage rules)
--   tone_voice      (brand voice keywords, formal/casual descriptors)
--   compliance_rules (required disclaimers, legal copy, certifications)
--   forbidden       (banned words, banned visuals, competitor mentions)
--
-- Stored as JSON columns so new sub-keys can evolve without a
-- migration. The resolver handles missing keys gracefully — an
-- under-calibrated brand still anchors on whatever layers are set.

CREATE TABLE `w_brand_profile` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uid` int NOT NULL DEFAULT 0 COMMENT '所属用户,0=系统模板',
  `project_id` bigint unsigned DEFAULT NULL COMMENT '项目归属,NULL=个人/系统',
  `name` varchar(120) NOT NULL DEFAULT '' COMMENT '品牌名',
  `slug` varchar(160) NOT NULL DEFAULT '' COMMENT '短链,跨项目复用',
  `description` text COMMENT '品牌一句话描述',
  `industry` varchar(64) NOT NULL DEFAULT '' COMMENT '行业 consumer/b2b/fashion/...',
  `logo_url` varchar(2048) NOT NULL DEFAULT '' COMMENT '主 logo URL',
  `visual_identity_json` json DEFAULT NULL COMMENT '视觉锚点 (logo/wordmark/iconography)',
  `color_system_json` json DEFAULT NULL COMMENT '色彩系统 (primary/secondary/accent hex)',
  `typography_json` json DEFAULT NULL COMMENT '字体系统 (family/weight/rules)',
  `tone_voice_json` json DEFAULT NULL COMMENT '品牌语气 (keywords, formal/casual)',
  `compliance_rules_json` json DEFAULT NULL COMMENT '合规规则 (disclaimers, legal)',
  `forbidden_json` json DEFAULT NULL COMMENT '禁忌 (words, visuals, competitors)',
  `prompt_suffix` text COMMENT '生成时自动附加的prompt',
  `negative_prompt` text COMMENT '默认负向prompt',
  `calibrated_at` datetime DEFAULT NULL COMMENT '上次AI校准时间',
  `source_kind` varchar(32) NOT NULL DEFAULT 'manual' COMMENT '来源 manual/ai_calibrated/template',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=可用 2=归档',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_project` (`project_id`),
  KEY `idx_slug` (`slug`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='品牌资产:跨方案视觉+语气+合规一致性';

CREATE TABLE `w_brand_reference` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `brand_id` bigint unsigned NOT NULL COMMENT '品牌ID',
  `uid` int NOT NULL DEFAULT 0,
  `image_url` varchar(2048) NOT NULL DEFAULT '',
  `reference_type` varchar(32) NOT NULL DEFAULT 'logo' COMMENT 'logo/wordmark/color_swatch/typography_sample/mood',
  `label` varchar(80) NOT NULL DEFAULT '',
  `sort_order` int NOT NULL DEFAULT 0,
  `metadata` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_brand` (`brand_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='品牌参考素材';
