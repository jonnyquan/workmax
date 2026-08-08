-- Adds w_location + w_location_reference — the scene/environment
-- counterpart to w_character. Motivation: shots that share a
-- location should look like the same location. Today, the only
-- anchor is free-text scene.location; two shots of "LOFT - NIGHT"
-- can drift badly across generations. Location assets give us the
-- same identity-anchor plumbing characters already have.
--
-- Shape intentionally mirrors w_character: same uid + nullable
-- project_id scope rules, same identity_anchors_json + negative_
-- anchors_json columns, same calibrated_at stamp, same source_kind
-- discriminator. The panel prompt resolver can then fold location
-- anchors into each shot using the exact same formatIdentityCue
-- helper it already uses for characters.
--
-- w_location_reference carries tagged images (aerial / interior /
-- exterior / detail / mood) just like character references — so the
-- cinematographer can attach boards the AI will anchor against.

CREATE TABLE `w_location` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uid` int NOT NULL DEFAULT 0 COMMENT '所属用户,0=全局',
  `project_id` bigint unsigned DEFAULT NULL COMMENT '项目归属,NULL=全局模板',
  `name` varchar(120) NOT NULL DEFAULT '' COMMENT '场景名',
  `slug` varchar(160) NOT NULL DEFAULT '' COMMENT '短链,跨项目复用',
  `description` text COMMENT '场景描述',
  `location_type` varchar(32) NOT NULL DEFAULT '' COMMENT '类型 interior/exterior/aerial/...',
  `time_of_day` varchar(32) NOT NULL DEFAULT '' COMMENT '默认时段 day/night/dawn/dusk/...',
  `atmosphere` varchar(255) NOT NULL DEFAULT '' COMMENT '氛围关键词',
  `visual_dna_json` json DEFAULT NULL COMMENT '结构化视觉特征',
  `prompt_suffix` text COMMENT '生成时自动附加的prompt',
  `negative_prompt` text COMMENT '默认负向prompt',
  `identity_anchors_json` json DEFAULT NULL COMMENT '结构化锚点(布局/材质/光影/色调/标志物/季节)',
  `negative_anchors_json` json DEFAULT NULL COMMENT '结构化负面提示词',
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
  COMMENT='场景/地点资产:跨镜头一致性';

CREATE TABLE `w_location_reference` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `location_id` bigint unsigned NOT NULL COMMENT '场景ID',
  `uid` int NOT NULL DEFAULT 0 COMMENT '所属用户',
  `image_url` varchar(2048) NOT NULL DEFAULT '',
  `reference_type` varchar(32) NOT NULL DEFAULT 'interior' COMMENT 'interior/exterior/aerial/detail/mood',
  `label` varchar(80) NOT NULL DEFAULT '',
  `sort_order` int NOT NULL DEFAULT 0,
  `metadata` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_location` (`location_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='场景参考图';
