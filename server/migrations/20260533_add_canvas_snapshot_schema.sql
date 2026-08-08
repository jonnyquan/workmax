-- M5-B-06 Stage 1: additive schema for the live + snapshot split
-- (canvas-version-restructure-design.md). This migration adds the
-- target columns + table without changing any read/write path —
-- writers still hit w_canvas_version, readers still JOIN through
-- latest_version_id. Stage 2 wires dual-write; Stage 3 backfills;
-- Stage 4 swaps reads; Stage 5 retires w_canvas_version.
--
-- Scope:
--
--   w_canvas_project gains three NULLable columns so dual-write can
--   populate them without forcing existing rows to backfill first:
--
--     document        JSON   — the live canvas document (currently
--                              held on w_canvas_version's "latest" row).
--     schema_version  INT    — mirrors EnsureSchemaVersion's stamp;
--                              redundant with the JSON-embedded value
--                              for now, promoted to a column so future
--                              indexed migration filters are cheap.
--     element_count   INT    — mirrors w_canvas_version.element_count
--                              for the live row.
--
--   w_canvas_snapshot is the new immutable table for "Save as new
--   version". One row per snapshot; PatchElements / autosave never
--   write here. snapshot_no is a monotonic per-project counter
--   (mirrors w_canvas_version.version semantics for snapshots only).
--
-- Reverting Stage 1: drop the three columns + the snapshot table.
-- No data loss — Stage 1 produces no rows in w_canvas_snapshot
-- (Stage 3's backfill is what populates it).

ALTER TABLE `w_canvas_project`
  ADD COLUMN `document` json DEFAULT NULL COMMENT 'M5-B-06 live document; populated from Stage 2',
  ADD COLUMN `schema_version` int DEFAULT NULL COMMENT 'M5-B-06 EnsureSchemaVersion stamp; populated from Stage 2',
  ADD COLUMN `element_count` int DEFAULT NULL COMMENT 'M5-B-06 live document element count; populated from Stage 2';

CREATE TABLE `w_canvas_snapshot` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `project_id` bigint unsigned NOT NULL COMMENT '所属画布项目ID',
  `snapshot_no` int NOT NULL COMMENT '项目内单调递增的快照号',
  `created_by` int NOT NULL COMMENT '创建者UID',
  `source` varchar(20) NOT NULL DEFAULT 'manual' COMMENT '快照来源: manual | web | api',
  `message` varchar(255) NOT NULL DEFAULT '' COMMENT '用户备注',
  `element_count` int NOT NULL DEFAULT 0 COMMENT '快照document的elements数量',
  `thumbnail_url` longtext NOT NULL COMMENT '缩略图URL',
  `document` json NOT NULL COMMENT '快照document JSON',
  `schema_version` int NOT NULL COMMENT 'EnsureSchemaVersion stamp at snapshot time',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_snapshot` (`project_id`, `snapshot_no`),
  KEY `idx_project_created` (`project_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='M5-B-06 immutable snapshot table — replaces w_canvas_version snapshot rows after Stages 2-5';
