-- Adds w_credit_reservation — backing table for the in-flight
-- credit-debit ledger. Previously created implicitly by GORM
-- AutoMigrate (server/initialize/gorm.go RegisterDBTables); this
-- migration makes the schema explicit so it shows up in the
-- migration stream alongside every other table the platform owns.
--
-- One row per reservation, created atomically with the CreditsPack
-- debit. Reused on retries within the TTL window via the
-- (uid, idempotency_key) unique key, so the same client request
-- never double-charges. Status transitions to finalized / released
-- / expired when the downstream operation resolves; IsTerminal()
-- on the model gates short-circuit behaviour for repeat hits.
CREATE TABLE IF NOT EXISTS `w_credit_reservation` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `created_at`      datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at`      datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  `uid`             int NOT NULL COMMENT '用户ID',
  `tool`            varchar(64) NOT NULL COMMENT '工具标识（canvas_chat / canvas_agent / canvas_optimize_prompt / image_generate ...）',
  `idempotency_key` varchar(128) NOT NULL COMMENT '幂等键，通常为 quoteId 或客户端生成的 UUID',
  `quote_id`        varchar(128) DEFAULT NULL COMMENT '关联的 quoteId（可选）',
  `reserved`        int NOT NULL DEFAULT 0 COMMENT '预留积分数量',
  `used`            int NOT NULL DEFAULT 0 COMMENT '实际结算使用的积分（finalize 后写入）',
  `status`          varchar(16) NOT NULL DEFAULT 'reserved' COMMENT '状态 reserved/finalized/released/expired',
  `expires_at`      datetime NOT NULL COMMENT '过期时间',
  `finalized_at`    datetime DEFAULT NULL COMMENT '结算时间',
  `released_at`     datetime DEFAULT NULL COMMENT '释放/退回时间',
  `remark`          varchar(255) DEFAULT NULL COMMENT '备注',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_reservation_uid_key` (`uid`, `idempotency_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci COMMENT='积分预留（幂等 + 预扣结算）';
