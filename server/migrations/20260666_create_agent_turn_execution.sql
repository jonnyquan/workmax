-- P0-08b - fenced Agent Turn execution and transactional Effect Outbox.
--
-- This migration extends the durable Turn/Event foundation from 20260665. It
-- is still candidate schema: it does not start a worker, register Agent routes
-- or make the current unfenced worker methods production-safe.
--
-- A claim increments w_agent_turn.fencing_token and creates exactly one
-- matching Attempt. Every worker-authored Operation records that Attempt and
-- the Event committed by the same transaction. Effect Outbox rows are bound
-- to the same Operation and Attempt so a stale worker cannot enqueue a new
-- external effect under a current Turn identity.
--
-- All counters stop at MaxInt64 even though MariaDB uses unsigned columns. The
-- shared ceiling keeps the contract representable by Go int64 and SQLite.

ALTER TABLE `w_agent_turn`
  ADD COLUMN `active_attempt_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
    COMMENT 'Current active server-generated Attempt identity; NULL before claim and after active execution ends',
  ADD COLUMN `fencing_token` bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'Monotonic execution fence; 0 before the first claim and 1..MaxInt64 afterwards',
  ADD KEY `idx_w_agent_turn_active_attempt` (`turn_id`, `active_attempt_id`, `fencing_token`),
  ADD CONSTRAINT `chk_w_agent_turn_fencing_token`
    CHECK (`fencing_token` BETWEEN 0 AND 9223372036854775807),
  ADD CONSTRAINT `chk_w_agent_turn_active_attempt_tuple`
    CHECK (
      (`active_attempt_id` IS NULL AND `fencing_token` BETWEEN 0 AND 9223372036854775807)
      OR
      (`active_attempt_id` IS NOT NULL AND `fencing_token` BETWEEN 1 AND 9223372036854775807)
    ),
  ADD CONSTRAINT `chk_w_agent_turn_active_attempt_id_bytes`
    CHECK (`active_attempt_id` IS NULL OR OCTET_LENGTH(`active_attempt_id`) BETWEEN 1 AND 64);

CREATE TABLE IF NOT EXISTS `w_agent_turn_attempt` (
  `id`                   bigint unsigned NOT NULL AUTO_INCREMENT,
  `attempt_id`           varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                           COMMENT 'Opaque server-generated Attempt identity',
  `turn_id`              varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `fencing_token`        bigint unsigned NOT NULL
                           COMMENT 'Turn-scoped monotonic execution fence in 1..MaxInt64',
  `status`               varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'running'
                           COMMENT 'running|completed|stopped|failed|timeout|expired',
  `worker_id`            varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `worker_build_digest`  varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `claimed_at`           datetime(6) NOT NULL,
  `last_heartbeat_at`    datetime(6) NOT NULL,
  `lease_expires_at`     datetime(6) NOT NULL,
  `finished_at`          datetime(6) DEFAULT NULL,
  `created_at`           datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at`           datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                           COMMENT 'Written explicitly by claim, heartbeat and terminal mutations; no ON UPDATE clause',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_attempt_id` (`attempt_id`),
  UNIQUE KEY `uk_w_agent_turn_attempt_fence` (`turn_id`, `fencing_token`),
  UNIQUE KEY `uk_w_agent_turn_attempt_binding` (`turn_id`, `attempt_id`, `fencing_token`),
  KEY `idx_w_agent_turn_attempt_claim` (`status`, `lease_expires_at`, `id`),
  CONSTRAINT `chk_w_agent_turn_attempt_fencing_token`
    CHECK (`fencing_token` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `chk_w_agent_turn_attempt_identity_bytes`
    CHECK (OCTET_LENGTH(`attempt_id`) BETWEEN 1 AND 64),
  CONSTRAINT `chk_w_agent_turn_attempt_worker_id_bytes`
    CHECK (OCTET_LENGTH(`worker_id`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_turn_attempt_worker_build_bytes`
    CHECK (OCTET_LENGTH(`worker_build_digest`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_turn_attempt_status`
    CHECK (`status` IN ('running', 'completed', 'stopped', 'failed', 'timeout', 'expired')),
  CONSTRAINT `chk_w_agent_turn_attempt_status_time`
    CHECK (
      (`status` = 'running' AND `finished_at` IS NULL)
      OR
      (`status` IN ('completed', 'stopped', 'failed', 'timeout', 'expired') AND `finished_at` IS NOT NULL)
    ),
  CONSTRAINT `chk_w_agent_turn_attempt_lease_time`
    CHECK (`claimed_at` <= `last_heartbeat_at` AND `last_heartbeat_at` < `lease_expires_at`),
  CONSTRAINT `chk_w_agent_turn_attempt_finished_time`
    CHECK (`finished_at` IS NULL OR `finished_at` >= `last_heartbeat_at`),
  CONSTRAINT `chk_w_agent_turn_attempt_updated_time`
    CHECK (`updated_at` >= `created_at`),
  CONSTRAINT `fk_w_agent_turn_attempt_turn`
    FOREIGN KEY (`turn_id`) REFERENCES `w_agent_turn` (`turn_id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Fenced worker execution attempts for durable Agent Turns';

-- Added after the Attempt table exists because Turn and Attempt deliberately
-- form a cycle: an Attempt belongs to a Turn, while Turn points at the sole
-- Attempt authorized by its current fencing token.
ALTER TABLE `w_agent_turn`
  ADD CONSTRAINT `fk_w_agent_turn_active_attempt`
    FOREIGN KEY (`turn_id`, `active_attempt_id`, `fencing_token`)
    REFERENCES `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`)
    ON DELETE RESTRICT ON UPDATE RESTRICT;

CREATE TABLE IF NOT EXISTS `w_agent_turn_operation` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT,
  `turn_id`           varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `operation_id`      varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                        COMMENT 'Stable Turn-scoped idempotency identity for one worker commit',
  `operation_digest`  varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                        COMMENT 'Canonical digest used to reject conflicting operation replay',
  `attempt_id`        varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `fencing_token`     bigint unsigned NOT NULL,
  `event_sequence`    bigint unsigned NOT NULL
                        COMMENT 'Event committed atomically with this Operation',
  `turn_status`       varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `effect_count`      smallint unsigned NOT NULL DEFAULT 0
                        COMMENT 'Expected Outbox row count for unknown-commit completeness checks; maximum 64',
  `created_at`        datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_operation_identity` (`turn_id`, `operation_id`),
  UNIQUE KEY `uk_w_agent_turn_operation_binding` (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`),
  KEY `idx_w_agent_turn_operation_attempt` (`turn_id`, `attempt_id`, `fencing_token`),
  KEY `idx_w_agent_turn_operation_event` (`turn_id`, `event_sequence`),
  CONSTRAINT `chk_w_agent_turn_operation_id_bytes`
    CHECK (OCTET_LENGTH(`operation_id`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_turn_operation_digest_bytes`
    CHECK (OCTET_LENGTH(`operation_digest`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_turn_operation_fencing_token`
    CHECK (`fencing_token` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `chk_w_agent_turn_operation_event_sequence`
    CHECK (`event_sequence` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `chk_w_agent_turn_operation_effect_count`
    CHECK (`effect_count` BETWEEN 0 AND 64),
  CONSTRAINT `chk_w_agent_turn_operation_status`
    CHECK (`turn_status` IN ('queued', 'running', 'completed', 'stopped', 'failed', 'timeout')),
  CONSTRAINT `fk_w_agent_turn_operation_attempt`
    FOREIGN KEY (`turn_id`, `attempt_id`, `fencing_token`)
    REFERENCES `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_turn_operation_event`
    FOREIGN KEY (`turn_id`, `event_sequence`)
    REFERENCES `w_agent_turn_event` (`turn_id`, `sequence`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Immutable idempotent worker Operations committed under a valid Turn fence';

CREATE TABLE IF NOT EXISTS `w_agent_effect_outbox` (
  `id`                        bigint unsigned NOT NULL AUTO_INCREMENT,
  `outbox_id`                 varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `turn_id`                   varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `attempt_id`                varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `turn_fencing_token`        bigint unsigned NOT NULL,
  `operation_id`              varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `ordinal`                   int unsigned NOT NULL
                                COMMENT 'Zero-based stable Effect ordinal within one Operation',
  `topic`                     varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `dedupe_key`                varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `payload_json`              json NOT NULL
                                COMMENT 'Bounded typed Effect payload; never stores credentials or arbitrary destination authority',
  `status`                    varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending'
                                COMMENT 'pending|delivering|delivered|dead_letter',
  `available_at`              datetime(6) NOT NULL,
  `delivery_attempts`         bigint unsigned NOT NULL DEFAULT 0,
  `dispatch_fencing_token`    bigint unsigned NOT NULL DEFAULT 0
                                COMMENT 'Monotonic dispatcher fence; independent from the Turn fencing token',
  `lease_owner_id`            varchar(128) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `lease_expires_at`          datetime(6) DEFAULT NULL,
  `last_error_code`           varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `delivered_at`              datetime(6) DEFAULT NULL,
  `dead_lettered_at`          datetime(6) DEFAULT NULL,
  `created_at`                datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at`                datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                                COMMENT 'Written explicitly by dispatcher transitions; no ON UPDATE clause',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_effect_outbox_id` (`outbox_id`),
  UNIQUE KEY `uk_w_agent_effect_outbox_dedupe` (`topic`, `dedupe_key`),
  UNIQUE KEY `uk_w_agent_effect_outbox_operation_ordinal` (`turn_id`, `operation_id`, `ordinal`),
  KEY `idx_w_agent_effect_outbox_pending` (`status`, `available_at`, `id`),
  KEY `idx_w_agent_effect_outbox_expired` (`status`, `lease_expires_at`, `id`),
  KEY `idx_w_agent_effect_outbox_attempt` (`turn_id`, `attempt_id`, `turn_fencing_token`),
  KEY `idx_w_agent_effect_outbox_operation` (`turn_id`, `operation_id`, `attempt_id`, `turn_fencing_token`),
  CONSTRAINT `chk_w_agent_effect_outbox_id_bytes`
    CHECK (OCTET_LENGTH(`outbox_id`) BETWEEN 1 AND 64),
  CONSTRAINT `chk_w_agent_effect_outbox_topic_bytes`
    CHECK (OCTET_LENGTH(`topic`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_effect_outbox_dedupe_bytes`
    CHECK (OCTET_LENGTH(`dedupe_key`) BETWEEN 1 AND 256),
  CONSTRAINT `chk_w_agent_effect_outbox_payload_bytes`
    CHECK (OCTET_LENGTH(`payload_json`) BETWEEN 1 AND 1048576),
  CONSTRAINT `chk_w_agent_effect_outbox_ordinal`
    CHECK (`ordinal` BETWEEN 0 AND 63),
  CONSTRAINT `chk_w_agent_effect_outbox_turn_fence`
    CHECK (`turn_fencing_token` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `chk_w_agent_effect_outbox_dispatch_counters`
    CHECK (
      `delivery_attempts` BETWEEN 0 AND 9223372036854775807
      AND `dispatch_fencing_token` BETWEEN 0 AND 9223372036854775807
    ),
  CONSTRAINT `chk_w_agent_effect_outbox_lease_owner_bytes`
    CHECK (`lease_owner_id` IS NULL OR OCTET_LENGTH(`lease_owner_id`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_effect_outbox_error_code_bytes`
    CHECK (`last_error_code` IS NULL OR OCTET_LENGTH(`last_error_code`) BETWEEN 1 AND 64),
  CONSTRAINT `chk_w_agent_effect_outbox_status`
    CHECK (`status` IN ('pending', 'delivering', 'delivered', 'dead_letter')),
  CONSTRAINT `chk_w_agent_effect_outbox_state_tuple`
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
    ),
  CONSTRAINT `chk_w_agent_effect_outbox_lease_time`
    CHECK (`lease_expires_at` IS NULL OR `lease_expires_at` > `updated_at`),
  CONSTRAINT `chk_w_agent_effect_outbox_terminal_time`
    CHECK (
      (`delivered_at` IS NULL OR `delivered_at` >= `created_at`)
      AND (`dead_lettered_at` IS NULL OR `dead_lettered_at` >= `created_at`)
    ),
  CONSTRAINT `chk_w_agent_effect_outbox_updated_time`
    CHECK (`updated_at` >= `created_at`),
  CONSTRAINT `fk_w_agent_effect_outbox_attempt`
    FOREIGN KEY (`turn_id`, `attempt_id`, `turn_fencing_token`)
    REFERENCES `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_effect_outbox_operation`
    FOREIGN KEY (`turn_id`, `operation_id`, `attempt_id`, `turn_fencing_token`)
    REFERENCES `w_agent_turn_operation` (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Transactional Agent Effect Outbox with independent dispatch leasing and fencing';
