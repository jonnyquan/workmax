-- Persist Canvas direct-generation recovery metadata.
--
-- Canvas image/video chat turns share the WorkAgent message thread. If the
-- page refreshes before the placeholder element autosaves, task recovery needs
-- these ids from the backend binding row to write the completed assistant
-- message back into the same thread.

ALTER TABLE `w_canvas_task_binding`
  ADD COLUMN `generation_run_id` varchar(64) NOT NULL DEFAULT '' COMMENT 'Canvas chat direct generation run id' AFTER `element_id`,
  ADD COLUMN `generation_thread_id` varchar(64) NOT NULL DEFAULT '' COMMENT 'Canvas chat/workagent thread uuid for direct generation' AFTER `generation_run_id`;

-- Durable idempotency for direct Canvas image/video chat messages. NULL keeps
-- ordinary messages out of the unique key while allowing retry-safe upserts for
-- messages carrying a clientRunId.
ALTER TABLE `w_workagent_message`
  ADD COLUMN `message_idempotency_key` varchar(180) NULL COMMENT 'Canvas direct-message idempotency key' AFTER `metadata`;

CREATE UNIQUE INDEX `uk_workagent_message_thread_idem`
  ON `w_workagent_message` (`thread_id`, `uid`, `message_idempotency_key`);
