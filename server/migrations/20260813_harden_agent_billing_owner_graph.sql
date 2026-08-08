-- P0-048 follow-up: close the remaining Agent billing owner-graph schema gaps.
--
-- Runtime contract: Oracle MySQL 8.0.19+ only. This migration adds exactly:
--
--   * chk_w_global_project_budget_credits
--   * fk_w_credit_reservation_allocation_pack
--   * idx_w_order_membership_resolution
--
-- Rollout and auto-commit safety:
--   1. stop Project-budget, Reservation/Allocation, CreditsPack, Order,
--      membership and payment writers, then drain their transactions;
--   2. execute every statement on one physical MySQL session and stop on the
--      first error; TEMPORARY guards, user variables and GET_LOCK are
--      connection-scoped;
--   3. keep the external maintenance fence until every post-guard succeeds;
--   4. do not assume that a client error rolled DDL back. Each ALTER TABLE is
--      individually atomic on MySQL 8, but the three-ALTER migration is not a
--      transaction: successful DDL auto-commits and remains durable;
--   5. after a disconnect or ambiguous result, close the failed session, use a
--      fresh read-only session to classify every target as ABSENT, EXACT or
--      DRIFT, and only then forward-resume this file. Never retry blindly.
--
-- ABSENT means that neither the reserved name nor an equivalent/touching
-- fingerprint exists. EXACT means that the reserved name has the full expected
-- fingerprint and is the only equivalent/touching object. Every other state is
-- DRIFT and aborts before the first persistent DDL. A partial prior run is safe:
-- EXACT targets become a prepared SELECT no-op, while ABSENT targets receive
-- their one reviewed ALTER. Every conditional ALTER is followed immediately by
-- a fresh exact information_schema post-guard.
--
-- There is intentionally no UPDATE, DELETE, business INSERT, column MODIFY,
-- backfill, IF NOT EXISTS, DROP CONSTRAINT or DROP INDEX. Negative project
-- budgets, orphan/cross-owner Allocations, incompatible legacy Order columns,
-- name collisions and renamed-equivalent objects require separately reviewed
-- reconciliation. Rollback is forward repair; dropping an additive object after
-- new writes can destroy a now-relied-upon invariant.

-- Fail mechanically before CHECK-based guards are used. Row 0 is the rejection
-- sentinel; an incompatible server attempts to insert 0 again and gets a
-- duplicate-primary-key error. REGEXP_SUBSTR also makes MySQL 5.7 fail closed.
CREATE TEMPORARY TABLE `_w_agent_billing_owner_version_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_billing_owner_version_guard` (`guard_key`) VALUES (0);

SET @w_agent_billing_owner_version_core =
  REGEXP_SUBSTR(VERSION(), '^[0-9]+[.][0-9]+[.][0-9]+');
SET @w_agent_billing_owner_version_major =
  CAST(SUBSTRING_INDEX(@w_agent_billing_owner_version_core, '.', 1) AS UNSIGNED);
SET @w_agent_billing_owner_version_minor =
  CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(@w_agent_billing_owner_version_core, '.', 2), '.', -1) AS UNSIGNED);
SET @w_agent_billing_owner_version_patch =
  CAST(SUBSTRING_INDEX(@w_agent_billing_owner_version_core, '.', -1) AS UNSIGNED);

INSERT INTO `_w_agent_billing_owner_version_guard` (`guard_key`)
SELECT CASE
  WHEN LOCATE('mariadb', LOWER(VERSION())) = 0
    AND LOCATE('mariadb', LOWER(@@version_comment)) = 0
    AND @w_agent_billing_owner_version_core IS NOT NULL
    AND (
      @w_agent_billing_owner_version_major > 8
      OR (
        @w_agent_billing_owner_version_major = 8
        AND (
          @w_agent_billing_owner_version_minor > 0
          OR (
            @w_agent_billing_owner_version_minor = 0
            AND @w_agent_billing_owner_version_patch >= 19
          )
        )
      )
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_billing_owner_version_guard`;

-- Serialize reviewed execution for this database. GET_LOCK is only a migration
-- mutex; it is not the external writer fence required above.
SET @w_agent_billing_owner_lock_name =
  CONCAT('wm:20260813:', LEFT(SHA2(DATABASE(), 256), 48));
SET @w_agent_billing_owner_lock_acquired =
  GET_LOCK(@w_agent_billing_owner_lock_name, 0);

CREATE TEMPORARY TABLE `_w_agent_billing_owner_session_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_billing_owner_session_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_agent_billing_owner_session_guard` (`guard_key`)
SELECT CASE
  WHEN DATABASE() IS NOT NULL
    AND @w_agent_billing_owner_lock_acquired = 1
    AND @@SESSION.foreign_key_checks = 1
    AND @@SESSION.check_constraint_checks = 1
    AND @@SESSION.unique_checks = 1
    AND @@SESSION.time_zone = '+00:00'
    AND TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(6), CURRENT_TIMESTAMP(6)) = 0
    AND UPPER(@@SESSION.transaction_isolation) IN ('READ-COMMITTED', 'REPEATABLE-READ')
    AND @@innodb_page_size >= 16384
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_billing_owner_session_guard`;

-- Exact target-table and column compatibility. The financial columns were
-- migration-owned and are pinned to their historical definitions. w_order is a
-- legacy table whose exact null/default/collation differs across supported
-- installations, so this migration does not silently normalize it. Instead it
-- accepts only a closed, non-generated, full-column-indexable family and proves
-- the maximum character payload fits a 3072-byte InnoDB key on the required
-- 16KiB page size plus DYNAMIC row format. COMPACT/REDUNDANT retains the
-- legacy 767-byte limit, while COMPRESSED can still fail index creation for a
-- smaller KEY_BLOCK_SIZE or incompressible existing rows; all three are
-- rejected before any DDL. A later legacy-column migration may narrow that
-- contract.
CREATE TEMPORARY TABLE `_w_agent_billing_owner_schema_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_billing_owner_schema_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_agent_billing_owner_schema_guard` (`guard_key`)
SELECT CASE
  WHEN (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLES`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_global_project',
          'w_credit_reservation',
          'w_credit_reservation_allocation',
          'w_credits_pack',
          'w_order'
        )
        AND `TABLE_TYPE` = 'BASE TABLE'
        AND UPPER(`ENGINE`) = 'INNODB'
    ) = 5
    AND NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`PARTITIONS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_global_project',
          'w_credit_reservation',
          'w_credit_reservation_allocation',
          'w_credits_pack',
          'w_order'
        )
        AND `PARTITION_NAME` IS NOT NULL
    )
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLES`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_order'
        AND UPPER(`ROW_FORMAT`) = 'DYNAMIC'
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_global_project'
        AND (
          (`COLUMN_NAME` = 'budget_credits_cap'
            AND `DATA_TYPE` = 'int'
            AND `COLUMN_TYPE` = 'int'
            AND `IS_NULLABLE` = 'YES'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND `COLUMN_DEFAULT` IS NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
          OR
          (`COLUMN_NAME` = 'budget_credits_used'
            AND `DATA_TYPE` = 'int'
            AND `COLUMN_TYPE` = 'int'
            AND `IS_NULLABLE` = 'NO'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND CAST(`COLUMN_DEFAULT` AS CHAR) = '0'
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
        )
    ) = 2
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND (
          (`TABLE_NAME` = 'w_credit_reservation_allocation'
            AND `COLUMN_NAME` IN ('reservation_id', 'pack_id')
            AND `DATA_TYPE` = 'bigint'
            AND `COLUMN_TYPE` = 'bigint unsigned'
            AND `IS_NULLABLE` = 'NO'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
          OR
          (`TABLE_NAME` = 'w_credit_reservation'
            AND `COLUMN_NAME` = 'id'
            AND `DATA_TYPE` = 'bigint'
            AND `COLUMN_TYPE` = 'bigint unsigned'
            AND `IS_NULLABLE` = 'NO'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND LOWER(`EXTRA`) LIKE '%auto_increment%')
          OR
          (`TABLE_NAME` = 'w_credit_reservation'
            AND `COLUMN_NAME` = 'uid'
            AND `DATA_TYPE` = 'int'
            AND `COLUMN_TYPE` = 'int'
            AND `IS_NULLABLE` = 'NO'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
          OR
          (`TABLE_NAME` = 'w_credits_pack'
            AND `COLUMN_NAME` = 'id'
            AND `DATA_TYPE` = 'bigint'
            AND `COLUMN_TYPE` = 'bigint unsigned'
            AND `IS_NULLABLE` = 'NO'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND LOWER(`EXTRA`) LIKE '%auto_increment%')
          OR
          (`TABLE_NAME` = 'w_credits_pack'
            AND `COLUMN_NAME` = 'uid'
            AND `DATA_TYPE` = 'bigint'
            AND `COLUMN_TYPE` = 'bigint'
            AND `IS_NULLABLE` = 'NO'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
        )
    ) = 6
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               COUNT(*) AS `key_parts`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_parts`,
               SUM(`EXPRESSION` IS NOT NULL) AS `expression_parts`,
               SUM(COALESCE(`IS_VISIBLE`, 'NO') <> 'YES') AS `invisible_parts`,
               SUM(COALESCE(`COLLATION`, '') <> 'A') AS `nonascending_parts`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_credits_pack'
          AND `INDEX_NAME` = 'PRIMARY'
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`
        HAVING `NON_UNIQUE` = 0
          AND UPPER(`INDEX_TYPE`) = 'BTREE'
          AND `ordered_columns` = 'id'
          AND `key_parts` = 1
          AND `prefix_parts` = 0
          AND `expression_parts` = 0
          AND `invisible_parts` = 0
          AND `nonascending_parts` = 0
      ) AS `w_credits_pack_primary_fingerprint`
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               COUNT(*) AS `key_parts`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_parts`,
               SUM(`EXPRESSION` IS NOT NULL) AS `expression_parts`,
               SUM(COALESCE(`IS_VISIBLE`, 'NO') <> 'YES') AS `invisible_parts`,
               SUM(COALESCE(`COLLATION`, '') <> 'A') AS `nonascending_parts`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_credit_reservation_allocation'
          AND `INDEX_NAME` = 'idx_credit_reservation_allocation_pack'
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`
        HAVING `NON_UNIQUE` = 1
          AND UPPER(`INDEX_TYPE`) = 'BTREE'
          AND `ordered_columns` = 'pack_id'
          AND `key_parts` = 1
          AND `prefix_parts` = 0
          AND `expression_parts` = 0
          AND `invisible_parts` = 0
          AND `nonascending_parts` = 0
      ) AS `w_allocation_pack_index_fingerprint`
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_order'
        AND (
          (`COLUMN_NAME` = 'id'
            AND `DATA_TYPE` IN ('int', 'bigint')
            AND `COLUMN_TYPE` IN ('int', 'int unsigned', 'bigint', 'bigint unsigned')
            AND `IS_NULLABLE` = 'NO'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` IS NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
          OR
          (`COLUMN_NAME` = 'uid'
            AND `DATA_TYPE` IN ('int', 'bigint')
            AND `COLUMN_TYPE` IN ('int', 'int unsigned', 'bigint', 'bigint unsigned')
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
          OR
          (`COLUMN_NAME` IN ('order_type', 'status')
            AND `DATA_TYPE` IN ('char', 'varchar')
            AND `CHARACTER_MAXIMUM_LENGTH` BETWEEN 1 AND 255
            AND `CHARACTER_SET_NAME` IS NOT NULL
            AND `COLLATION_NAME` IS NOT NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
          OR
          (`COLUMN_NAME` = 'pay_time'
            AND `DATA_TYPE` IN ('datetime', 'timestamp')
            AND `IS_NULLABLE` = 'YES'
            AND `CHARACTER_SET_NAME` IS NULL
            AND `COLLATION_NAME` IS NULL
            AND `DATETIME_PRECISION` BETWEEN 0 AND 6
            AND `COLUMN_DEFAULT` IS NULL
            AND LOWER(`EXTRA`) NOT LIKE '%generated%')
        )
    ) = 5
    AND (
      SELECT COALESCE(SUM(`CHARACTER_OCTET_LENGTH`), 3073)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_order'
        AND `COLUMN_NAME` IN ('order_type', 'status')
    ) <= 3000
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               COUNT(*) AS `key_parts`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_parts`,
               SUM(`EXPRESSION` IS NOT NULL) AS `expression_parts`,
               SUM(COALESCE(`IS_VISIBLE`, 'NO') <> 'YES') AS `invisible_parts`,
               SUM(COALESCE(`COLLATION`, '') <> 'A') AS `nonascending_parts`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_order'
          AND `INDEX_NAME` = 'PRIMARY'
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`
        HAVING `NON_UNIQUE` = 0
          AND UPPER(`INDEX_TYPE`) = 'BTREE'
          AND `ordered_columns` = 'id'
          AND `key_parts` = 1
          AND `prefix_parts` = 0
          AND `expression_parts` = 0
          AND `invisible_parts` = 0
          AND `nonascending_parts` = 0
      ) AS `w_order_primary_fingerprint`
    ) = 1
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_billing_owner_schema_guard`;

-- Data compatibility is checked without rewriting business rows. The CHECK
-- intentionally does not require used <= cap: lowering a cap below historical
-- usage is allowed and simply blocks further positive reservations.
CREATE TEMPORARY TABLE `_w_agent_billing_owner_data_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_billing_owner_data_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_agent_billing_owner_data_guard` (`guard_key`)
SELECT CASE
  WHEN NOT EXISTS (
      SELECT 1
      FROM `w_global_project`
      WHERE (`budget_credits_cap` IS NOT NULL AND `budget_credits_cap` < 0)
         OR `budget_credits_used` < 0
    )
    AND NOT EXISTS (
      SELECT 1
      FROM `w_credit_reservation_allocation` AS `a`
      LEFT JOIN `w_credit_reservation` AS `r`
        ON `r`.`id` = `a`.`reservation_id`
      LEFT JOIN `w_credits_pack` AS `p`
        ON `p`.`id` = `a`.`pack_id`
      WHERE `r`.`id` IS NULL
         OR `p`.`id` IS NULL
         OR `r`.`uid` <> `p`.`uid`
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_billing_owner_data_guard`;

-- Canonical CHECK_CLAUSE fingerprint. MySQL may expose one redundant outer
-- pair; no other token, grouping or predicate variation is accepted.
SET @w_agent_billing_budget_clause =
  '(budget_credits_capisnullorbudget_credits_cap>=0)andbudget_credits_used>=0';
SET @w_agent_billing_budget_clause_mysql =
  '((budget_credits_capisnull)or(budget_credits_cap>=0))and(budget_credits_used>=0)';

SET @w_agent_billing_budget_name_count = (
  SELECT COUNT(*)
  FROM `information_schema`.`TABLE_CONSTRAINTS`
  WHERE `CONSTRAINT_SCHEMA` = DATABASE()
    AND `CONSTRAINT_NAME` = 'chk_w_global_project_budget_credits'
);

SET @w_agent_billing_budget_exact_count = (
  SELECT COUNT(*)
  FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
  INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
    ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
    AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
  WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
    AND `tc`.`TABLE_NAME` = 'w_global_project'
    AND `tc`.`CONSTRAINT_NAME` = 'chk_w_global_project_budget_credits'
    AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
    AND `tc`.`ENFORCED` = 'YES'
    AND LOWER(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(
          `cc`.`CHECK_CLAUSE`, '`', ''), ' ', ''), CHAR(9), ''), CHAR(10), ''), CHAR(13), ''))
      IN (
        @w_agent_billing_budget_clause,
        CONCAT('(', @w_agent_billing_budget_clause, ')'),
        @w_agent_billing_budget_clause_mysql,
        CONCAT('(', @w_agent_billing_budget_clause_mysql, ')')
      )
);

SET @w_agent_billing_budget_shape_count = (
  SELECT COUNT(*)
  FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
  INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
    ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
    AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
  WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
    AND `tc`.`TABLE_NAME` = 'w_global_project'
    AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
    AND LOWER(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(
          `cc`.`CHECK_CLAUSE`, '`', ''), ' ', ''), CHAR(9), ''), CHAR(10), ''), CHAR(13), ''))
      IN (
        @w_agent_billing_budget_clause,
        CONCAT('(', @w_agent_billing_budget_clause, ')'),
        @w_agent_billing_budget_clause_mysql,
        CONCAT('(', @w_agent_billing_budget_clause_mysql, ')')
      )
);

SET @w_agent_billing_budget_touch_count = (
  SELECT COUNT(*)
  FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
  INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
    ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
    AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
  WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
    AND `tc`.`TABLE_NAME` = 'w_global_project'
    AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
    AND (
      LOCATE('budget_credits_cap', LOWER(REPLACE(`cc`.`CHECK_CLAUSE`, '`', ''))) > 0
      OR LOCATE('budget_credits_used', LOWER(REPLACE(`cc`.`CHECK_CLAUSE`, '`', ''))) > 0
    )
);

SET @w_agent_billing_budget_state = CASE
  WHEN @w_agent_billing_budget_name_count = 0
    AND @w_agent_billing_budget_shape_count = 0
    AND @w_agent_billing_budget_touch_count = 0
  THEN 'ABSENT'
  WHEN @w_agent_billing_budget_name_count = 1
    AND @w_agent_billing_budget_exact_count = 1
    AND @w_agent_billing_budget_shape_count = 1
    AND @w_agent_billing_budget_touch_count = 1
  THEN 'EXACT'
  ELSE 'DRIFT'
END;

-- Full FK classification. Any foreign key that touches pack_id is owned by
-- this migration; a composite, renamed, wrong-parent or wrong-action variant is
-- DRIFT rather than a partial match.
SET @w_agent_billing_pack_fk_name_count = (
  SELECT COUNT(*)
  FROM `information_schema`.`TABLE_CONSTRAINTS`
  WHERE `CONSTRAINT_SCHEMA` = DATABASE()
    AND `CONSTRAINT_NAME` = 'fk_w_credit_reservation_allocation_pack'
);

SET @w_agent_billing_pack_fk_exact_count = (
  SELECT COUNT(*)
  FROM (
    SELECT `rc`.`CONSTRAINT_NAME`, `rc`.`UNIQUE_CONSTRAINT_SCHEMA`,
           `rc`.`UNIQUE_CONSTRAINT_NAME`, `rc`.`MATCH_OPTION`,
           `rc`.`UPDATE_RULE`, `rc`.`DELETE_RULE`,
           COUNT(*) AS `key_parts`,
           GROUP_CONCAT(`kcu`.`COLUMN_NAME`
             ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `child_columns`,
           GROUP_CONCAT(`kcu`.`REFERENCED_TABLE_SCHEMA`
             ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_schemas`,
           GROUP_CONCAT(`kcu`.`REFERENCED_TABLE_NAME`
             ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_tables`,
           GROUP_CONCAT(`kcu`.`REFERENCED_COLUMN_NAME`
             ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_columns`,
           GROUP_CONCAT(`kcu`.`POSITION_IN_UNIQUE_CONSTRAINT`
             ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_positions`
    FROM `information_schema`.`REFERENTIAL_CONSTRAINTS` AS `rc`
    INNER JOIN `information_schema`.`KEY_COLUMN_USAGE` AS `kcu`
      ON `kcu`.`CONSTRAINT_SCHEMA` = `rc`.`CONSTRAINT_SCHEMA`
      AND `kcu`.`TABLE_NAME` = `rc`.`TABLE_NAME`
      AND `kcu`.`CONSTRAINT_NAME` = `rc`.`CONSTRAINT_NAME`
    WHERE `rc`.`CONSTRAINT_SCHEMA` = DATABASE()
      AND `rc`.`TABLE_NAME` = 'w_credit_reservation_allocation'
      AND `rc`.`CONSTRAINT_NAME` = 'fk_w_credit_reservation_allocation_pack'
    GROUP BY `rc`.`CONSTRAINT_NAME`, `rc`.`UNIQUE_CONSTRAINT_SCHEMA`,
             `rc`.`UNIQUE_CONSTRAINT_NAME`, `rc`.`MATCH_OPTION`,
             `rc`.`UPDATE_RULE`, `rc`.`DELETE_RULE`
    HAVING `rc`.`UNIQUE_CONSTRAINT_SCHEMA` = DATABASE()
      AND `rc`.`UNIQUE_CONSTRAINT_NAME` = 'PRIMARY'
      AND `rc`.`MATCH_OPTION` = 'NONE'
      AND `rc`.`UPDATE_RULE` = 'RESTRICT'
      AND `rc`.`DELETE_RULE` = 'RESTRICT'
      AND `key_parts` = 1
      AND `child_columns` = 'pack_id'
      AND `parent_schemas` = DATABASE()
      AND `parent_tables` = 'w_credits_pack'
      AND `parent_columns` = 'id'
      AND `parent_positions` = '1'
  ) AS `exact_pack_fk`
);

SET @w_agent_billing_pack_fk_touch_count = (
  SELECT COUNT(DISTINCT `kcu`.`CONSTRAINT_NAME`)
  FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
  INNER JOIN `information_schema`.`KEY_COLUMN_USAGE` AS `kcu`
    ON `kcu`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
    AND `kcu`.`TABLE_NAME` = `tc`.`TABLE_NAME`
    AND `kcu`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
  WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
    AND `tc`.`TABLE_NAME` = 'w_credit_reservation_allocation'
    AND `tc`.`CONSTRAINT_TYPE` = 'FOREIGN KEY'
    AND `kcu`.`COLUMN_NAME` = 'pack_id'
);

SET @w_agent_billing_pack_fk_state = CASE
  WHEN @w_agent_billing_pack_fk_name_count = 0
    AND @w_agent_billing_pack_fk_touch_count = 0
  THEN 'ABSENT'
  WHEN @w_agent_billing_pack_fk_name_count = 1
    AND @w_agent_billing_pack_fk_exact_count = 1
    AND @w_agent_billing_pack_fk_touch_count = 1
  THEN 'EXACT'
  ELSE 'DRIFT'
END;

-- Exact ordinary-index classification. Full columns, order, ascending
-- direction, BTREE type, non-unique semantics, visibility, no prefixes and no
-- functional expressions are all part of the fingerprint. A renamed index
-- with the same full ordered column shape is DRIFT, even if invisible/UNIQUE.
SET @w_agent_billing_order_index_name_rows = (
  SELECT COUNT(*)
  FROM `information_schema`.`STATISTICS`
  WHERE `TABLE_SCHEMA` = DATABASE()
    AND `TABLE_NAME` = 'w_order'
    AND `INDEX_NAME` = 'idx_w_order_membership_resolution'
);

SET @w_agent_billing_order_index_exact_count = (
  SELECT COUNT(*)
  FROM (
    SELECT `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`,
           GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
             ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
           COUNT(*) AS `key_parts`,
           SUM(`SUB_PART` IS NOT NULL) AS `prefix_parts`,
           SUM(`EXPRESSION` IS NOT NULL) AS `expression_parts`,
           SUM(COALESCE(`IS_VISIBLE`, 'NO') <> 'YES') AS `invisible_parts`,
           SUM(COALESCE(`COLLATION`, '') <> 'A') AS `nonascending_parts`
    FROM `information_schema`.`STATISTICS`
    WHERE `TABLE_SCHEMA` = DATABASE()
      AND `TABLE_NAME` = 'w_order'
      AND `INDEX_NAME` = 'idx_w_order_membership_resolution'
    GROUP BY `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`
    HAVING `NON_UNIQUE` = 1
      AND UPPER(`INDEX_TYPE`) = 'BTREE'
      AND `ordered_columns` = 'uid,order_type,status,pay_time,id'
      AND `key_parts` = 5
      AND `prefix_parts` = 0
      AND `expression_parts` = 0
      AND `invisible_parts` = 0
      AND `nonascending_parts` = 0
  ) AS `exact_membership_resolution_index`
);

SET @w_agent_billing_order_index_shape_count = (
  SELECT COUNT(*)
  FROM (
    SELECT `INDEX_NAME`,
           GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
             ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
           COUNT(*) AS `key_parts`,
           SUM(`SUB_PART` IS NOT NULL) AS `prefix_parts`,
           SUM(`EXPRESSION` IS NOT NULL) AS `expression_parts`
    FROM `information_schema`.`STATISTICS`
    WHERE `TABLE_SCHEMA` = DATABASE()
      AND `TABLE_NAME` = 'w_order'
    GROUP BY `INDEX_NAME`
    HAVING `ordered_columns` = 'uid,order_type,status,pay_time,id'
      AND `key_parts` = 5
      AND `prefix_parts` = 0
      AND `expression_parts` = 0
  ) AS `membership_resolution_index_shape`
);

SET @w_agent_billing_order_index_state = CASE
  WHEN @w_agent_billing_order_index_name_rows = 0
    AND @w_agent_billing_order_index_shape_count = 0
  THEN 'ABSENT'
  WHEN @w_agent_billing_order_index_name_rows = 5
    AND @w_agent_billing_order_index_exact_count = 1
    AND @w_agent_billing_order_index_shape_count = 1
  THEN 'EXACT'
  ELSE 'DRIFT'
END;

-- All three classifications finish before the first persistent DDL. Therefore
-- a DRIFT in a later target cannot strand an earlier newly-added object.
CREATE TEMPORARY TABLE `_w_agent_billing_owner_target_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_agent_billing_owner_target_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_agent_billing_owner_target_guard` (`guard_key`)
SELECT CASE
  WHEN @w_agent_billing_budget_state IN ('ABSENT', 'EXACT')
    AND @w_agent_billing_pack_fk_state IN ('ABSENT', 'EXACT')
    AND @w_agent_billing_order_index_state IN ('ABSENT', 'EXACT')
    AND (
      @w_agent_billing_order_index_state = 'EXACT'
      OR (
        SELECT COUNT(DISTINCT `INDEX_NAME`)
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_order'
          AND `INDEX_NAME` <> 'PRIMARY'
      ) <= 63
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_agent_billing_owner_target_guard`;

-- Conditional DDL 1/3. EXACT is a no-op during forward-resume.
SET @w_agent_billing_project_ddl = IF(
  @w_agent_billing_budget_state = 'ABSENT',
  'ALTER TABLE `w_global_project`
     ADD CONSTRAINT `chk_w_global_project_budget_credits`
     CHECK ((`budget_credits_cap` IS NULL OR `budget_credits_cap` >= 0)
            AND `budget_credits_used` >= 0) ENFORCED',
  'SELECT 1'
);
PREPARE `w_agent_billing_project_stmt` FROM @w_agent_billing_project_ddl;
EXECUTE `w_agent_billing_project_stmt`;
DEALLOCATE PREPARE `w_agent_billing_project_stmt`;

CREATE TEMPORARY TABLE `_w_agent_billing_project_post_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;
INSERT INTO `_w_agent_billing_project_post_guard` (`guard_key`) VALUES (0);
INSERT INTO `_w_agent_billing_project_post_guard` (`guard_key`)
SELECT CASE
  WHEN (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
      INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
        ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
        AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
      WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
        AND `tc`.`TABLE_NAME` = 'w_global_project'
        AND `tc`.`CONSTRAINT_NAME` = 'chk_w_global_project_budget_credits'
        AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
        AND `tc`.`ENFORCED` = 'YES'
        AND LOWER(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(
              `cc`.`CHECK_CLAUSE`, '`', ''), ' ', ''), CHAR(9), ''), CHAR(10), ''), CHAR(13), ''))
          IN (
            @w_agent_billing_budget_clause,
            CONCAT('(', @w_agent_billing_budget_clause, ')'),
            @w_agent_billing_budget_clause_mysql,
            CONCAT('(', @w_agent_billing_budget_clause_mysql, ')')
          )
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
      INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
        ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
        AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
      WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
        AND `tc`.`TABLE_NAME` = 'w_global_project'
        AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
        AND LOWER(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(
              `cc`.`CHECK_CLAUSE`, '`', ''), ' ', ''), CHAR(9), ''), CHAR(10), ''), CHAR(13), ''))
          IN (
            @w_agent_billing_budget_clause,
            CONCAT('(', @w_agent_billing_budget_clause, ')'),
            @w_agent_billing_budget_clause_mysql,
            CONCAT('(', @w_agent_billing_budget_clause_mysql, ')')
          )
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
      INNER JOIN `information_schema`.`CHECK_CONSTRAINTS` AS `cc`
        ON `cc`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
        AND `cc`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
      WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
        AND `tc`.`TABLE_NAME` = 'w_global_project'
        AND `tc`.`CONSTRAINT_TYPE` = 'CHECK'
        AND (
          LOCATE('budget_credits_cap', LOWER(REPLACE(`cc`.`CHECK_CLAUSE`, '`', ''))) > 0
          OR LOCATE('budget_credits_used', LOWER(REPLACE(`cc`.`CHECK_CLAUSE`, '`', ''))) > 0
        )
    ) = 1
  THEN 1
  ELSE 0
END;
DROP TEMPORARY TABLE `_w_agent_billing_project_post_guard`;

-- Conditional DDL 2/3. RESTRICT is explicit in both directions.
SET @w_agent_billing_pack_fk_ddl = IF(
  @w_agent_billing_pack_fk_state = 'ABSENT',
  'ALTER TABLE `w_credit_reservation_allocation`
     ADD CONSTRAINT `fk_w_credit_reservation_allocation_pack`
     FOREIGN KEY (`pack_id`) REFERENCES `w_credits_pack` (`id`)
     ON DELETE RESTRICT ON UPDATE RESTRICT',
  'SELECT 1'
);
PREPARE `w_agent_billing_pack_fk_stmt` FROM @w_agent_billing_pack_fk_ddl;
EXECUTE `w_agent_billing_pack_fk_stmt`;
DEALLOCATE PREPARE `w_agent_billing_pack_fk_stmt`;

CREATE TEMPORARY TABLE `_w_agent_billing_pack_fk_post_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;
INSERT INTO `_w_agent_billing_pack_fk_post_guard` (`guard_key`) VALUES (0);
INSERT INTO `_w_agent_billing_pack_fk_post_guard` (`guard_key`)
SELECT CASE
  WHEN (
      SELECT COUNT(*)
      FROM (
        SELECT `rc`.`CONSTRAINT_NAME`, `rc`.`UNIQUE_CONSTRAINT_SCHEMA`,
               `rc`.`UNIQUE_CONSTRAINT_NAME`, `rc`.`MATCH_OPTION`,
               `rc`.`UPDATE_RULE`, `rc`.`DELETE_RULE`,
               COUNT(*) AS `key_parts`,
               GROUP_CONCAT(`kcu`.`COLUMN_NAME`
                 ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `child_columns`,
               GROUP_CONCAT(`kcu`.`REFERENCED_TABLE_SCHEMA`
                 ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_schemas`,
               GROUP_CONCAT(`kcu`.`REFERENCED_TABLE_NAME`
                 ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_tables`,
               GROUP_CONCAT(`kcu`.`REFERENCED_COLUMN_NAME`
                 ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_columns`,
               GROUP_CONCAT(`kcu`.`POSITION_IN_UNIQUE_CONSTRAINT`
                 ORDER BY `kcu`.`ORDINAL_POSITION` SEPARATOR ',') AS `parent_positions`
        FROM `information_schema`.`REFERENTIAL_CONSTRAINTS` AS `rc`
        INNER JOIN `information_schema`.`KEY_COLUMN_USAGE` AS `kcu`
          ON `kcu`.`CONSTRAINT_SCHEMA` = `rc`.`CONSTRAINT_SCHEMA`
          AND `kcu`.`TABLE_NAME` = `rc`.`TABLE_NAME`
          AND `kcu`.`CONSTRAINT_NAME` = `rc`.`CONSTRAINT_NAME`
        WHERE `rc`.`CONSTRAINT_SCHEMA` = DATABASE()
          AND `rc`.`TABLE_NAME` = 'w_credit_reservation_allocation'
          AND `rc`.`CONSTRAINT_NAME` = 'fk_w_credit_reservation_allocation_pack'
        GROUP BY `rc`.`CONSTRAINT_NAME`, `rc`.`UNIQUE_CONSTRAINT_SCHEMA`,
                 `rc`.`UNIQUE_CONSTRAINT_NAME`, `rc`.`MATCH_OPTION`,
                 `rc`.`UPDATE_RULE`, `rc`.`DELETE_RULE`
        HAVING `rc`.`UNIQUE_CONSTRAINT_SCHEMA` = DATABASE()
          AND `rc`.`UNIQUE_CONSTRAINT_NAME` = 'PRIMARY'
          AND `rc`.`MATCH_OPTION` = 'NONE'
          AND `rc`.`UPDATE_RULE` = 'RESTRICT'
          AND `rc`.`DELETE_RULE` = 'RESTRICT'
          AND `key_parts` = 1
          AND `child_columns` = 'pack_id'
          AND `parent_schemas` = DATABASE()
          AND `parent_tables` = 'w_credits_pack'
          AND `parent_columns` = 'id'
          AND `parent_positions` = '1'
      ) AS `exact_pack_fk_post`
    ) = 1
    AND (
      SELECT COUNT(DISTINCT `kcu`.`CONSTRAINT_NAME`)
      FROM `information_schema`.`TABLE_CONSTRAINTS` AS `tc`
      INNER JOIN `information_schema`.`KEY_COLUMN_USAGE` AS `kcu`
        ON `kcu`.`CONSTRAINT_SCHEMA` = `tc`.`CONSTRAINT_SCHEMA`
        AND `kcu`.`TABLE_NAME` = `tc`.`TABLE_NAME`
        AND `kcu`.`CONSTRAINT_NAME` = `tc`.`CONSTRAINT_NAME`
      WHERE `tc`.`CONSTRAINT_SCHEMA` = DATABASE()
        AND `tc`.`TABLE_NAME` = 'w_credit_reservation_allocation'
        AND `tc`.`CONSTRAINT_TYPE` = 'FOREIGN KEY'
        AND `kcu`.`COLUMN_NAME` = 'pack_id'
    ) = 1
  THEN 1
  ELSE 0
END;
DROP TEMPORARY TABLE `_w_agent_billing_pack_fk_post_guard`;

-- Conditional DDL 3/3. VISIBLE is explicit; no prefix or expression is used.
SET @w_agent_billing_order_index_ddl = IF(
  @w_agent_billing_order_index_state = 'ABSENT',
  'ALTER TABLE `w_order`
     ADD INDEX `idx_w_order_membership_resolution`
       (`uid`, `order_type`, `status`, `pay_time`, `id`) VISIBLE',
  'SELECT 1'
);
PREPARE `w_agent_billing_order_index_stmt` FROM @w_agent_billing_order_index_ddl;
EXECUTE `w_agent_billing_order_index_stmt`;
DEALLOCATE PREPARE `w_agent_billing_order_index_stmt`;

CREATE TEMPORARY TABLE `_w_agent_billing_order_index_post_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;
INSERT INTO `_w_agent_billing_order_index_post_guard` (`guard_key`) VALUES (0);
INSERT INTO `_w_agent_billing_order_index_post_guard` (`guard_key`)
SELECT CASE
  WHEN (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               COUNT(*) AS `key_parts`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_parts`,
               SUM(`EXPRESSION` IS NOT NULL) AS `expression_parts`,
               SUM(COALESCE(`IS_VISIBLE`, 'NO') <> 'YES') AS `invisible_parts`,
               SUM(COALESCE(`COLLATION`, '') <> 'A') AS `nonascending_parts`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_order'
          AND `INDEX_NAME` = 'idx_w_order_membership_resolution'
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`, `INDEX_TYPE`
        HAVING `NON_UNIQUE` = 1
          AND UPPER(`INDEX_TYPE`) = 'BTREE'
          AND `ordered_columns` = 'uid,order_type,status,pay_time,id'
          AND `key_parts` = 5
          AND `prefix_parts` = 0
          AND `expression_parts` = 0
          AND `invisible_parts` = 0
          AND `nonascending_parts` = 0
      ) AS `exact_membership_resolution_index_post`
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               COUNT(*) AS `key_parts`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_parts`,
               SUM(`EXPRESSION` IS NOT NULL) AS `expression_parts`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_order'
        GROUP BY `INDEX_NAME`
        HAVING `ordered_columns` = 'uid,order_type,status,pay_time,id'
          AND `key_parts` = 5
          AND `prefix_parts` = 0
          AND `expression_parts` = 0
      ) AS `membership_resolution_index_shape_post`
    ) = 1
  THEN 1
  ELSE 0
END;
DROP TEMPORARY TABLE `_w_agent_billing_order_index_post_guard`;

-- A failed statement deliberately leaves the advisory lock owned by the failed
-- session; the operator must close that session before classifying/retrying.
-- Reaching this line proves all three exact post-guards completed.
SET @w_agent_billing_owner_lock_released =
  RELEASE_LOCK(@w_agent_billing_owner_lock_name);

CREATE TEMPORARY TABLE `_w_agent_billing_owner_release_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;
INSERT INTO `_w_agent_billing_owner_release_guard` (`guard_key`) VALUES (0);
INSERT INTO `_w_agent_billing_owner_release_guard` (`guard_key`)
SELECT CASE WHEN @w_agent_billing_owner_lock_released = 1 THEN 1 ELSE 0 END;
DROP TEMPORARY TABLE `_w_agent_billing_owner_release_guard`;
