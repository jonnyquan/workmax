-- P0-045 - immutable Provider usage journal and exact meter provenance.
--
-- Existing P0-043 Evidence does not identify the immutable Meter Release or
-- the Provider receipts from which it was derived. Neither those bindings nor
-- their receipt count can be reconstructed safely. Fail closed before the
-- first schema mutation when Evidence or Resolution already contains rows;
-- operators must investigate them rather than manufacture provenance.

CREATE TEMPORARY TABLE `_w_agent_provider_usage_empty_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_agent_provider_usage_empty_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_provider_usage_empty_guard` (`incompatible_rows`)
SELECT
  (SELECT COUNT(*) FROM `w_agent_turn_settlement_usage_evidence`)
  +
  (SELECT COUNT(*) FROM `w_agent_turn_settlement_review_resolution`);

DROP TEMPORARY TABLE `_w_agent_provider_usage_empty_guard`;

CREATE TABLE IF NOT EXISTS `w_agent_usage_meter_release` (
  `id`                       bigint unsigned NOT NULL AUTO_INCREMENT,
  `release_id`               varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                               COMMENT 'Opaque immutable meter-release identity',
  `plugin_id`                varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_version`           varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_release_digest`    varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_snapshot_digest`   varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                               COMMENT 'Canonical digest of the exact Plugin release tuple',
  `billing_policy_key`       varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `pricing_snapshot_json`    mediumblob NOT NULL
                               COMMENT 'Exact canonical UTF-8 JSON bytes; never MySQL JSON-reserialized',
  `pricing_snapshot_digest`  varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `meter_key`                varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `meter_version`            varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `meter_build_digest`       varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_registry_json`     mediumblob NOT NULL
                               COMMENT 'Canonical sorted multi-provider SourceRegistration set',
  `source_registry_digest`   varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                               COMMENT 'Digest of the complete multi-source registration set',
  `release_digest`           varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `created_at`               datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_usage_meter_release_release_id` (`release_id`),
  UNIQUE KEY `uk_w_agent_usage_meter_release_plugin_snapshot`
    (`plugin_snapshot_digest`),
  UNIQUE KEY `uk_w_agent_usage_meter_release_digest` (`release_digest`),
  CONSTRAINT `chk_w_agent_usage_meter_release_identity`
    CHECK (
      OCTET_LENGTH(`release_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`plugin_id`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`plugin_version`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`plugin_release_digest`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`plugin_snapshot_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`billing_policy_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`meter_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`meter_version`) BETWEEN 1 AND 256
    ),
  CONSTRAINT `chk_w_agent_usage_meter_release_digests`
    CHECK (
      OCTET_LENGTH(`pricing_snapshot_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`meter_build_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`source_registry_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`release_digest`) BETWEEN 1 AND 128
    ),
  CONSTRAINT `chk_w_agent_usage_meter_release_payloads`
    CHECK (
      OCTET_LENGTH(`pricing_snapshot_json`) BETWEEN 1 AND 65536
      AND JSON_VALID(CONVERT(`pricing_snapshot_json` USING utf8mb4))
      AND CONVERT(CONVERT(`pricing_snapshot_json` USING utf8mb4) USING binary)
        = `pricing_snapshot_json`
      AND OCTET_LENGTH(`source_registry_json`) BETWEEN 1 AND 65536
      AND JSON_VALID(CONVERT(`source_registry_json` USING utf8mb4))
      AND CONVERT(CONVERT(`source_registry_json` USING utf8mb4) USING binary)
        = `source_registry_json`
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Immutable trusted-meter release registry for Agent usage';

CREATE TABLE IF NOT EXISTS `w_agent_provider_usage_journal` (
  `id`                          bigint unsigned NOT NULL AUTO_INCREMENT,
  `receipt_id`                  varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                                  COMMENT 'Opaque server-issued immutable journal receipt identity',
  `turn_id`                     varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `attempt_id`                  varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `fencing_token`               bigint unsigned NOT NULL,
  `meter_release_id`            varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `plugin_id`                   varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_version`              varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_release_digest`       varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_snapshot_digest`      varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_key`                varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_account_digest`     varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_request_digest`     varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_event_digest`       varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_key`                  varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_version`              varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_build_digest`         varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_registration_digest`  varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `usage_schema_key`            varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `usage_schema_version`        varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_schema_digest`        varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `canonical_usage_digest`      varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_receipt_digest`     varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `verification_kind`           varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `verification_key_digest`     varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `verification_build_digest`   varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `attestation_digest`          varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `journal_record_digest`       varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_usage_json`         mediumblob NOT NULL
                                  COMMENT 'Exact canonical UTF-8 JSON bytes; never MySQL JSON-reserialized',
  `provider_reported_at`        datetime(6) NOT NULL,
  `created_at`                  datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_provider_usage_journal_receipt_id` (`receipt_id`),
  UNIQUE KEY `uk_w_agent_provider_usage_journal_provider_event`
    (`provider_key`, `provider_account_digest`, `provider_event_digest`),
  UNIQUE KEY `uk_w_agent_provider_usage_journal_source_binding`
    (`receipt_id`, `turn_id`, `meter_release_id`, `canonical_usage_digest`,
     `provider_receipt_digest`, `journal_record_digest`),
  KEY `idx_w_agent_provider_usage_journal_turn`
    (`turn_id`, `created_at`, `id`),
  KEY `idx_w_agent_provider_usage_journal_attempt`
    (`turn_id`, `attempt_id`, `fencing_token`, `id`),
  CONSTRAINT `chk_w_agent_provider_usage_journal_identity`
    CHECK (
      OCTET_LENGTH(`receipt_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`attempt_id`) BETWEEN 1 AND 64
      AND `fencing_token` BETWEEN 1 AND 9223372036854775807
      AND OCTET_LENGTH(`meter_release_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`plugin_id`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`plugin_version`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`plugin_release_digest`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`provider_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`source_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`source_version`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`usage_schema_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`usage_schema_version`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`verification_kind`) BETWEEN 1 AND 32
    ),
  CONSTRAINT `chk_w_agent_provider_usage_journal_digests`
    CHECK (
      OCTET_LENGTH(`plugin_snapshot_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`provider_account_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`provider_request_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`provider_event_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`source_build_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`source_registration_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`source_schema_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`canonical_usage_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`provider_receipt_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`verification_key_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`verification_build_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`attestation_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`journal_record_digest`) BETWEEN 1 AND 128
    ),
  CONSTRAINT `chk_w_agent_provider_usage_journal_payload`
    CHECK (
      OCTET_LENGTH(`provider_usage_json`) BETWEEN 1 AND 65536
      AND JSON_VALID(CONVERT(`provider_usage_json` USING utf8mb4))
      AND CONVERT(CONVERT(`provider_usage_json` USING utf8mb4) USING binary)
        = `provider_usage_json`
    ),
  CONSTRAINT `fk_w_agent_provider_usage_journal_attempt`
    FOREIGN KEY (`turn_id`, `attempt_id`, `fencing_token`)
    REFERENCES `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_provider_usage_journal_meter_release`
    FOREIGN KEY (`meter_release_id`)
    REFERENCES `w_agent_usage_meter_release` (`release_id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Append-only attested scoped-adapter usage receipts';

ALTER TABLE `w_agent_turn_settlement_review`
  ADD COLUMN `prior_provider_usage_count` bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'Provider journal rows observed before the terminal decision'
    AFTER `prior_effect_count`,
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_reason`,
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_source`,
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_counts`,
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_source_tuple`,
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_reason`
    CHECK (`reason` IN (
      'usage_unknown', 'completed_usage_unmeasured', 'terminal_usage_unmeasured'
    )),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_source`
    CHECK (`source` IN (
      'executor_release', 'reconcile_release', 'executor_completion',
      'executor_terminal', 'reconcile_terminal'
    )),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_counts`
    CHECK (
      `prior_operation_count` BETWEEN 0 AND 9223372036854775807
      AND `prior_effect_count` BETWEEN 0 AND 9223372036854775807
      AND `prior_provider_usage_count` BETWEEN 0 AND 9223372036854775807
      AND `current_effect_count` BETWEEN 0 AND 64
      AND (
        `source` IN ('executor_completion', 'executor_terminal', 'reconcile_terminal')
        OR (`source` IN ('executor_release', 'reconcile_release')
          AND `prior_provider_usage_count` = 0
          AND (`prior_operation_count` > 0
            OR `prior_effect_count` > 0
            OR `current_effect_count` > 0))
      )
    ),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_source_tuple`
    CHECK (
      (`source` = 'executor_release'
        AND `reason` = 'usage_unknown'
        AND `attempt_id` IS NOT NULL AND `operation_id` IS NOT NULL)
      OR
      (`source` = 'reconcile_release'
        AND `reason` = 'usage_unknown'
        AND `attempt_id` IS NULL AND `operation_id` IS NULL
        AND `current_effect_count` = 0)
      OR
      (`source` = 'executor_completion'
        AND `reason` = 'completed_usage_unmeasured'
        AND `terminal_status` = 'completed'
        AND `attempt_id` IS NOT NULL AND `operation_id` IS NOT NULL)
      OR
      (`source` = 'executor_terminal'
        AND `reason` = 'terminal_usage_unmeasured'
        AND `terminal_status` IN ('stopped', 'failed', 'timeout')
        AND `attempt_id` IS NOT NULL AND `operation_id` IS NOT NULL)
      OR
      (`source` = 'reconcile_terminal'
        AND `reason` = 'terminal_usage_unmeasured'
        AND `terminal_status` IN ('stopped', 'failed', 'timeout')
        AND `attempt_id` IS NULL AND `operation_id` IS NULL
        AND `current_effect_count` = 0)
    );

ALTER TABLE `w_agent_turn_settlement_usage_evidence`
  ADD COLUMN `meter_release_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
    COMMENT 'Exact immutable Meter Release used for this measurement'
    AFTER `meter_build_digest`,
  ADD COLUMN `source_receipt_count` smallint unsigned NOT NULL
    COMMENT 'Exact number of Provider journal receipts bound below'
    AFTER `usage_source_digest`,
  ADD UNIQUE KEY `uk_w_agent_turn_settlement_usage_evidence_provenance`
    (`evidence_id`, `review_id`, `turn_id`, `settlement_key`, `review_request_digest`,
     `meter_release_id`, `usage_source_digest`, `evidence_digest`, `source_receipt_count`),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_provenance`
    CHECK (
      OCTET_LENGTH(`meter_release_id`) BETWEEN 1 AND 64
      AND `source_receipt_count` BETWEEN 1 AND 64
    ),
  ADD CONSTRAINT `fk_w_agent_turn_settlement_usage_evidence_meter_release`
    FOREIGN KEY (`meter_release_id`)
    REFERENCES `w_agent_usage_meter_release` (`release_id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT;

CREATE TABLE IF NOT EXISTS `w_agent_turn_settlement_usage_evidence_source` (
  `id`                          bigint unsigned NOT NULL AUTO_INCREMENT,
  `evidence_id`                 varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `ordinal`                     smallint unsigned NOT NULL,
  `review_id`                   varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `turn_id`                     varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `settlement_key`              varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `review_request_digest`       varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `meter_release_id`            varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `usage_source_digest`         varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `evidence_digest`             varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_receipt_count`        smallint unsigned NOT NULL,
  `receipt_id`                  varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_registration_digest`  varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `source_schema_digest`        varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `canonical_usage_digest`      varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_receipt_digest`     varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `journal_record_digest`       varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `created_at`                  datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_usage_evidence_source_ordinal`
    (`evidence_id`, `ordinal`),
  UNIQUE KEY `uk_w_agent_turn_settlement_usage_evidence_source_receipt`
    (`receipt_id`),
  KEY `idx_w_agent_turn_settlement_usage_evidence_source_turn`
    (`turn_id`, `evidence_id`, `ordinal`),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_source_identity`
    CHECK (
      OCTET_LENGTH(`evidence_id`) BETWEEN 1 AND 64
      AND `ordinal` BETWEEN 0 AND 63
      AND OCTET_LENGTH(`review_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`settlement_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`meter_release_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`receipt_id`) BETWEEN 1 AND 64
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_source_digests`
    CHECK (
      OCTET_LENGTH(`review_request_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`usage_source_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`evidence_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`source_registration_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`source_schema_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`canonical_usage_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`provider_receipt_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`journal_record_digest`) BETWEEN 1 AND 128
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_source_ordinal`
    CHECK (
      `source_receipt_count` BETWEEN 1 AND 64
      AND `ordinal` < `source_receipt_count`
    ),
  CONSTRAINT `fk_w_agent_turn_settlement_usage_evidence_source_evidence`
    FOREIGN KEY (`evidence_id`, `review_id`, `turn_id`, `settlement_key`,
                 `review_request_digest`, `meter_release_id`, `usage_source_digest`,
                 `evidence_digest`, `source_receipt_count`)
    REFERENCES `w_agent_turn_settlement_usage_evidence`
      (`evidence_id`, `review_id`, `turn_id`, `settlement_key`,
       `review_request_digest`, `meter_release_id`, `usage_source_digest`,
       `evidence_digest`, `source_receipt_count`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_turn_settlement_usage_evidence_source_journal`
    FOREIGN KEY (`receipt_id`, `turn_id`, `meter_release_id`,
                 `canonical_usage_digest`, `provider_receipt_digest`,
                 `journal_record_digest`)
    REFERENCES `w_agent_provider_usage_journal`
      (`receipt_id`, `turn_id`, `meter_release_id`,
       `canonical_usage_digest`, `provider_receipt_digest`,
       `journal_record_digest`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Exact attested journal receipts consumed by trusted Meter Evidence';
