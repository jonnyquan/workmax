ALTER TABLE `w_workagent_knowledge_document`
  ADD COLUMN `review_status` varchar(32) NOT NULL DEFAULT 'pending' COMMENT 'pending/approved/rejected' AFTER `agent_mode`,
  ADD COLUMN `reviewed_by` bigint NOT NULL DEFAULT 0 COMMENT 'review admin uid' AFTER `review_status`,
  ADD COLUMN `reviewed_at` datetime DEFAULT NULL COMMENT 'review time' AFTER `reviewed_by`,
  ADD COLUMN `review_note` varchar(512) NOT NULL DEFAULT '' COMMENT 'review note' AFTER `reviewed_at`,
  ADD KEY `idx_workagent_knowledge_document_review_status` (`review_status`),
  ADD KEY `idx_workagent_knowledge_document_reviewed_by` (`reviewed_by`);
