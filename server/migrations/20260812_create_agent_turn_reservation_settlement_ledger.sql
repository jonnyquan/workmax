-- P0-048 - immutable Turn-to-Reservation binding and SettlementKey outcome ledger.
--
-- Runtime contract: Oracle MySQL 8.0.19+ only. This migration is schema-first
-- and no-backfill: a historical Turn cannot be paired safely with a Credits
-- Reservation by principal, idempotency key, TTL, current balance or mutable
-- request data. The binding must be written by admission in the same database
-- transaction that publishes the new Turn and its exact Reservation owner.
--
-- Rollout safety:
--   1. apply 20260665 through 20260671 and 20260807 first;
--   2. stop Agent Start, close every shared AdmissionGate, drain all Attempts,
--      and stop old Worker/Reconciler/Review writers;
--   3. stop the Reservation expiry/refund sweeper and drain its transactions;
--   4. require an empty Agent Turn graph and no historical Reservation hold;
--      do not manufacture a binding or outcome for old rows;
--   5. run every statement on one physical MySQL session, stop on the first error,
--      and enable only a P0-048-aware fleet after exact preflight.
--
-- The persistent DDL statements are individually atomic, but not atomic as one six-statement migration.
-- If a disconnect or
-- failure leaves a partial schema, do not rerun the whole file. Compare every
-- affected column, ordered full-column index, CHECK and RESTRICT foreign key
-- with this file in information_schema, then forward-resume only the first missing reviewed statement.
-- A same-name but different fingerprint is drift.

-- Mechanical Oracle MySQL 8.0.19+ gate. Failure deliberately inserts the
-- existing sentinel 0, so it does not depend on CHECK enforcement or sql_mode.
CREATE TEMPORARY TABLE `_w_agent_settlement_version_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_settlement_version_guard` (`guard_key`) VALUES (0);

SET @w_agent_settlement_version_core =
  REGEXP_SUBSTR(VERSION(), '^[0-9]+[.][0-9]+[.][0-9]+');
SET @w_agent_settlement_version_major =
  CAST(SUBSTRING_INDEX(@w_agent_settlement_version_core, '.', 1) AS UNSIGNED);
SET @w_agent_settlement_version_minor =
  CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(@w_agent_settlement_version_core, '.', 2), '.', -1) AS UNSIGNED);
SET @w_agent_settlement_version_patch =
  CAST(SUBSTRING_INDEX(@w_agent_settlement_version_core, '.', -1) AS UNSIGNED);

INSERT INTO `_w_agent_settlement_version_guard` (`guard_key`)
SELECT CASE
  WHEN LOCATE('mariadb', LOWER(VERSION())) = 0
    AND @w_agent_settlement_version_core IS NOT NULL
    AND (
      @w_agent_settlement_version_major > 8
      OR (
        @w_agent_settlement_version_major = 8
        AND (
          @w_agent_settlement_version_minor > 0
          OR (
            @w_agent_settlement_version_minor = 0
            AND @w_agent_settlement_version_patch >= 19
          )
        )
      )
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_settlement_version_guard`;

-- Prove the exact parent graph needed by the new full-column foreign keys.
-- This is a compatibility precondition, not a replacement for the normal
-- runtime schema preflight or an operator-reviewed migration fingerprint.
CREATE TEMPORARY TABLE `_w_agent_settlement_baseline_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_settlement_baseline_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_agent_settlement_baseline_guard` (`guard_key`)
SELECT CASE
  WHEN @@SESSION.foreign_key_checks = 1
    AND @@SESSION.check_constraint_checks = 1
    AND @@SESSION.unique_checks = 1
    AND @@SESSION.time_zone = '+00:00'
    AND UPPER(@@SESSION.transaction_isolation) IN ('READ-COMMITTED', 'REPEATABLE-READ')
    AND TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(6), CURRENT_TIMESTAMP(6)) = 0
    -- The turn/principal/command full-column FK key is 1664 bytes at its
    -- declared charsets, which exceeds the 1536-byte limit of an 8 KiB page.
    AND @@innodb_page_size >= 16384
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLES`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_agent_turn', 'w_agent_turn_operation',
          'w_agent_turn_settlement_review', 'w_credit_reservation',
          'w_credit_reservation_allocation'
        )
        AND `TABLE_TYPE` = 'BASE TABLE'
        AND UPPER(`ENGINE`) = 'INNODB'
    ) = 5
    -- Several parent keys exceed the legacy 767-byte COMPACT/REDUNDANT
    -- limit. Require a deterministic 3072-byte DYNAMIC-row-format boundary
    -- for every predecessor table before the first durable ALTER.
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLES`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_agent_turn', 'w_agent_turn_operation',
          'w_agent_turn_settlement_review', 'w_credit_reservation',
          'w_credit_reservation_allocation'
        )
        AND UPPER(COALESCE(`ROW_FORMAT`, '')) = 'DYNAMIC'
    ) = 5
    -- Oracle MySQL permits at most 64 secondary indexes per table. Reserve
    -- all required slots before any parent ALTER can auto-commit.
    AND NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`STATISTICS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_agent_turn', 'w_agent_turn_operation',
          'w_agent_turn_settlement_review', 'w_credit_reservation'
        )
        AND `INDEX_NAME` <> 'PRIMARY'
      GROUP BY `TABLE_NAME`
      HAVING COUNT(DISTINCT `INDEX_NAME`) > CASE `TABLE_NAME`
        WHEN 'w_agent_turn' THEN 62
        ELSE 63
      END
    )
    AND NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`PARTITIONS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_agent_turn', 'w_agent_turn_operation',
          'w_agent_turn_settlement_review', 'w_credit_reservation',
          'w_credit_reservation_allocation'
        )
        AND `PARTITION_NAME` IS NOT NULL
    )
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND LOWER(COALESCE(`EXTRA`, '')) NOT LIKE '%generated%'
        AND (
          (`TABLE_NAME` = 'w_agent_turn'
            AND (`COLUMN_NAME`, `COLUMN_TYPE`, `IS_NULLABLE`,
                 COALESCE(`CHARACTER_SET_NAME`, ''), COALESCE(`COLLATION_NAME`, '')) IN (
              ('turn_id', 'varchar(256)', 'NO', 'utf8mb4', 'utf8mb4_bin'),
              ('principal_id', 'varchar(128)', 'NO', 'utf8mb4', 'utf8mb4_bin'),
              ('command_digest', 'varchar(128)', 'NO', 'ascii', 'ascii_bin'),
              ('fencing_token', 'bigint unsigned', 'NO', '', ''),
              ('status', 'varchar(16)', 'NO', 'ascii', 'ascii_bin')
            ))
          OR
          (`TABLE_NAME` = 'w_agent_turn_operation'
            AND (`COLUMN_NAME`, `COLUMN_TYPE`, `IS_NULLABLE`,
                 COALESCE(`CHARACTER_SET_NAME`, ''), COALESCE(`COLLATION_NAME`, '')) IN (
              ('turn_id', 'varchar(256)', 'NO', 'utf8mb4', 'utf8mb4_bin'),
              ('operation_id', 'varchar(128)', 'NO', 'ascii', 'ascii_bin'),
              ('attempt_id', 'varchar(64)', 'NO', 'ascii', 'ascii_bin'),
              ('fencing_token', 'bigint unsigned', 'NO', '', ''),
              ('turn_status', 'varchar(16)', 'NO', 'ascii', 'ascii_bin')
            ))
          OR
          (`TABLE_NAME` = 'w_agent_turn_settlement_review'
            AND (`COLUMN_NAME`, `COLUMN_TYPE`, `IS_NULLABLE`,
                 COALESCE(`CHARACTER_SET_NAME`, ''), COALESCE(`COLLATION_NAME`, '')) IN (
              ('review_id', 'varchar(64)', 'NO', 'ascii', 'ascii_bin'),
              ('turn_id', 'varchar(256)', 'NO', 'utf8mb4', 'utf8mb4_bin'),
              ('settlement_key', 'varchar(256)', 'NO', 'ascii', 'ascii_bin'),
              ('request_digest', 'varchar(128)', 'NO', 'ascii', 'ascii_bin'),
              ('terminal_status', 'varchar(16)', 'NO', 'ascii', 'ascii_bin')
            ))
          OR
          (`TABLE_NAME` = 'w_credit_reservation'
            AND (`COLUMN_NAME`, `COLUMN_TYPE`, `IS_NULLABLE`,
                 COALESCE(`CHARACTER_SET_NAME`, ''), COALESCE(`COLLATION_NAME`, '')) IN (
              ('id', 'bigint unsigned', 'NO', '', ''),
              ('uid', 'int', 'NO', '', ''),
              ('idempotency_key', 'varchar(128)', 'NO', 'utf8mb4', 'utf8mb4_general_ci'),
              ('request_digest', 'varchar(64)', 'YES', 'ascii', 'ascii_bin'),
              ('tool', 'varchar(64)', 'NO', 'utf8mb4', 'utf8mb4_general_ci'),
              ('reserved', 'int', 'NO', '', ''),
              ('project_id', 'int', 'NO', '', '')
            ))
          OR
          (`TABLE_NAME` = 'w_credit_reservation_allocation'
            AND (`COLUMN_NAME`, `COLUMN_TYPE`, `IS_NULLABLE`,
                 COALESCE(`CHARACTER_SET_NAME`, ''), COALESCE(`COLLATION_NAME`, '')) IN (
              ('id', 'bigint unsigned', 'NO', '', ''),
              ('reservation_id', 'bigint unsigned', 'NO', '', ''),
              ('pack_id', 'bigint unsigned', 'NO', '', ''),
              ('credits', 'int', 'NO', '', '')
            ))
        )
    ) = 26
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `TABLE_NAME`, `INDEX_NAME`, `NON_UNIQUE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`,
               SUM(UPPER(`IS_VISIBLE`) <> 'YES') AS `invisible_columns`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND (`TABLE_NAME`, `INDEX_NAME`) IN (
            ('w_agent_turn', 'uk_w_agent_turn_turn_id'),
            ('w_agent_turn_operation', 'uk_w_agent_turn_operation_binding'),
            ('w_agent_turn_settlement_review',
              'uk_w_agent_turn_settlement_review_resolution_binding'),
            ('w_credit_reservation', 'idx_reservation_uid_key'),
            ('w_credit_reservation_allocation',
              'uk_w_credit_reservation_allocation_pair')
          )
        GROUP BY `TABLE_NAME`, `INDEX_NAME`, `NON_UNIQUE`
        HAVING `NON_UNIQUE` = 0
          AND `prefix_columns` = 0
          AND `invisible_columns` = 0
          AND (
            (`TABLE_NAME` = 'w_agent_turn'
              AND `ordered_columns` = 'turn_id')
            OR
            (`TABLE_NAME` = 'w_agent_turn_operation'
              AND `ordered_columns` = 'turn_id,operation_id,attempt_id,fencing_token')
            OR
            (`TABLE_NAME` = 'w_agent_turn_settlement_review'
              AND `ordered_columns` = 'review_id,turn_id,settlement_key,request_digest')
            OR
            (`TABLE_NAME` = 'w_credit_reservation'
              AND `ordered_columns` = 'uid,idempotency_key')
            OR
            (`TABLE_NAME` = 'w_credit_reservation_allocation'
              AND `ordered_columns` = 'reservation_id,pack_id')
          )
      ) AS `required_unique_fingerprints`
    ) = 5
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `TABLE_NAME`, `INDEX_NAME`, `NON_UNIQUE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`,
               SUM(UPPER(`IS_VISIBLE`) <> 'YES') AS `invisible_columns`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` IN (
            'w_credit_reservation', 'w_credit_reservation_allocation'
          )
          AND `INDEX_NAME` = 'PRIMARY'
        GROUP BY `TABLE_NAME`, `INDEX_NAME`, `NON_UNIQUE`
        HAVING `NON_UNIQUE` = 0
          AND `ordered_columns` = 'id'
          AND `prefix_columns` = 0
          AND `invisible_columns` = 0
      ) AS `required_primary_fingerprints`
    ) = 2
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
      INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
        ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
        AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
      WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
        AND `tc`.`TABLE_NAME` = 'w_credit_reservation'
        AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
        AND UPPER(`tc`.`ENFORCED`) = 'YES'
        AND `tc`.`CONSTRAINT_NAME` IN (
          'chk_w_credit_reservation_status',
          'chk_w_credit_reservation_amounts',
          'chk_w_credit_reservation_digests',
          'chk_w_credit_reservation_bounded_codes',
          'chk_w_credit_reservation_hold_tuple',
          'chk_w_credit_reservation_review_state',
          'chk_w_credit_reservation_refund_tuple',
          'chk_w_credit_reservation_status_time',
          'chk_w_credit_reservation_refund_error_code',
          'chk_w_credit_reservation_lifecycle_time'
        )
    ) = 10
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
      INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
        ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
        AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
      WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
        AND `tc`.`TABLE_NAME` = 'w_credit_reservation_allocation'
        AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
        AND UPPER(`tc`.`ENFORCED`) = 'YES'
        AND `tc`.`CONSTRAINT_NAME` = 'chk_w_credit_reservation_allocation_credits'
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `rc`.`TABLE_NAME`, `rc`.`CONSTRAINT_NAME`,
               `rc`.`UPDATE_RULE`, `rc`.`DELETE_RULE`,
               GROUP_CONCAT(`kcu`.`COLUMN_NAME`
                 ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `ordered_columns`,
               GROUP_CONCAT(`kcu`.`REFERENCED_COLUMN_NAME`
                 ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `ordered_referenced_columns`,
               COUNT(*) AS `column_count`,
               SUM(`kcu`.`REFERENCED_TABLE_SCHEMA` <> DATABASE()
                 OR `kcu`.`REFERENCED_TABLE_NAME` <> 'w_credit_reservation') AS `wrong_parent_columns`
        FROM `information_schema`.`REFERENTIAL_CONSTRAINTS` AS `rc`
        INNER JOIN `information_schema`.`KEY_COLUMN_USAGE` AS `kcu`
          ON `kcu`.`CONSTRAINT_SCHEMA` = `rc`.`CONSTRAINT_SCHEMA`
          AND `kcu`.`TABLE_NAME` = `rc`.`TABLE_NAME`
          AND `kcu`.`CONSTRAINT_NAME` = `rc`.`CONSTRAINT_NAME`
        WHERE `rc`.`CONSTRAINT_SCHEMA` = DATABASE()
          AND `rc`.`TABLE_NAME` = 'w_credit_reservation_allocation'
          AND `rc`.`CONSTRAINT_NAME` = 'fk_w_credit_reservation_allocation_reservation'
        GROUP BY `rc`.`TABLE_NAME`, `rc`.`CONSTRAINT_NAME`,
                 `rc`.`UPDATE_RULE`, `rc`.`DELETE_RULE`
        HAVING `rc`.`UPDATE_RULE` = 'RESTRICT'
          AND `rc`.`DELETE_RULE` = 'RESTRICT'
          AND `ordered_columns` = 'reservation_id'
          AND `ordered_referenced_columns` = 'id'
          AND `column_count` = 1
          AND `wrong_parent_columns` = 0
      ) AS `required_reservation_fk_fingerprint`
    ) = 1
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_settlement_baseline_guard`;

-- No business backfill is possible. An existing Turn, Review hold or
-- non-canonical Reservation digest requires operator reconciliation outside
-- this migration. In particular, never infer a Reservation from a Principal,
-- idempotency key, current Pack balance or hold_settlement_key.
CREATE TEMPORARY TABLE `_w_agent_settlement_history_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_settlement_history_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_agent_settlement_history_guard` (`guard_key`)
SELECT CASE
  WHEN NOT EXISTS (SELECT 1 FROM `w_agent_turn` LIMIT 1)
    AND NOT EXISTS (
      SELECT 1
      FROM `w_credit_reservation`
      WHERE BINARY `status` = 'review_hold'
        OR `hold_review_id` IS NOT NULL
        OR `hold_settlement_key` IS NOT NULL
        OR `hold_request_digest` IS NOT NULL
        OR `review_held_at` IS NOT NULL
    )
    AND NOT EXISTS (
      SELECT 1
      FROM `w_credit_reservation`
      WHERE `request_digest` IS NOT NULL
        AND (
          OCTET_LENGTH(`request_digest`) <> 64
          OR BINARY `request_digest` REGEXP '[^0-9a-f]'
        )
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_settlement_history_guard`;

-- A clean run owns none of the target tables, parent indexes or new FK/CHECK
-- names. This makes a partial previous run fail before another persistent DDL
-- statement and forces exact forward repair rather than IF NOT EXISTS drift.
CREATE TEMPORARY TABLE `_w_agent_settlement_target_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_settlement_target_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_agent_settlement_target_guard` (`guard_key`)
SELECT CASE
  WHEN NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`TABLES`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_agent_turn_reservation_binding',
          'w_agent_turn_settlement_outcome'
        )
    )
    AND NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`STATISTICS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND (`TABLE_NAME`, `INDEX_NAME`) IN (
          ('w_agent_turn', 'uk_w_agent_turn_reservation_identity'),
          ('w_agent_turn', 'uk_w_agent_turn_settlement_fence'),
          ('w_agent_turn_operation', 'uk_w_agent_turn_operation_settlement_binding'),
          ('w_agent_turn_settlement_review',
            'uk_w_agent_turn_settlement_review_outcome_binding'),
          ('w_credit_reservation', 'uk_w_credit_reservation_agent_binding')
        )
    )
    AND NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`TABLE_CONSTRAINTS`
      WHERE `CONSTRAINT_SCHEMA` = DATABASE()
        AND `CONSTRAINT_NAME` IN (
          'chk_w_agent_turn_reservation_binding_identity',
          'chk_w_agent_turn_reservation_binding_amounts',
          'chk_w_agent_turn_reservation_binding_digests',
          'chk_w_agent_turn_settlement_outcome_identity',
          'chk_w_agent_turn_settlement_outcome_digests',
          'chk_w_agent_turn_settlement_outcome_authorization',
          'chk_w_agent_turn_settlement_outcome_terminal',
          'chk_w_agent_turn_settlement_outcome_amounts',
          'chk_w_agent_turn_settlement_outcome_review_tuple',
          'chk_w_agent_turn_settlement_outcome_resolution_tuple',
          'chk_w_agent_turn_settlement_outcome_state_tuple',
          'chk_w_agent_turn_settlement_outcome_updated_time',
          'fk_w_agent_turn_reservation_binding_turn',
          'fk_w_agent_turn_reservation_binding_reservation',
          'fk_w_agent_turn_settlement_outcome_binding',
          'fk_w_agent_turn_settlement_outcome_turn_fence',
          'fk_w_agent_turn_settlement_outcome_operation',
          'fk_w_agent_turn_settlement_outcome_review'
        )
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_settlement_target_guard`;

-- The Turn identity and final fence become immutable once a child binding or
-- outcome exists. turn_id is already unique; these redundant full-column keys
-- are intentional FK targets and must not be replaced by prefix indexes.
ALTER TABLE `w_agent_turn`
  ADD UNIQUE KEY `uk_w_agent_turn_reservation_identity`
    (`turn_id`, `principal_id`, `command_digest`),
  ADD UNIQUE KEY `uk_w_agent_turn_settlement_fence`
    (`turn_id`, `fencing_token`, `status`);

-- Bind an executor outcome to the immutable terminal status recorded by its
-- Operation receipt, not merely to a caller-supplied Operation identity.
ALTER TABLE `w_agent_turn_operation`
  ADD UNIQUE KEY `uk_w_agent_turn_operation_settlement_binding`
    (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`, `turn_status`);

-- Bind a review-owned outcome to the exact terminal status frozen by Review.
ALTER TABLE `w_agent_turn_settlement_review`
  ADD UNIQUE KEY `uk_w_agent_turn_settlement_review_outcome_binding`
    (`review_id`, `turn_id`, `settlement_key`, `request_digest`, `terminal_status`);

-- Reservation.tool becomes a binary identity before it participates in the
-- exact binding FK. The strengthened digest CHECK accepts NULL only for rows
-- predating P0-046; every new Agent binding requires a canonical non-NULL hex
-- request digest. The composite key freezes the exact commercial snapshot.
ALTER TABLE `w_credit_reservation`
  MODIFY COLUMN `tool` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  DROP CONSTRAINT `chk_w_credit_reservation_digests`,
  ADD UNIQUE KEY `uk_w_credit_reservation_agent_binding`
    (`id`, `uid`, `request_digest`, `tool`, `reserved`, `project_id`),
  ADD CONSTRAINT `chk_w_credit_reservation_digests`
    CHECK (
      (`request_digest` IS NULL OR (
        OCTET_LENGTH(`request_digest`) = 64
        AND BINARY `request_digest` NOT REGEXP '[^0-9a-f]'
      ))
      AND (`hold_request_digest` IS NULL OR (
        OCTET_LENGTH(`hold_request_digest`) = 71
        AND BINARY LEFT(`hold_request_digest`, 7) = 'sha256:'
        AND BINARY SUBSTRING(`hold_request_digest`, 8) NOT REGEXP '[^0-9a-f]'
      ))
    );

CREATE TABLE `w_agent_turn_reservation_binding` (
  `id`                          bigint unsigned NOT NULL AUTO_INCREMENT,
  `binding_id`                  char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                                  COMMENT 'Server-generated immutable lowercase-hex binding identity',
  `turn_id`                     varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `principal_id`                varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `turn_command_digest`         varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `reservation_id`              bigint unsigned NOT NULL,
  `reservation_uid`             int NOT NULL,
  `reservation_request_digest` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `reservation_tool`            varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `reserved_units`              int NOT NULL,
  `project_id`                  int NOT NULL,
  `pricing_snapshot_digest`     char(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `binding_digest`              char(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `created_at`                  datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_reservation_binding_binding_id` (`binding_id`),
  UNIQUE KEY `uk_w_agent_turn_reservation_binding_turn_id` (`turn_id`),
  UNIQUE KEY `uk_w_agent_turn_reservation_binding_reservation_id` (`reservation_id`),
  UNIQUE KEY `uk_w_agent_turn_reservation_binding_exact`
    (`binding_id`, `turn_id`, `reservation_id`, `binding_digest`, `reserved_units`),
  CONSTRAINT `chk_w_agent_turn_reservation_binding_identity`
    CHECK (
      OCTET_LENGTH(`binding_id`) = 64
      AND BINARY `binding_id` NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`principal_id`) BETWEEN 1 AND 128
      AND OCTET_LENGTH(`turn_command_digest`) BETWEEN 1 AND 128
      AND BINARY `turn_command_digest` NOT REGEXP '[^!-~]'
      AND `reservation_uid` > 0
      AND OCTET_LENGTH(`reservation_request_digest`) = 64
      AND BINARY `reservation_request_digest` NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`reservation_tool`) BETWEEN 1 AND 64
      AND `reservation_tool` = TRIM(`reservation_tool`)
    ),
  CONSTRAINT `chk_w_agent_turn_reservation_binding_amounts`
    CHECK (
      `reserved_units` BETWEEN 0 AND 2147483647
      AND `project_id` >= 0
    ),
  CONSTRAINT `chk_w_agent_turn_reservation_binding_digests`
    CHECK (
      OCTET_LENGTH(`pricing_snapshot_digest`) = 71
      AND BINARY LEFT(`pricing_snapshot_digest`, 7) = 'sha256:'
      AND BINARY SUBSTRING(`pricing_snapshot_digest`, 8) NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`binding_digest`) = 71
      AND BINARY LEFT(`binding_digest`, 7) = 'sha256:'
      AND BINARY SUBSTRING(`binding_digest`, 8) NOT REGEXP '[^0-9a-f]'
    ),
  CONSTRAINT `fk_w_agent_turn_reservation_binding_turn`
    FOREIGN KEY (`turn_id`, `principal_id`, `turn_command_digest`)
    REFERENCES `w_agent_turn` (`turn_id`, `principal_id`, `command_digest`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_turn_reservation_binding_reservation`
    FOREIGN KEY (`reservation_id`, `reservation_uid`, `reservation_request_digest`,
                 `reservation_tool`, `reserved_units`, `project_id`)
    REFERENCES `w_credit_reservation`
      (`id`, `uid`, `request_digest`, `tool`, `reserved`, `project_id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Immutable one-to-one Agent Turn and Credits Reservation admission binding';

CREATE TABLE `w_agent_turn_settlement_outcome` (
  `id`                         bigint unsigned NOT NULL AUTO_INCREMENT,
  `outcome_id`                 char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `binding_id`                 char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `turn_id`                    varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `reservation_id`             bigint unsigned NOT NULL,
  `binding_digest`             char(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `settlement_key`             varchar(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `ledger_request_digest`      char(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `authorization_kind`         varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `attempt_id`                 varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `fencing_token`              bigint unsigned NOT NULL,
  `operation_id`               varchar(128) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `terminal_status`            varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `requested_intent`           varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `used_units`                 int DEFAULT NULL,
  `reserved_units`             int NOT NULL,
  `status`                     varchar(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `refund_target`              varchar(16) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `refund_due`                 int NOT NULL DEFAULT 0,
  `reservation_state_version` bigint unsigned NOT NULL,
  `review_id`                  varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `review_request_digest`      varchar(128) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `resolution_id`              varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `resolution_request_digest` varchar(128) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  `outcome_digest`             char(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `created_at`                 datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at`                 datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                                 COMMENT 'Written explicitly by settlement CAS; no ON UPDATE clause',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_outcome_outcome_id` (`outcome_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_outcome_settlement_key` (`settlement_key`),
  UNIQUE KEY `uk_w_agent_turn_settlement_outcome_turn_id` (`turn_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_outcome_reservation_id` (`reservation_id`),
  UNIQUE KEY `uk_w_agent_turn_settlement_outcome_review_id` (`review_id`),
  KEY `idx_w_agent_turn_settlement_outcome_recovery` (`status`, `updated_at`, `id`),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_identity`
    CHECK (
      OCTET_LENGTH(`outcome_id`) = 64
      AND BINARY `outcome_id` NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`binding_id`) = 64
      AND BINARY `binding_id` NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`turn_id`) BETWEEN 1 AND 256
      AND OCTET_LENGTH(`settlement_key`) = 86
      AND BINARY LEFT(`settlement_key`, 22) = 'wm:turn-settlement:v1:'
      AND BINARY SUBSTRING(`settlement_key`, 23) NOT REGEXP '[^0-9a-f]'
      AND `fencing_token` BETWEEN 1 AND 9223372036854775807
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_digests`
    CHECK (
      OCTET_LENGTH(`binding_digest`) = 71
      AND BINARY LEFT(`binding_digest`, 7) = 'sha256:'
      AND BINARY SUBSTRING(`binding_digest`, 8) NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`ledger_request_digest`) = 71
      AND BINARY LEFT(`ledger_request_digest`, 7) = 'sha256:'
      AND BINARY SUBSTRING(`ledger_request_digest`, 8) NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`outcome_digest`) = 71
      AND BINARY LEFT(`outcome_digest`, 7) = 'sha256:'
      AND BINARY SUBSTRING(`outcome_digest`, 8) NOT REGEXP '[^0-9a-f]'
      AND (`review_request_digest` IS NULL OR (
        OCTET_LENGTH(`review_request_digest`) = 71
        AND BINARY LEFT(`review_request_digest`, 7) = 'sha256:'
        AND BINARY SUBSTRING(`review_request_digest`, 8) NOT REGEXP '[^0-9a-f]'
      ))
      AND (`resolution_request_digest` IS NULL OR (
        OCTET_LENGTH(`resolution_request_digest`) = 71
        AND BINARY LEFT(`resolution_request_digest`, 7) = 'sha256:'
        AND BINARY SUBSTRING(`resolution_request_digest`, 8) NOT REGEXP '[^0-9a-f]'
      ))
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_authorization`
    CHECK (
      (`authorization_kind` = 'operation'
        AND `attempt_id` IS NOT NULL
        AND OCTET_LENGTH(`attempt_id`) BETWEEN 1 AND 64
        AND `operation_id` IS NOT NULL
        AND OCTET_LENGTH(`operation_id`) BETWEEN 1 AND 128)
      OR
      (`authorization_kind` = 'reconcile'
        AND `attempt_id` IS NULL AND `operation_id` IS NULL)
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_terminal`
    CHECK (`terminal_status` IN ('completed', 'stopped', 'failed', 'timeout')),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_amounts`
    CHECK (
      `reserved_units` BETWEEN 0 AND 2147483647
      AND (`used_units` IS NULL OR `used_units` BETWEEN 0 AND `reserved_units`)
      AND `refund_due` BETWEEN 0 AND `reserved_units`
      AND `reservation_state_version` BETWEEN 1 AND 9223372036854775807
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_review_tuple`
    CHECK (
      (`requested_intent` = 'review'
        AND `review_id` IS NOT NULL
        AND OCTET_LENGTH(`review_id`) BETWEEN 1 AND 64
        AND `review_request_digest` IS NOT NULL)
      OR
      (`requested_intent` IN ('finalize', 'release')
        AND `review_id` IS NULL AND `review_request_digest` IS NULL)
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_resolution_tuple`
    CHECK (
      (`resolution_id` IS NULL AND `resolution_request_digest` IS NULL)
      OR
      (`requested_intent` = 'review'
        AND `resolution_id` IS NOT NULL
        AND OCTET_LENGTH(`resolution_id`) = 64
        AND BINARY `resolution_id` NOT REGEXP '[^0-9a-f]'
        AND `resolution_request_digest` IS NOT NULL
        AND `status` IN ('refund_pending', 'finalized'))
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_state_tuple`
    CHECK (
      (`status` = 'review_held'
        AND `requested_intent` = 'review'
        AND `used_units` IS NULL
        AND `refund_target` IS NULL AND `refund_due` = 0
        AND `resolution_id` IS NULL AND `resolution_request_digest` IS NULL)
      OR
      (`status` = 'refund_pending'
        AND `used_units` IS NOT NULL
        AND `refund_target` IN ('finalized', 'released')
        AND `refund_due` > 0
        AND `refund_due` = `reserved_units` - `used_units`
        AND (
          (`refund_target` = 'finalized'
            AND `requested_intent` IN ('finalize', 'review')
            AND (`requested_intent` <> 'review' OR `resolution_id` IS NOT NULL))
          OR
          (`refund_target` = 'released'
            AND `requested_intent` = 'release' AND `used_units` = 0)
        ))
      OR
      (`status` = 'finalized'
        AND `requested_intent` IN ('finalize', 'review')
        AND `used_units` IS NOT NULL
        AND `refund_target` IS NULL AND `refund_due` = 0
        AND (`requested_intent` <> 'review' OR `resolution_id` IS NOT NULL))
      OR
      (`status` = 'released'
        AND `requested_intent` = 'release' AND `used_units` = 0
        AND `refund_target` IS NULL AND `refund_due` = 0
        AND `resolution_id` IS NULL AND `resolution_request_digest` IS NULL)
    ),
  CONSTRAINT `chk_w_agent_turn_settlement_outcome_updated_time`
    CHECK (`updated_at` >= `created_at`),
  CONSTRAINT `fk_w_agent_turn_settlement_outcome_binding`
    FOREIGN KEY (`binding_id`, `turn_id`, `reservation_id`, `binding_digest`, `reserved_units`)
    REFERENCES `w_agent_turn_reservation_binding`
      (`binding_id`, `turn_id`, `reservation_id`, `binding_digest`, `reserved_units`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_turn_settlement_outcome_turn_fence`
    FOREIGN KEY (`turn_id`, `fencing_token`, `terminal_status`)
    REFERENCES `w_agent_turn` (`turn_id`, `fencing_token`, `status`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_turn_settlement_outcome_operation`
    FOREIGN KEY (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`, `terminal_status`)
    REFERENCES `w_agent_turn_operation`
      (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`, `turn_status`)
    ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `fk_w_agent_turn_settlement_outcome_review`
    FOREIGN KEY (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `terminal_status`)
    REFERENCES `w_agent_turn_settlement_review`
      (`review_id`, `turn_id`, `settlement_key`, `request_digest`, `terminal_status`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='One monotonic financial outcome per Agent Turn, Reservation and SettlementKey';

-- `review_held` records only financial ownership; Agent Review remains the
-- authority for pending/metered/finalized_held and Effect delivery remains
-- separately fenced. Bound Agent Reservations must never be expired directly:
-- the sweeper must first lock the Turn owner and reconcile it to a SettlementKey.
-- A bound+expired row is an integrity incident, not a state to invent here.
