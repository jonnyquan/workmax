-- Mark why an artifact is attached to a parent artifact.
--
-- parent_artifact_id is shared by revision chains and derived reports
-- such as visual diffs. artifact_relation lets clients distinguish
-- "next version" from "comparison report" without inferring from names.

ALTER TABLE `w_workagent_artifact`
  ADD COLUMN `artifact_relation` varchar(32) NOT NULL DEFAULT '' AFTER `parent_artifact_id`,
  ADD KEY `idx_workagent_artifact_relation` (`uid`, `thread_id`, `artifact_relation`, `updated_at`);
