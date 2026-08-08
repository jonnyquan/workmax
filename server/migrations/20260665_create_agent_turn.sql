-- P0-08a - durable Agent Turn persistence foundation.
--
-- Design refs:
--   ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
--     section 6.4: target Kernel tables use the w_agent_* namespace
--     section 11: Turn owns plugin_snapshot, lifecycle and durable status
--     section 15: replay uses monotonic per-Turn sequence and event identity
--
-- This migration is schema-only. It does not register Agent v1 routes, wire a
-- SQL Store, start a worker, or claim that lease/fencing, settlement or atomic
-- replay-to-live streaming exists.
--
-- Admission identity bounds are part of the SQL contract. A future SQL Store
-- must reject values outside them before issuing a statement:
--   principal_id    1..128 UTF-8 bytes
--   thread_id       1..256 UTF-8 bytes
--   idempotency_key 1..128 UTF-8 bytes
-- All three columns use utf8mb4_bin, so the original values participate in an
-- exact unique(principal_id, thread_id, idempotency_key) constraint. Their
-- declared varchar widths have a worst-case composite index width of 2048
-- bytes under utf8mb4, below InnoDB's 3072-byte key limit; no lossy prefix
-- index or scope hash is used. OCTET_LENGTH checks mirror the byte-bounded Go
-- contract instead of relying on varchar's character-count semantics.
--
-- Event rows are append-only application facts. Admission must insert the Turn
-- and sequence 1 event in one transaction. Later appends must lock the Turn,
-- advance last_event_sequence and INSERT the immutable EventEnvelope JSON in
-- the same transaction. The application role must not UPDATE event rows;
-- retention/compaction, if introduced, requires a separate privileged policy.

CREATE TABLE IF NOT EXISTS `w_agent_turn` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `turn_id`               varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
                            COMMENT 'Opaque public Turn identity; maximum 256 UTF-8 bytes',
  `principal_id`          varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
                            COMMENT 'Server-owned resource principal; exact-match bound 1..128 UTF-8 bytes',
  `thread_id`             varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
                            COMMENT 'Owned Agent thread identity; exact-match bound 1..256 UTF-8 bytes',
  `idempotency_key`       varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
                            COMMENT 'Original client idempotency key; exact-match bound 1..128 UTF-8 bytes',
  `command_digest`        varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                            COMMENT 'Canonical server-computed command digest used to reject conflicting replays',
  `plugin_snapshot_json`  json NOT NULL
                            COMMENT 'Immutable server-resolved Plugin release/capability snapshot; never trusted from Start JSON',
  `status`                varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'queued'
                            COMMENT 'queued|running|completed|stopped|failed|timeout',
  `last_event_sequence`   bigint unsigned NOT NULL DEFAULT 1
                            COMMENT 'Latest committed event sequence in 1..MaxInt64; admission atomically creates sequence 1',
  `cancel_requested_at`   datetime(6) DEFAULT NULL
                            COMMENT 'Durable cancellation intent; observer detach never writes this field',
  `started_at`            datetime(6) DEFAULT NULL,
  `finished_at`           datetime(6) DEFAULT NULL,
  `created_at`            datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at`            datetime(6) NOT NULL
                            COMMENT 'Written explicitly by admission/lifecycle mutations; event sequence allocation does not advance it',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_turn_id` (`turn_id`),
  UNIQUE KEY `uk_w_agent_turn_admission` (`principal_id`, `thread_id`, `idempotency_key`),
  KEY `idx_w_agent_turn_owner` (`principal_id`, `thread_id`, `turn_id`),
  KEY `idx_w_agent_turn_status_created` (`status`, `created_at`),
  CONSTRAINT `chk_w_agent_turn_status`
    CHECK (`status` IN ('queued', 'running', 'completed', 'stopped', 'failed', 'timeout')),
  CONSTRAINT `chk_w_agent_turn_last_event_sequence`
    CHECK (`last_event_sequence` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `chk_w_agent_turn_turn_id_bytes`
    CHECK (OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256),
  CONSTRAINT `chk_w_agent_turn_principal_id_bytes`
    CHECK (OCTET_LENGTH(`principal_id`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_turn_thread_id_bytes`
    CHECK (OCTET_LENGTH(`thread_id`) BETWEEN 1 AND 256),
  CONSTRAINT `chk_w_agent_turn_idempotency_key_bytes`
    CHECK (OCTET_LENGTH(`idempotency_key`) BETWEEN 1 AND 128),
  CONSTRAINT `chk_w_agent_turn_command_digest_bytes`
    CHECK (OCTET_LENGTH(`command_digest`) BETWEEN 1 AND 128)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Durable Agent Kernel Turn identity and lifecycle state';

CREATE TABLE IF NOT EXISTS `w_agent_turn_event` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT,
  `turn_id`         varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
                      COMMENT 'Logical reference to w_agent_turn.turn_id',
  `sequence`        bigint unsigned NOT NULL
                      COMMENT 'Positive, strictly monotonic sequence allocated under the Turn row lock',
  `event_id`        varchar(320) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
                      COMMENT 'Stable EventEnvelope eventId, unique within one Turn',
  `schema_version`  smallint unsigned NOT NULL
                      COMMENT 'EventEnvelope integer schema version',
  `event_type`      varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
                      COMMENT 'Open namespaced event type for indexed diagnostics',
  `event_json`      json NOT NULL
                      COMMENT 'Immutable complete EventEnvelope JSON; INSERT-only for the application role',
  `created_at`      datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_event_sequence` (`turn_id`, `sequence`),
  UNIQUE KEY `uk_w_agent_turn_event_id` (`turn_id`, `event_id`),
  CONSTRAINT `chk_w_agent_turn_event_sequence`
    CHECK (`sequence` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `chk_w_agent_turn_event_turn_id_bytes`
    CHECK (OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256),
  CONSTRAINT `chk_w_agent_turn_event_id_bytes`
    CHECK (OCTET_LENGTH(`event_id`) BETWEEN 1 AND 320),
  CONSTRAINT `chk_w_agent_turn_event_type_bytes`
    CHECK (OCTET_LENGTH(`event_type`) BETWEEN 1 AND 255),
  CONSTRAINT `fk_w_agent_turn_event_turn`
    FOREIGN KEY (`turn_id`) REFERENCES `w_agent_turn` (`turn_id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Append-only durable Agent EventEnvelope log';
