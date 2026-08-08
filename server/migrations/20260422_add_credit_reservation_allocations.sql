CREATE TABLE IF NOT EXISTS `w_credit_reservation_allocation` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  `reservation_id` bigint unsigned NOT NULL COMMENT '预留记录ID',
  `pack_id` bigint unsigned NOT NULL COMMENT '积分包ID',
  `credits` int NOT NULL DEFAULT '0' COMMENT '本包承担的积分数',
  PRIMARY KEY (`id`),
  KEY `idx_credit_reservation_allocation_reservation` (`reservation_id`),
  KEY `idx_credit_reservation_allocation_pack` (`pack_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci COMMENT='积分预留分摊明细';
