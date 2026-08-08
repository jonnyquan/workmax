-- P0-043 - immutable trusted-meter evidence for held Settlement Reviews.
--
-- P0-042 is not mounted and must not have written production Resolution rows.
-- The two new NOT NULL Resolution bindings cannot be reconstructed safely from
-- an old receipt: a fabricated evidence/pricing backfill would turn unknown
-- commercial history into trusted metering. Fail closed before any ALTER when
-- the old Resolution table is non-empty or a Review already claims the old
-- finalized_held state without a Resolution row; operators must investigate
-- rather than inventing evidence. The temporary CHECK guard works on MySQL 8.0.19+
-- (the existing DROP CONSTRAINT migrations already impose that version floor).

CREATE TEMPORARY TABLE `_w_agent_turn_resolution_empty_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_agent_turn_resolution_empty_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_turn_resolution_empty_guard` (`incompatible_rows`)
SELECT
  (SELECT COUNT(*) FROM `w_agent_turn_settlement_review_resolution`)
  +
  (SELECT COUNT(*) FROM `w_agent_turn_settlement_review`
    WHERE `status` = 'finalized_held');

DROP TEMPORARY TABLE `_w_agent_turn_resolution_empty_guard`;

ALTER TABLE `w_agent_turn_settlement_review`
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_status`,
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_status`
    CHECK (`status` IN ('pending', 'metered_held', 'finalized_held'));

CREATE TABLE IF NOT EXISTS `w_agent_turn_settlement_usage_evidence` (
  `id`                       bigint unsigned NOT NULL AUTO_INCREMENT,
  `evidence_id`              varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                               COMMENT 'Opaque server-generated immutable evidence identity',
  `review_id`                varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `turn_id`                  varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `settlement_key`           varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `review_request_digest`    varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `plugin_id`                varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_version`           varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `plugin_release_digest`    varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `billing_policy_key`       varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `pricing_snapshot_digest`  varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `meter_key`                varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `meter_version`            varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `meter_build_digest`       varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `usage_source_digest`      varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `measurement_digest`       varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `used_units`               bigint unsigned NOT NULL,
  `meter_receipt_digest`     varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `evidence_digest`          varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `created_at`               datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_usage_evidence_evidence_id`
    (`evidence_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_usage_evidence_review_id`
    (`review_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_usage_evidence_meter_source`
    (`meter_key`, `meter_version`, `usage_source_digest`),
  UNIQUE KEY `uk_w_agent_turn_settlement_usage_evidence_resolution_binding`
    (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`,
     `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`),
  KEY `idx_w_agent_turn_settlement_usage_evidence_turn`
    (`turn_id`, `created_at`, `id`),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_identity`
    CHECK (
      OCTET_LENGTH(`evidence_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`review_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`settlement_key`) BETWEEN 1 AND 256
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_plugin`
    CHECK (
      OCTET_LENGTH(`plugin_id`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`plugin_version`) BETWEEN 1 AND 512
      AND OCTET_LENGTH(`plugin_release_digest`) BETWEEN 1 AND 512
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_meter`
    CHECK (
      OCTET_LENGTH(`billing_policy_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`meter_key`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`meter_version`) BETWEEN 1 AND 256
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_digests`
    CHECK (
      OCTET_LENGTH(`review_request_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`pricing_snapshot_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`meter_build_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`usage_source_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`measurement_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`meter_receipt_digest`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`evidence_digest`) BETWEEN 1 AND 128
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_usage_evidence_units`
    CHECK (`used_units` BETWEEN 1 AND 9223372036854775807),
  CONSTRAINT `fk_w_agent_turn_settlement_usage_evidence_review`
    FOREIGN KEY (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`)
    REFERENCES `w_agent_turn_settlement_review`
      (`review_id`, `turn_id`, `settlement_key`, `request_digest`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Immutable trusted-meter evidence for held Agent Turn settlement reviews';

ALTER TABLE `w_agent_turn_settlement_review_resolution`
  ADD COLUMN `evidence_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
    COMMENT 'Exact trusted usage evidence resolved by this receipt'
    AFTER `review_request_digest`,
  ADD COLUMN `pricing_snapshot_digest` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
    COMMENT 'Exact immutable pricing snapshot used by the trusted meter'
    AFTER `evidence_digest`,
  ADD KEY `idx_w_agent_turn_settlement_review_resolution_evidence`
    (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`,
     `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_resolution_evidence`
    CHECK (
      OCTET_LENGTH(`evidence_id`) BETWEEN 1 AND 64
      AND OCTET_LENGTH(`pricing_snapshot_digest`) BETWEEN 1 AND 128
    ),
  ADD CONSTRAINT `fk_w_agent_turn_settlement_review_resolution_evidence`
    FOREIGN KEY (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`,
                 `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`)
    REFERENCES `w_agent_turn_settlement_usage_evidence`
      (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`,
       `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`)
    ON DELETE RESTRICT ON UPDATE RESTRICT;
