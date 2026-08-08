ALTER TABLE `w_workagent_knowledge_document`
  ADD COLUMN `scope_type` varchar(32) NOT NULL DEFAULT 'global' COMMENT 'global/project/team' AFTER `metadata_json`,
  ADD COLUMN `scope_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT 'scope entity id' AFTER `scope_type`,
  ADD COLUMN `agent_mode` varchar(64) NOT NULL DEFAULT '' COMMENT 'optional Work Agent mode filter' AFTER `scope_id`,
  ADD KEY `idx_workagent_knowledge_document_scope` (`scope_type`, `scope_id`),
  ADD KEY `idx_workagent_knowledge_document_agent_mode` (`agent_mode`);
