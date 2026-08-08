-- Track AI outline generation metadata on the short-drama project row.
-- Needed by POST /api/short-drama/:id/outline/generate so the UI can show
-- "last generated at" and so re-runs produce a monotonic version for audit.

ALTER TABLE `w_short_drama_project`
  ADD COLUMN `outline_generated_at` datetime DEFAULT NULL COMMENT '最近一次AI大纲生成时间',
  ADD COLUMN `outline_version` int NOT NULL DEFAULT 0 COMMENT '大纲版本号,每次AI生成+1';
