-- P0-042 - append-only positive-finalize Settlement Review resolution.
--
-- Resolution is deliberately narrower than a general adjudication workflow:
-- it records only a strictly positive finalize against the exact immutable
-- Review evidence. Effects remain in review_hold after financial resolution;
-- this migration does not authorize delivery, discard, release or traffic.

ALTER TABLE `w_agent_turn_settlement_review`
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_status`,
  ADD UNIQUE KEY `uk_w_agent_turn_settlement_review_resolution_binding`
    (`review_id`, `turn_id`, `settlement_key`, `request_digest`),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_status`
    CHECK (`status` IN ('pending', 'finalized_held'));

CREATE TABLE IF NOT EXISTS `w_agent_turn_settlement_review_resolution` (
  `id`                         bigint unsigned NOT NULL AUTO_INCREMENT,
  `resolution_id`              varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                                 COMMENT 'Opaque server-generated immutable resolution identity',
  `review_id`                  varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `turn_id`                    varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `settlement_key`             varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `review_request_digest`      varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                                 COMMENT 'Exact immutable Review request digest being resolved',
  `decision_digest`            varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `resolution_digest`          varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `intent`                     varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'finalize',
  `used_units`                 bigint unsigned NOT NULL,
  `reserved_units`             bigint unsigned NOT NULL,
  `actor_id`                   varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `reason`                     varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                                 DEFAULT 'metered_usage_confirmed',
  `evidence_digest`            varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `authority_receipt_digest`   varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `created_at`                 datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_review_resolution_resolution_id` (`resolution_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_review_resolution_review_id` (`review_id`),
  KEY `idx_w_agent_turn_settlement_review_resolution_binding`
    (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`),
  KEY `idx_w_agent_turn_settlement_review_resolution_turn`
    (`turn_id`, `created_at`, `id`),
  CONSTRAINT `chk_w_agent_turn_settlement_review_resolution_identity_bytes`
    CHECK (
      OCTET_LENGTH(`resolution_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`review_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`settlement_key`) BETWEEN 1 AND 256
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_review_resolution_digest_bytes`
    CHECK (
      OCTET_LENGTH(`review_request_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`decision_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`resolution_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`evidence_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`authority_receipt_digest`) BETWEEN 1 AND 128
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_review_resolution_actor_bytes`
    CHECK (OCTET_LENGTH(`actor_id`) BETWEEN 1 AND 256),
  CONSTRAINT `chk_w_agent_turn_settlement_review_resolution_intent`
    CHECK (`intent` = 'finalize'),
  CONSTRAINT `chk_w_agent_turn_settlement_review_resolution_units`
    CHECK (
      `used_units` BETWEEN 1 AND 9223372036854775807
      AND `reserved_units` BETWEEN `used_units` AND 9223372036854775807
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_review_resolution_reason`
    CHECK (`reason` = 'metered_usage_confirmed'),
  CONSTRAINT `fk_w_agent_turn_settlement_review_resolution_review`
    FOREIGN KEY (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`)
    REFERENCES `w_agent_turn_settlement_review`
      (`review_id`, `turn_id`, `settlement_key`, `request_digest`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Append-only positive-finalize receipts for held Agent Turn settlement reviews';
