-- U3 — append-only script revision log.
--
-- Problem: w_drama_script overwrites scenes_json on every AI generate,
-- manual save, or rewrite. Once a user edits a scene poorly there's
-- no path back to the AI's prior take. Exports already have versioning
-- (w_drama_episode_export_version + restore endpoint); scripts didn't.
--
-- Fix: snapshot every script write into this table. version aligns
-- 1:1 with the post-write `script.generation_version` so consumers
-- can pivot between the two without a join. Restore copies a
-- revision's scenes_json into a NEW row at the next version, keeping
-- generation_version monotonic and history append-only.
--
-- source values:
--   - ai_generate: full-script regeneration (LLM round trip)
--   - manual_save: user-authored save from the editor
--   - restore:     user clicked "restore version N" — payload is N's
--                  scenes_json, version is the new (incremented) one
--
-- Backfill: existing scripts get NO seed row. The UI shows "history
-- starts here" and offers no restore until the next write happens.
-- This keeps the migration fast (no JSON copy across millions of rows)
-- and the absence is detectable via "no rows for this episode_id".

CREATE TABLE IF NOT EXISTS `w_drama_script_revision` (
  `id`           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `episode_id`   BIGINT UNSIGNED NOT NULL,
  `uid`          INT             NOT NULL DEFAULT 0,
  `version`      INT             NOT NULL DEFAULT 1,
  `scenes_json`  MEDIUMTEXT,
  `scene_count`  INT             NOT NULL DEFAULT 0,
  `source`       VARCHAR(32)     NOT NULL DEFAULT ''
                  COMMENT 'ai_generate | manual_save | restore',
  `created_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP
                  ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_episode_revision` (`episode_id`, `version`),
  KEY `idx_uid` (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='短剧脚本版本历史; 每次写入插入一行';
