-- Scope canvas-side WorkAgent threads to their owning Canvas project.
--
-- Canvas uses w_workagent_thread for Agent, direct image, and direct video
-- messages. Without project_id, the right panel can load the latest canvas
-- thread from another project owned by the same user.

ALTER TABLE `w_workagent_thread`
  ADD COLUMN `project_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT 'Canvas project id for canvas-surface threads' AFTER `uuid`;

CREATE INDEX `idx_workagent_thread_uid_project`
  ON `w_workagent_thread` (`uid`, `project_id`, `agent_type`, `updated_at`);
