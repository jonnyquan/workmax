-- Harden w_credits_pack owner locking and source idempotency.
--
-- Runtime contract: Oracle MySQL 8.0.19+ only. The first fail-closed guard uses
-- a duplicate-primary-key sentinel; it does not depend on CHECK enforcement.
-- It rejects MariaDB and versions older than 8.0.19 mechanically,
-- before later guards rely on enforced CHECK constraints. Do not run this file
-- against MySQL 5.7 or another MySQL-compatible database without review.
--
-- Rollout safety:
--   1. stop all CreditsPack writers (payment callbacks, membership grants,
--      reservation admission/refund, schedulers, admin jobs and repair tools);
--   2. drain in-flight transactions before starting this file;
--   3. execute every statement on the same physical MySQL session because the
--      fail-closed guards are TEMPORARY tables scoped to that connection;
--   4. configure the migration client to stop on the first error. A rejected
--      guard INSERT is the intended abort signal and must never be ignored;
--   5. restart writers only after the final ALTER has succeeded and its index
--      fingerprint has been verified.
--
-- There is intentionally no UPDATE, backfill or DELETE. Reconcile incompatible
-- legacy data with a separately reviewed operation, then rerun all guards.
--
-- Partial-DDL fingerprint and recovery:
--   The only persistent statement below is one ALTER TABLE that adds both indexes
--   and also changes both source columns to binary collation plus the canonical
--   identity CHECK. MySQL 8 atomic DDL therefore publishes the complete
--   definition or none of it. Before a first run, no target name or equivalent
--   full-column index/CHECK may exist. After success, the exact fingerprint is:
--
--     idx_w_credits_pack_uid_id          NON_UNIQUE=1  (uid,id)
--     uk_w_credits_pack_source_identity  NON_UNIQUE=0  (uid,source_type,source_id)
--     source_type/source_id               utf8mb4_bin, exact 50/64 chars
--     chk_w_credits_pack_source_identity_canonical
--                                             nonblank, trim-canonical identity
--
--   If a client reports an ambiguous failure, query information_schema.STATISTICS
--   for those names and ordered columns. Zero definitions means it is safe to
--   investigate and rerun from the first guard while writers remain stopped;
--   the exact two definitions mean the migration committed; any one-index,
--   wrong-order, prefix-index or differently named equivalent fingerprint is
--   schema drift and must be reviewed instead of retried blindly.

-- Mechanical runtime gate. Row 0 is the rejection sentinel: a compatible
-- server inserts 1, while MariaDB, an unparsable version, or MySQL older than
-- 8.0.19 attempts to insert 0 again and stops on a duplicate-primary-key error.
-- MySQL 5.7 also fails closed at REGEXP_SUBSTR, before any persistent DDL.
CREATE TEMPORARY TABLE `_w_credits_pack_version_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_credits_pack_version_guard` (`guard_key`) VALUES (0);

SET @w_credits_pack_version_core =
  REGEXP_SUBSTR(VERSION(), '^[0-9]+[.][0-9]+[.][0-9]+');
SET @w_credits_pack_version_major =
  CAST(SUBSTRING_INDEX(@w_credits_pack_version_core, '.', 1) AS UNSIGNED);
SET @w_credits_pack_version_minor =
  CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(@w_credits_pack_version_core, '.', 2), '.', -1) AS UNSIGNED);
SET @w_credits_pack_version_patch =
  CAST(SUBSTRING_INDEX(@w_credits_pack_version_core, '.', -1) AS UNSIGNED);

INSERT INTO `_w_credits_pack_version_guard` (`guard_key`)
SELECT CASE
  WHEN LOCATE('mariadb', LOWER(VERSION())) = 0
    AND @w_credits_pack_version_core IS NOT NULL
    AND (
      @w_credits_pack_version_major > 8
      OR (
        @w_credits_pack_version_major = 8
        AND (
          @w_credits_pack_version_minor > 0
          OR (
            @w_credits_pack_version_minor = 0
            AND @w_credits_pack_version_patch >= 19
          )
        )
      )
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_credits_pack_version_guard`;

-- The row-lock protocol depends on one physical InnoDB table and the model's
-- critical column contract. Refuse missing/nullable/wrong-width identities,
-- non-integral counters, source strings too narrow for the application, or a
-- source identity whose full maximum byte width cannot fit an InnoDB key.
CREATE TEMPORARY TABLE `_w_credits_pack_schema_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_credits_pack_schema_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_credits_pack_schema_guard` (`incompatible_rows`)
SELECT CASE
  WHEN
    (SELECT COUNT(*)
       FROM `information_schema`.`TABLES`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `TABLE_TYPE` = 'BASE TABLE'
        AND UPPER(`ENGINE`) = 'INNODB') = 1
    AND NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`PARTITIONS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `PARTITION_NAME` IS NOT NULL
    )
    AND
    (SELECT COUNT(*)
       FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `COLUMN_NAME` = 'id'
        AND `DATA_TYPE` = 'bigint'
        AND `IS_NULLABLE` = 'NO'
        AND `COLUMN_KEY` = 'PRI'
        AND LOWER(`EXTRA`) LIKE '%auto_increment%') = 1
    AND
    (SELECT COUNT(*)
       FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `COLUMN_NAME` = 'uid'
        AND `DATA_TYPE` = 'bigint'
        AND `IS_NULLABLE` = 'NO') = 1
    AND
    (SELECT COUNT(*)
       FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `COLUMN_NAME` = 'source_type'
        AND `DATA_TYPE` IN ('char', 'varchar')
        AND `IS_NULLABLE` = 'NO'
        AND `CHARACTER_MAXIMUM_LENGTH` = 50
        AND `CHARACTER_SET_NAME` = 'utf8mb4') = 1
    AND
    (SELECT COUNT(*)
       FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `COLUMN_NAME` = 'source_id'
        AND `DATA_TYPE` IN ('char', 'varchar')
        AND `IS_NULLABLE` = 'NO'
        AND `CHARACTER_MAXIMUM_LENGTH` = 64
        AND `CHARACTER_SET_NAME` = 'utf8mb4') = 1
    AND
    (SELECT COUNT(*)
       FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `COLUMN_NAME` IN ('credits_total', 'credits_used')
        AND `DATA_TYPE` = 'bigint'
        AND `IS_NULLABLE` = 'NO') = 2
    AND
    (SELECT COALESCE(SUM(`CHARACTER_OCTET_LENGTH`), 3073)
       FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `COLUMN_NAME` IN ('source_type', 'source_id')) <= 3064
  THEN 0
  ELSE 1
END;

DROP TEMPORARY TABLE `_w_credits_pack_schema_guard`;

-- UNIQUE permits multiple NULL values, and a CHECK comparison that evaluates
-- UNKNOWN also passes. Keep this data guard explicit even though the physical
-- schema guard above requires all five business columns to be NOT NULL.
CREATE TEMPORARY TABLE `_w_credits_pack_amount_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_credits_pack_amount_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_credits_pack_amount_guard` (`incompatible_rows`)
SELECT COUNT(*)
FROM `w_credits_pack`
WHERE `uid` IS NULL
   OR `source_type` IS NULL
   OR `source_id` IS NULL
   OR `credits_total` IS NULL
   OR `credits_used` IS NULL
   OR `credits_total` < 0
   OR `credits_used` < 0
   OR `credits_used` > `credits_total`
   OR NULLIF(TRIM(`source_type`), '') IS NULL
   OR NULLIF(TRIM(`source_id`), '') IS NULL
   OR BINARY `source_type` <> BINARY TRIM(`source_type`)
   OR BINARY `source_id` <> BINARY TRIM(`source_id`);

DROP TEMPORARY TABLE `_w_credits_pack_amount_guard`;

-- Group with the target binary semantics. The atomic ALTER below changes both
-- source columns to utf8mb4_bin before publishing UNIQUE, matching Go's exact
-- source identity comparisons rather than inheriting a server-default *_ci
-- collation.
CREATE TEMPORARY TABLE `_w_credits_pack_duplicate_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_credits_pack_duplicate_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_credits_pack_duplicate_guard` (`incompatible_rows`)
SELECT COUNT(*)
FROM (
  SELECT `uid`, BINARY `source_type` AS `source_type_binary`, BINARY `source_id` AS `source_id_binary`
  FROM `w_credits_pack`
  GROUP BY `uid`, BINARY `source_type`, BINARY `source_id`
  HAVING COUNT(*) > 1
) AS `duplicate_source_identity`;

DROP TEMPORARY TABLE `_w_credits_pack_duplicate_guard`;

-- Refuse an already-applied, partially applied, renamed-equivalent or otherwise
-- ambiguous index fingerprint. Prefix indexes are not equivalent and are also
-- found when they use either reserved target name.
CREATE TEMPORARY TABLE `_w_credits_pack_index_fingerprint_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_credits_pack_index_fingerprint_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_credits_pack_index_fingerprint_guard` (`incompatible_rows`)
SELECT
  (SELECT COUNT(*)
     FROM `information_schema`.`STATISTICS`
    WHERE `TABLE_SCHEMA` = DATABASE()
      AND `TABLE_NAME` = 'w_credits_pack'
      AND `INDEX_NAME` IN (
        'idx_w_credits_pack_uid_id',
        'uk_w_credits_pack_source_identity'
      ))
  +
	(SELECT COUNT(*)
	   FROM `information_schema`.`TABLE_CONSTRAINTS`
	  WHERE `CONSTRAINT_SCHEMA` = DATABASE()
	    AND `TABLE_NAME` = 'w_credits_pack'
	    AND `CONSTRAINT_NAME` = 'chk_w_credits_pack_source_identity_canonical')
	+
  (SELECT COUNT(*)
     FROM (
       SELECT `INDEX_NAME`, `NON_UNIQUE`,
              GROUP_CONCAT(`COLUMN_NAME` ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
              SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
       FROM `information_schema`.`STATISTICS`
       WHERE `TABLE_SCHEMA` = DATABASE()
         AND `TABLE_NAME` = 'w_credits_pack'
       GROUP BY `INDEX_NAME`, `NON_UNIQUE`
       HAVING (`ordered_columns` = 'uid,id'
                AND `prefix_columns` = 0)
           OR (`NON_UNIQUE` = 0
                AND `ordered_columns` = 'uid,source_type,source_id'
                AND `prefix_columns` = 0)
     ) AS `equivalent_index_fingerprint`);

DROP TEMPORARY TABLE `_w_credits_pack_index_fingerprint_guard`;

-- One persistent, atomic MySQL 8 dictionary operation: the owner index supports
-- deterministic SELECT ... FOR UPDATE ORDER BY id locking, and the source
-- identity UNIQUE key is the final database-level idempotency defense.
ALTER TABLE `w_credits_pack`
  MODIFY COLUMN `source_type` varchar(50)
    CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
    COMMENT 'Credits source type',
  MODIFY COLUMN `source_id` varchar(64)
    CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL
    COMMENT 'Credits source identity',
  ADD KEY `idx_w_credits_pack_uid_id` (`uid`, `id`),
  ADD UNIQUE KEY `uk_w_credits_pack_source_identity` (`uid`, `source_type`, `source_id`),
  ADD CONSTRAINT `chk_w_credits_pack_source_identity_canonical`
    CHECK (
      NULLIF(TRIM(`source_type`), '') IS NOT NULL
      AND NULLIF(TRIM(`source_id`), '') IS NOT NULL
      AND BINARY `source_type` = BINARY TRIM(`source_type`)
      AND BINARY `source_id` = BINARY TRIM(`source_id`)
    );
