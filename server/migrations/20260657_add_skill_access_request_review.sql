ALTER TABLE `w_workagent_skill_access_request`
  ADD COLUMN `reviewed_by` bigint NOT NULL DEFAULT 0,
  ADD COLUMN `reviewed_at` datetime NULL,
  ADD COLUMN `review_note` varchar(512) NOT NULL DEFAULT '';

CREATE INDEX `idx_skill_access_reviewed_by`
  ON `w_workagent_skill_access_request` (`reviewed_by`);
