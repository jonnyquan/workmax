-- P0-041 - durable ambiguous-settlement review and retry isolation.
--
-- A release with durable Operation/Effect evidence cannot safely assert zero
-- usage. The kernel records that ambiguity once per Turn and SettlementKey so
-- execution is terminalized while Effect delivery remains fenced until an
-- authorised manual or metered adjudication path is introduced. This migration is schema-only;
-- it does not install that adjudication authority or enable Agent traffic.
--
-- Existing Effects are moved to review_hold by the same transaction that
-- opens a review. Dispatchers only claim pending or expired delivering rows,
-- so review_hold is deliberately not a claimable state. A held row retains
-- its delivery counters and dispatcher fence as evidence, but owns no lease
-- and cannot also be delivered or dead-lettered.

ALTER TABLE `w_agent_effect_outbox`
  DROP CONSTRAINT `chk_w_agent_effect_outbox_status`,
  DROP CONSTRAINT `chk_w_agent_effect_outbox_state_tuple`,
  ADD CONSTRAINT `chk_w_agent_effect_outbox_status`
    CHECK (`status` IN ('pending', 'delivering', 'delivered', 'dead_letter', 'review_hold')),
  ADD CONSTRAINT `chk_w_agent_effect_outbox_state_tuple`
    CHECK (
      (`status` = 'pending'
        AND `lease_owner_id` IS NULL AND `lease_expires_at` IS NULL
        AND `delivered_at` IS NULL AND `dead_lettered_at` IS NULL)
      OR
      (`status` = 'delivering'
        AND `lease_owner_id` IS NOT NULL AND `lease_expires_at` IS NOT NULL
        AND `delivery_attempts` >= 1 AND `dispatch_fencing_token` >= 1
        AND `delivered_at` IS NULL AND `dead_lettered_at` IS NULL)
      OR
      (`status` = 'delivered'
        AND `lease_owner_id` IS NULL AND `lease_expires_at` IS NULL
        AND `delivery_attempts` >= 1 AND `dispatch_fencing_token` >= 1
        AND `delivered_at` IS NOT NULL AND `dead_lettered_at` IS NULL)
      OR
      (`status` = 'dead_letter'
        AND `lease_owner_id` IS NULL AND `lease_expires_at` IS NULL
        AND `delivery_attempts` >= 1 AND `dispatch_fencing_token` >= 1
        AND `delivered_at` IS NULL AND `dead_lettered_at` IS NOT NULL)
      OR
      (`status` = 'review_hold'
        AND `lease_owner_id` IS NULL AND `lease_expires_at` IS NULL
        AND `delivered_at` IS NULL AND `dead_lettered_at` IS NULL)
    );

CREATE TABLE IF NOT EXISTS `w_agent_turn_settlement_review` (
  `id`                     bigint unsigned NOT NULL AUTO_INCREMENT,
  `review_id`              varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                             COMMENT 'Opaque server-generated review identity',
  `turn_id`                varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `settlement_key`         varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                             COMMENT 'Exactly-once settlement identity whose release is blocked',
  `request_digest`         varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                             COMMENT 'Canonical digest of the immutable review request evidence',
  `reason`                 varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'usage_unknown',
  `source`                 varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                             COMMENT 'executor_release|reconcile_release',
  `terminal_status`        varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `attempt_id`             varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `fencing_token`          bigint unsigned NOT NULL,
  `operation_id`           varchar(128) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `prior_operation_count` bigint unsigned NOT NULL DEFAULT 0,
  `prior_effect_count`    bigint unsigned NOT NULL DEFAULT 0,
  `current_effect_count`  smallint unsigned NOT NULL DEFAULT 0,
  `status`                 varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending',
  `created_at`             datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at`             datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                             COMMENT 'Written explicitly by future adjudication transitions; no ON UPDATE clause',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_review_review_id` (`review_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_review_turn_id` (`turn_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_review_settlement_key` (`settlement_key`),
  KEY `idx_w_agent_turn_settlement_review_pending` (`status`, `created_at`, `id`),
  CONSTRAINT `chk_w_agent_turn_settlement_review_identity_bytes`
    CHECK (
      OCTET_LENGTH(`review_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`settlement_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`request_digest`) BETWEEN 1 AND 128
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_review_reason`
    CHECK (`reason` = 'usage_unknown'),
  CONSTRAINT `chk_w_agent_turn_settlement_review_source`
    CHECK (`source` IN ('executor_release', 'reconcile_release')),
  CONSTRAINT `chk_w_agent_turn_settlement_review_terminal_status`
    CHECK (`terminal_status` IN ('completed', 'stopped', 'failed', 'timeout')),
  CONSTRAINT `chk_w_agent_turn_settlement_review_fencing_token`
    CHECK (`fencing_token` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `chk_w_agent_turn_settlement_review_attempt_id_bytes`
    CHECK (`attempt_id` IS NULL OR OCTET_LENGTH(`attempt_id`) BETWEEN 1 AND 64),
  CONSTRAINT `chk_w_agent_turn_settlement_review_operation_id_bytes`
    CHECK (`operation_id` IS NULL OR OCTET_LENGTH(`operation_id`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_turn_settlement_review_counts`
    CHECK (
      `prior_operation_count` BETWEEN 0 AND 9223372036854775807
      AND `prior_effect_count` BETWEEN 0 AND 9223372036854775807
      AND `current_effect_count` BETWEEN 0 AND 64
      AND (`prior_operation_count` > 0 OR `prior_effect_count` > 0 OR `current_effect_count` > 0)
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_review_source_tuple`
    CHECK (
      (`source` = 'executor_release' AND `attempt_id` IS NOT NULL AND `operation_id` IS NOT NULL)
      OR
      (`source` = 'reconcile_release' AND `attempt_id` IS NULL AND `operation_id` IS NULL
        AND `current_effect_count` = 0)
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_review_status`
    CHECK (`status` = 'pending'),
  CONSTRAINT `chk_w_agent_turn_settlement_review_updated_time`
    CHECK (`updated_at` >= `created_at`),
  CONSTRAINT `fk_w_agent_turn_settlement_review_turn`
    FOREIGN KEY (`turn_id`) REFERENCES `w_agent_turn` (`turn_id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_turn_settlement_review_operation`
    FOREIGN KEY (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`)
    REFERENCES `w_agent_turn_operation` (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Retry-isolating queue for ambiguous Agent Turn release settlement';
