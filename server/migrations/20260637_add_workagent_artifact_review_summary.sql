-- Attach the latest checklist/critique findings to artifact lifecycle state.
-- This makes artifact redo prompts actionable without re-reading done-event
-- payloads from chat history.

ALTER TABLE `w_workagent_artifact`
  ADD COLUMN `review_source` varchar(64) NOT NULL DEFAULT '' AFTER `review_state`,
  ADD COLUMN `review_summary` text AFTER `review_source`;
