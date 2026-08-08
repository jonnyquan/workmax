CREATE TABLE IF NOT EXISTS `w_workagent_knowledge_chunk` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  `document_id` bigint unsigned NOT NULL COMMENT 'w_workagent_knowledge_document.id',
  `chunk_index` int NOT NULL COMMENT 'zero-based chunk order',
  `content_text` text COMMENT 'chunk text',
  `content_hash` varchar(64) NOT NULL DEFAULT '' COMMENT 'sha256 of chunk text',
  `token_count` int NOT NULL DEFAULT 0 COMMENT 'estimated chunk token count',
  `metadata_json` text COMMENT 'JSON metadata for citation filters',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_workagent_knowledge_chunk_doc_index` (`document_id`, `chunk_index`),
  KEY `idx_workagent_knowledge_chunk_document_id` (`document_id`),
  KEY `idx_workagent_knowledge_chunk_content_hash` (`content_hash`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Work Agent knowledge-base text chunks';
