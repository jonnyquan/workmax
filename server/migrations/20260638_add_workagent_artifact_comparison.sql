-- Store the latest comparison result for an artifact version.
-- The first producer is the Work Agent "Compare versions" prompt; future
-- visual diff runners can write into the same fields.

ALTER TABLE `w_workagent_artifact`
  ADD COLUMN `comparison_source` varchar(64) NOT NULL DEFAULT '' AFTER `review_summary`,
  ADD COLUMN `comparison_summary` text AFTER `comparison_source`,
  ADD COLUMN `comparison_decision` varchar(32) NOT NULL DEFAULT '' AFTER `comparison_summary`;
