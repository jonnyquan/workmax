-- P0-046 - harden the legacy Credits Reservation hold, TTL and refund state.
--
-- This is an additive, schema-first migration. It deliberately does not bind
-- Credits rows to the still-unmounted Agent Turn candidate schema and it never
-- manufactures a refund, hold or terminal outcome for historical rows.
--
-- Rollout contract:
--   1. stop reservation admission and the legacy sweeper, then drain every
--      legacy writer before running either guard or persistent ALTER;
--   2. apply this schema while all Reservation writers remain stopped;
--   3. deploy only binaries that understand review_hold and refund_pending;
--   4. enable the new state machine and then restart the hardened sweeper.
--
-- Rolling back to a legacy writer is safe only after proving that no row is in
-- review_hold or refund_pending. The added columns and constraints should stay
-- in place; there is intentionally no destructive down migration.

-- A historical sweeper could mark a reservation expired even after its pack
-- refund failed, truncate the failure marker out of a full 255-byte remark,
-- and never refund project budget. That outcome cannot be reconstructed from
-- current aggregate balances. Every legacy expired row therefore needs an
-- explicit operator evidence marker before this migration may proceed.
CREATE TEMPORARY TABLE `_w_credit_reservation_refund_history_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_credit_reservation_refund_history_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_credit_reservation_refund_history_guard` (`incompatible_rows`)
SELECT COUNT(*)
FROM `w_credit_reservation`
WHERE BINARY `status` = 'expired'
  AND LOWER(COALESCE(`remark`, '')) NOT LIKE '%p0-046-refund-reconciled%';

DROP TEMPORARY TABLE `_w_credit_reservation_refund_history_guard`;

-- Fail before the first persistent mutation when legacy rows cannot satisfy
-- the new state and allocation contract. No UPDATE/backfill is safe here: in
-- particular, current pack balances cannot prove an old refund outcome.
CREATE TEMPORARY TABLE `_w_credit_reservation_integrity_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_credit_reservation_integrity_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_credit_reservation_integrity_guard` (`incompatible_rows`)
SELECT
  (SELECT COUNT(*)
     FROM `w_credit_reservation`
    WHERE BINARY `status` NOT IN ('reserved', 'finalized', 'released', 'expired'))
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation`
    WHERE `reserved` < 0
       OR `used` < 0
       OR `used` > `reserved`
       OR (BINARY `status` <> 'finalized' AND `used` <> 0))
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation` AS `r`
     LEFT JOIN `w_global_project` AS `p` ON `p`.`id` = `r`.`project_id`
    WHERE `r`.`project_id` <> 0
      AND (`p`.`id` IS NULL OR `p`.`uid` <> `r`.`uid`))
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation`
    WHERE NOT (
      (BINARY `status` = 'reserved'
        AND `finalized_at` IS NULL AND `released_at` IS NULL)
      OR
      (BINARY `status` = 'finalized'
        AND `finalized_at` IS NOT NULL AND `released_at` IS NULL)
      OR
      (BINARY `status` IN ('released', 'expired')
        AND `finalized_at` IS NULL AND `released_at` IS NOT NULL)
    ))
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation_allocation`
    WHERE `credits` <= 0)
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation_allocation` AS `a`
     LEFT JOIN `w_credit_reservation` AS `r` ON `r`.`id` = `a`.`reservation_id`
    WHERE `r`.`id` IS NULL)
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation_allocation` AS `a`
     LEFT JOIN `w_credits_pack` AS `p` ON `p`.`id` = `a`.`pack_id`
    WHERE `p`.`id` IS NULL)
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation_allocation` AS `a`
     INNER JOIN `w_credit_reservation` AS `r` ON `r`.`id` = `a`.`reservation_id`
     INNER JOIN `w_credits_pack` AS `p` ON `p`.`id` = `a`.`pack_id`
    WHERE `p`.`uid` <> `r`.`uid`)
  +
  (SELECT COUNT(*)
     FROM (
       SELECT `a`.`pack_id`
         FROM `w_credit_reservation_allocation` AS `a`
         INNER JOIN `w_credit_reservation` AS `r` ON `r`.`id` = `a`.`reservation_id`
         INNER JOIN `w_credits_pack` AS `p` ON `p`.`id` = `a`.`pack_id`
        WHERE BINARY `r`.`status` = 'reserved'
        GROUP BY `a`.`pack_id`, `p`.`credits_used`
       HAVING SUM(`a`.`credits`) > `p`.`credits_used`
     ) AS `active_pack_debit_drift`)
  +
  (SELECT COUNT(*)
     FROM (
       SELECT `r`.`project_id`
         FROM `w_credit_reservation` AS `r`
         INNER JOIN `w_global_project` AS `p` ON `p`.`id` = `r`.`project_id`
        WHERE BINARY `r`.`status` = 'reserved'
          AND `r`.`project_id` <> 0
        GROUP BY `r`.`project_id`, `p`.`budget_credits_used`
       HAVING SUM(`r`.`reserved`) > `p`.`budget_credits_used`
     ) AS `active_project_debit_drift`)
  +
  (SELECT COUNT(*)
     FROM (
       SELECT `reservation_id`, `pack_id`
         FROM `w_credit_reservation_allocation`
        GROUP BY `reservation_id`, `pack_id`
       HAVING COUNT(*) > 1
     ) AS `duplicate_allocation`)
  +
  (SELECT COUNT(*)
     FROM `w_credit_reservation` AS `r`
     LEFT JOIN (
       SELECT `reservation_id`, SUM(`credits`) AS `allocated`
         FROM `w_credit_reservation_allocation`
        GROUP BY `reservation_id`
     ) AS `a` ON `a`.`reservation_id` = `r`.`id`
    WHERE COALESCE(`a`.`allocated`, 0) <> `r`.`reserved`);

DROP TEMPORARY TABLE `_w_credit_reservation_integrity_guard`;

ALTER TABLE `w_credit_reservation`
  ADD COLUMN `request_digest` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
    COMMENT 'Optional canonical reservation request digest; NULL only for legacy rows',
  ADD COLUMN `hold_review_id` varchar(256) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
    COMMENT 'Opaque exact review identity while or after a commercial hold',
  ADD COLUMN `hold_settlement_key` varchar(256) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
    COMMENT 'Opaque exact settlement identity; unique when present',
  ADD COLUMN `hold_request_digest` varchar(128) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
    COMMENT 'Canonical digest of the immutable hold request',
  ADD COLUMN `review_held_at` datetime(6) DEFAULT NULL,
  ADD COLUMN `refund_target_status` varchar(16) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
    COMMENT 'Terminal state to publish only after the pending refund commits',
  ADD COLUMN `refund_target_used` int unsigned DEFAULT NULL
    COMMENT 'Used units to publish with the refund target state',
  ADD COLUMN `refund_due` int unsigned NOT NULL DEFAULT 0
    COMMENT 'Credits still due back while status is refund_pending',
  ADD COLUMN `refund_attempts` bigint unsigned NOT NULL DEFAULT 0,
  ADD COLUMN `next_refund_at` datetime(6) DEFAULT NULL,
  ADD COLUMN `last_refund_error_code` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
    COMMENT 'Bounded stable code only; never raw driver or user text',
  ADD COLUMN `state_changed_at` datetime(6) DEFAULT NULL
    COMMENT 'Database-authoritative lifecycle time; NULL only for legacy rows',
  ADD COLUMN `state_version` bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'Monotonic compare-and-swap version; 0 only for legacy rows',
  ADD UNIQUE KEY `uk_w_credit_reservation_hold_settlement` (`hold_settlement_key`),
  ADD KEY `idx_w_credit_reservation_sweep` (`status`, `expires_at`, `id`),
  ADD KEY `idx_w_credit_reservation_refund` (`status`, `next_refund_at`, `id`),
  ADD CONSTRAINT `chk_w_credit_reservation_status`
    CHECK (BINARY `status` IN (
      'reserved', 'review_hold', 'refund_pending', 'finalized', 'released', 'expired'
    )),
  ADD CONSTRAINT `chk_w_credit_reservation_amounts`
    CHECK (
      `reserved` >= 0
      AND `used` >= 0
      AND `used` <= `reserved`
      AND (BINARY `status` = 'finalized' OR `used` = 0)
      AND `refund_due` BETWEEN 0 AND `reserved`
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_digests`
    CHECK (
      (`request_digest` IS NULL OR OCTET_LENGTH(`request_digest`) = 64)
      AND (`hold_request_digest` IS NULL OR (
        OCTET_LENGTH(`hold_request_digest`) = 71
        AND BINARY LEFT(`hold_request_digest`, 7) = 'sha256:'
      ))
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_bounded_codes`
    CHECK (
      (`hold_review_id` IS NULL
        OR OCTET_LENGTH(`hold_review_id`) BETWEEN 1 AND 256)
      AND (`hold_settlement_key` IS NULL
        OR OCTET_LENGTH(`hold_settlement_key`) BETWEEN 1 AND 256)
      AND (`last_refund_error_code` IS NULL
        OR OCTET_LENGTH(`last_refund_error_code`) BETWEEN 1 AND 64)
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_hold_tuple`
    CHECK (
      (`hold_review_id` IS NULL
        AND `hold_settlement_key` IS NULL
        AND `hold_request_digest` IS NULL
        AND `review_held_at` IS NULL)
      OR
      (`hold_review_id` IS NOT NULL
        AND `hold_settlement_key` IS NOT NULL
        AND `hold_request_digest` IS NOT NULL
        AND `review_held_at` IS NOT NULL)
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_review_state`
    CHECK (
      (BINARY `status` <> 'reserved'
        OR (`hold_review_id` IS NULL
          AND `hold_settlement_key` IS NULL
          AND `hold_request_digest` IS NULL
          AND `review_held_at` IS NULL))
      AND
      (BINARY `status` <> 'review_hold'
        OR (`hold_review_id` IS NOT NULL
          AND `hold_settlement_key` IS NOT NULL
          AND `hold_request_digest` IS NOT NULL
          AND `review_held_at` IS NOT NULL))
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_refund_tuple`
    CHECK (
      (BINARY `status` = 'refund_pending'
        AND `refund_target_status` IS NOT NULL
        AND BINARY `refund_target_status` IN ('finalized', 'released', 'expired')
        AND `refund_target_used` IS NOT NULL
        AND `refund_target_used` BETWEEN 0 AND `reserved`
        AND `refund_due` > 0
        AND `refund_due` = `reserved` - `refund_target_used`
        AND (BINARY `refund_target_status` = 'finalized' OR `refund_target_used` = 0)
        AND `next_refund_at` IS NOT NULL)
      OR
      (BINARY `status` <> 'refund_pending'
        AND `refund_target_status` IS NULL
        AND `refund_target_used` IS NULL
        AND `refund_due` = 0
        AND `next_refund_at` IS NULL)
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_status_time`
    CHECK (
      (BINARY `status` IN ('reserved', 'review_hold', 'refund_pending')
        AND `finalized_at` IS NULL AND `released_at` IS NULL)
      OR
      (BINARY `status` = 'finalized'
        AND `finalized_at` IS NOT NULL AND `released_at` IS NULL)
      OR
      (BINARY `status` IN ('released', 'expired')
        AND `finalized_at` IS NULL AND `released_at` IS NOT NULL)
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_refund_error_code`
    CHECK (
      `last_refund_error_code` IS NULL
      OR (BINARY `status` = 'refund_pending'
        AND BINARY `last_refund_error_code` IN (
          'project_invariant', 'allocation_invalid', 'allocation_incomplete',
          'pack_invariant', 'database_error'
        ))
    ),
  ADD CONSTRAINT `chk_w_credit_reservation_lifecycle_time`
    CHECK (
      (`review_held_at` IS NULL OR `review_held_at` >= `created_at`)
      AND (`state_changed_at` IS NULL OR `state_changed_at` >= `created_at`)
    );

ALTER TABLE `w_credit_reservation_allocation`
  ADD UNIQUE KEY `uk_w_credit_reservation_allocation_pair` (`reservation_id`, `pack_id`),
  ADD CONSTRAINT `chk_w_credit_reservation_allocation_credits`
    CHECK (`credits` > 0),
  ADD CONSTRAINT `fk_w_credit_reservation_allocation_reservation`
    FOREIGN KEY (`reservation_id`) REFERENCES `w_credit_reservation` (`id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT;

-- MySQL commits DDL at statement boundaries. If the first ALTER is present but
-- the allocation ALTER is absent after a deployment interruption, do not rerun
-- this file from the top: verify every named main-table column/index/CHECK in
-- information_schema, then resume only the allocation ALTER. If the first
-- ALTER is absent, rerun both guards before retrying it. Never infer completion
-- from migration-file bookkeeping alone.
