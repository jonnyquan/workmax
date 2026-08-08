-- Adds Video-Ad solution foundation (step 5 in solutions-overview
-- §6.4). Two parts:
--
--   1. Project extensions — ad-campaign-specific columns that only
--      apply when solution_type='video_ad'. Kept on w_project rather
--      than a separate w_ad_campaign table because every campaign
--      IS a project and creating a parallel table would just fork
--      the ownership/team-scope plumbing.
--
--   2. w_ad_creative — child table holding per-concept creatives
--      inside an ad campaign. One campaign can have multiple
--      creatives (alternate concepts for A/B testing); each
--      creative carries its own AdScript as JSON (pattern parallel
--      to w_storyboard.panels_json).
--
-- Variant matrix + compliance-check tables are Phase 2 (blocked by
-- #138 batch engine) and land in a later migration.

ALTER TABLE `w_project`
  ADD COLUMN `brand_profile_id` bigint unsigned DEFAULT NULL
    COMMENT '关联品牌 (video_ad/ecommerce),NULL=未设',
  ADD COLUMN `objective` varchar(32) NOT NULL DEFAULT ''
    COMMENT '广告目标 awareness/consideration/conversion',
  ADD COLUMN `target_platforms_json` json DEFAULT NULL
    COMMENT '投放平台数组 [tiktok, instagram, ...]',
  ADD COLUMN `target_audience` varchar(500) NOT NULL DEFAULT ''
    COMMENT '目标人群描述';

CREATE INDEX `idx_brand_profile` ON `w_project` (`brand_profile_id`);

CREATE TABLE `w_ad_creative` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL DEFAULT '' COMMENT '对外UUID',
  `project_id` bigint unsigned NOT NULL COMMENT 'AdCampaign ID',
  `uid` int NOT NULL DEFAULT 0 COMMENT '创建者',
  `title` varchar(160) NOT NULL DEFAULT '' COMMENT '创意名',
  `concept` text COMMENT '核心创意叙述',
  `structure` varchar(64) NOT NULL DEFAULT '' COMMENT '结构模板 hook_ptf_cta/story_brand/demo_cta/...',
  `hook_line` varchar(300) NOT NULL DEFAULT '' COMMENT '前 3 秒钩子文案',
  `cta_line` varchar(200) NOT NULL DEFAULT '' COMMENT '行动号召文案',
  `duration_target` int NOT NULL DEFAULT 15 COMMENT '目标时长秒,6/15/30/60',
  `platform_target` varchar(64) NOT NULL DEFAULT '' COMMENT '主投放平台',
  `script_json` json DEFAULT NULL COMMENT 'AdScript (scenes[], 每场 visual/voiceover/text/cta)',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=draft 2=scripting 3=generating 4=reviewing 5=approved',
  `script_version` int NOT NULL DEFAULT 0 COMMENT '脚本生成次数',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_creative_uuid` (`uuid`),
  KEY `idx_project_status` (`project_id`, `status`),
  KEY `idx_uid` (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='广告创意:每条为campaign下一个alternate concept';
