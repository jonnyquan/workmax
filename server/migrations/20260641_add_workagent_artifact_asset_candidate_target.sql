-- Track the typed asset produced from a confirmed artifact asset candidate.
-- Initial writer only materializes reference candidates into w_global_asset;
-- stronger typed libraries are filled by later kind-specific writers.

ALTER TABLE `w_workagent_artifact_asset_candidate`
  ADD COLUMN `target_kind` varchar(32) NOT NULL DEFAULT '' AFTER `status`,
  ADD COLUMN `target_id` bigint unsigned NOT NULL DEFAULT 0 AFTER `target_kind`,
  ADD KEY `idx_workagent_artifact_asset_candidate_target` (`uid`, `target_kind`, `target_id`);
