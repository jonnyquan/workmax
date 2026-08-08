-- Adds w_director_preset for reusable cinematography templates.
--
-- Each preset bundles shot_type + camera_movement + composition
-- + duration_hint into one named handle a panel editor can apply
-- in a single click. Motivated by moyin-creator's director preset
-- system (shot scale / camera angle / movement as a unit).
--
-- Scope:
--   project_id NULL + uid NULL → built-in, visible to all users
--                                (but built-ins are hardcoded in Go;
--                                this branch exists for future
--                                "system-imported" presets).
--   project_id NULL + uid NOT NULL → personal preset (user library).
--   project_id NOT NULL          → project-scoped preset (shared
--                                with teammates via w_team_member).
--
-- Keep it nullable/scope-flexible so the UI can evolve without a
-- follow-up migration.

CREATE TABLE `w_director_preset` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uid` int NOT NULL DEFAULT 0 COMMENT '创建者,0表示系统预设',
  `project_id` bigint unsigned DEFAULT NULL COMMENT '项目ID,NULL表示个人/系统预设',
  `name` varchar(120) NOT NULL DEFAULT '' COMMENT '预设名,用户可见',
  `slug` varchar(120) NOT NULL DEFAULT '' COMMENT '稳定标识,跨项目复用',
  `description` varchar(500) NOT NULL DEFAULT '' COMMENT '一句话描述,UI tooltip',
  `shot_type` varchar(32) NOT NULL DEFAULT '' COMMENT '镜头类型 close-up/medium/wide/over-shoulder/insert/establishing',
  `camera_movement` varchar(32) NOT NULL DEFAULT '' COMMENT '运镜 static/pan-left/dolly-in/crane/tracking/...',
  `composition` varchar(255) NOT NULL DEFAULT '' COMMENT '构图提示,约100字',
  `duration_hint` int NOT NULL DEFAULT 0 COMMENT '建议时长(秒),0表示不约束',
  `tags_json` json DEFAULT NULL COMMENT '风格/场景标签,用于分组筛选',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=可用 2=归档',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_uid` (`uid`),
  KEY `idx_project` (`project_id`),
  KEY `idx_slug` (`slug`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='导演预设:镜头/运镜/构图/时长的命名组合';
