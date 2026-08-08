-- PanelShot — one row per generated video candidate for a storyboard panel.
-- Replaces the flattened videoUrl/videoTaskId/videoModel on the panel JSON
-- for full history / multi-candidate workflows. The panel still caches the
-- currently-selected shot's URL on the storyboard's panels_json blob for
-- fast read; w_panel_shot is the authoritative log.
--
-- Named w_panel_shot (not w_shot) to avoid collision with the existing
-- canvas Shot table which represents promoted canvas cards.

CREATE TABLE IF NOT EXISTS `w_panel_shot` (
  `id`                  bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`          datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`          datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`          datetime DEFAULT NULL,
  `uid`                 int NOT NULL DEFAULT 0,
  `solution_type`       varchar(32) NOT NULL DEFAULT 'short_drama',
  `project_id`          bigint unsigned NOT NULL,
  `episode_id`          bigint unsigned NOT NULL,
  `storyboard_id`       bigint unsigned NOT NULL,
  `panel_uuid`          varchar(36) NOT NULL DEFAULT '' COMMENT '稳定面板ID,跨重排生效',
  `panel_number`        int NOT NULL DEFAULT 0 COMMENT '生成时的面板序号快照,用于审计',
  `model`               varchar(64) NOT NULL DEFAULT '' COMMENT '视频模型如kling-2.6/veo-3.1',
  `task_id`             varchar(128) NOT NULL DEFAULT '' COMMENT '生成器task_id,便于对账',
  `video_url`           varchar(2048) NOT NULL DEFAULT '',
  `thumbnail_url`       varchar(2048) NOT NULL DEFAULT '',
  `duration_sec`        int NOT NULL DEFAULT 0,
  `prompt`              text COMMENT '生成时的prompt快照',
  `reference_image_url` varchar(2048) NOT NULL DEFAULT '' COMMENT '生成时的参考图URL',
  `credits_used`        int NOT NULL DEFAULT 0,
  `selected`            tinyint(1) NOT NULL DEFAULT 0 COMMENT '1=当前选中候选',
  `score`               int NOT NULL DEFAULT 0 COMMENT '人工评分0-100,可选',
  `status`              tinyint NOT NULL DEFAULT 3 COMMENT '1=pending 2=processing 3=completed 4=failed',
  PRIMARY KEY (`id`),
  KEY `idx_panel_uuid` (`panel_uuid`),
  KEY `idx_storyboard` (`storyboard_id`),
  KEY `idx_episode_uid` (`episode_id`, `uid`),
  KEY `idx_task_id` (`task_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
