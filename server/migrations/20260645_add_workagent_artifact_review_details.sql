-- Store structured critique metadata alongside the human-readable review
-- summary so redo/version comparison can compute score deltas and top fixes.

ALTER TABLE `w_workagent_artifact`
  ADD COLUMN `review_details_json` text AFTER `review_summary`;
