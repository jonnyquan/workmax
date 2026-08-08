-- SD-S0.3: append-only activity log for episode-level edits.
--
-- Why: pre-S0.3 the platform had no record of who did what when on an
-- episode. Two team members could overwrite each other's edits silently,
-- and the only signal a user had was "the row updated_at moved". The
-- activity log makes every meaningful write visible — script generate /
-- lock / unlock, storyboard generate / lock, per-panel rewrites, shot
-- record / select / delete, export complete, character calibrate apply.
--
-- Append-only (no UPDATE / DELETE) so the audit trail is a true history,
-- not a mutable view. Soft-delete via deleted_at is intentionally absent —
-- when an episode is deleted, downstream readers filter by the parent
-- episode's deleted_at, leaving the activity rows untouched as historical
-- record.
--
-- Surfaced by the workspace UI (S2.1) as the "recent activity" sidebar.

CREATE TABLE `w_drama_episode_activity` (
  `id`           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `episode_id`   BIGINT UNSIGNED NOT NULL COMMENT 'FK→w_drama_episode.id',
  `project_id`   BIGINT UNSIGNED NOT NULL COMMENT 'FK→w_drama_project.id; denormalised so the project-level view doesn''t join',
  `actor_uid`    INT             NOT NULL DEFAULT 0       COMMENT 'who performed the action; 0 = system / migration',
  `action`       VARCHAR(64)     NOT NULL                 COMMENT 'dotted action name (script.locked, panel.shot_recorded, …)',
  `target_type`  VARCHAR(32)     NOT NULL DEFAULT ''      COMMENT 'kind of target (script / storyboard / panel / shot / character / export_version / episode)',
  `target_id`    VARCHAR(64)     NOT NULL DEFAULT ''      COMMENT 'string-form id (uuid / numeric / composite); avoids forced FK type coupling',
  `diff_json`    JSON            DEFAULT NULL             COMMENT 'structured before/after or summary fields specific to the action',
  `desc_text`    VARCHAR(500)    NOT NULL DEFAULT ''      COMMENT 'optional human-readable summary; UI falls back to action+target when empty',
  `created_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_episode_created` (`episode_id`, `created_at`),
  KEY `idx_project_created` (`project_id`, `created_at`),
  KEY `idx_actor`           (`actor_uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='短剧 episode 级 append-only 变更流水';
