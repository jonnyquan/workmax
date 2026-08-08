-- Freeze checkout ownership and provider billing-period facts on w_order.
--
-- Runtime contract: MySQL 8.0.19 or newer (not MariaDB). The guards use
-- duplicate-primary-key sentinels, rather than relying only on CHECK
-- enforcement, so an incompatible precondition aborts even under an
-- unexpectedly permissive sql_mode.
--
-- Rollout safety:
--   1. stop every w_order writer, including checkout admission, Stripe
--      webhooks, membership renewal, refund, scheduler and repair processes;
--   2. drain their in-flight transactions before starting this file;
--   3. execute every statement on the same physical MySQL session because all
--      fail-closed guards are TEMPORARY-table and session-variable scoped;
--   4. configure the client to stop on the first error. A duplicate-key error
--      from a guard INSERT is the intended abort signal and must not be ignored;
--   5. restart writers only after the final fingerprint has been inspected.
--
-- Migration ordering: 20260808_harden_order_webhook_idempotency.sql must have
-- completed first. Its exact invoice generated-key/UNIQUE baseline is checked
-- below because current renewal code selects that column directly.
--
-- There is deliberately no UPDATE, DELETE or business-data backfill. Existing
-- orders receive only the declared additive defaults: blank provider/session
-- identities and NULL billing bounds. Provider identities and billing periods
-- must never be inferred from mutable application configuration. If a drifted
-- schema already contains non-empty checkout sessions, the read-only guards
-- below require a provider price and a valid complete billing pair, but the
-- target-fingerprint guard still refuses to adopt that unreviewed schema.
-- Legacy unpaid Stripe orders are also rejected: an old provider Checkout
-- Session may still be open or already paid, while its immutable price/session
-- snapshot is unknowable locally. Reconcile it with Stripe and terminalize the
-- local row in a separately audited operation before rerunning this migration.
--
-- Partial-DDL fingerprint and recovery:
--   The sole persistent statement is one MySQL 8 atomic ALTER TABLE. It adds
--   all five columns and the UNIQUE key together, or publishes none of them.
--   The exact successful fingerprint is:
--
--     provider_price_id                    varchar(64)  NOT NULL DEFAULT ''
--     billing_period_start                 datetime(6)  NULL
--     billing_period_end                   datetime(6)  NULL
--     checkout_session_id                  varchar(255) NOT NULL DEFAULT ''
--     checkout_session_idempotency_key     varchar(255), nullable STORED
--       generated as NULLIF(TRIM(checkout_session_id), ''), utf8mb4_bin
--     uk_w_order_checkout_session_identity UNIQUE
--       (checkout_session_idempotency_key), full column, no prefix
--     chk_w_order_billing_period_pair       both NULL, or start < end
--     chk_w_order_checkout_provider_price   non-empty session => price
--
--   If the client reports an ambiguous failure, keep writers stopped and query
--   information_schema.COLUMNS and information_schema.STATISTICS for that exact
--   fingerprint. Zero target definitions means investigate the original error
--   and rerun from the first guard. The complete exact fingerprint means the
--   ALTER committed. Any partial, renamed-equivalent, prefix-index, collation,
--   default, precision or generated-expression difference is schema drift and
--   requires a separately reviewed recovery; never retry the ALTER blindly.

-- MySQL version guard. A sentinel value of 0 already exists. Compatibility
-- inserts 1; incompatibility tries to insert 0 again and fails closed.
CREATE TEMPORARY TABLE `_w_order_checkout_version_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_version_guard` (`guard_key`) VALUES (0);

SET @w_order_checkout_version_core =
  REGEXP_SUBSTR(VERSION(), '^[0-9]+[.][0-9]+[.][0-9]+');
SET @w_order_checkout_version_major =
  CAST(SUBSTRING_INDEX(@w_order_checkout_version_core, '.', 1) AS UNSIGNED);
SET @w_order_checkout_version_minor =
  CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(@w_order_checkout_version_core, '.', 2), '.', -1) AS UNSIGNED);
SET @w_order_checkout_version_patch =
  CAST(SUBSTRING_INDEX(@w_order_checkout_version_core, '.', -1) AS UNSIGNED);

INSERT INTO `_w_order_checkout_version_guard` (`guard_key`)
SELECT CASE
  WHEN LOCATE('mariadb', LOWER(VERSION())) = 0
    AND @w_order_checkout_version_core IS NOT NULL
    AND (
      @w_order_checkout_version_major > 8
      OR (
        @w_order_checkout_version_major = 8
        AND (
          @w_order_checkout_version_minor > 0
          OR (
            @w_order_checkout_version_minor = 0
            AND @w_order_checkout_version_patch >= 19
          )
        )
      )
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_order_checkout_version_guard`;

-- Require one physical, non-partitioned InnoDB base table. Generated columns,
-- UNIQUE admission and SELECT ... FOR UPDATE ownership all rely on this shape.
-- A full utf8mb4 varchar(255) index needs 1020 bytes; DYNAMIC/COMPRESSED with an
-- InnoDB page of at least 8 KiB has at least the required 1536-byte key budget.
CREATE TEMPORARY TABLE `_w_order_checkout_table_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_table_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_order_checkout_table_guard` (`guard_key`)
SELECT CASE
  WHEN (
    SELECT COUNT(*)
    FROM `information_schema`.`TABLES`
    WHERE `TABLE_SCHEMA` = DATABASE()
      AND `TABLE_NAME` = 'w_order'
      AND `TABLE_TYPE` = 'BASE TABLE'
      AND UPPER(`ENGINE`) = 'INNODB'
      AND UPPER(`ROW_FORMAT`) IN ('DYNAMIC', 'COMPRESSED')
  ) = 1
  AND @@innodb_page_size >= 8192
  AND NOT EXISTS (
    SELECT 1
    FROM `information_schema`.`PARTITIONS`
    WHERE `TABLE_SCHEMA` = DATABASE()
      AND `TABLE_NAME` = 'w_order'
      AND `PARTITION_NAME` IS NOT NULL
  )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_order_checkout_table_guard`;

-- The order-number generator writes exactly 32 ASCII bytes and w_order.no is
-- the financial owner. Require the physical capacity, no NULL/blank legacy
-- owner, and exactly one full-column UNIQUE fingerprint. Do not hide a narrow
-- or ambiguous baseline inside this unrelated additive ALTER.
CREATE TEMPORARY TABLE `_w_order_checkout_no_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_no_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_order_checkout_no_guard` (`guard_key`)
SELECT CASE
  WHEN
    (SELECT COUNT(*)
     FROM `information_schema`.`COLUMNS`
     WHERE `TABLE_SCHEMA` = DATABASE()
       AND `TABLE_NAME` = 'w_order'
       AND `COLUMN_NAME` = 'no'
       AND `DATA_TYPE` IN ('char', 'varchar')
       AND `CHARACTER_MAXIMUM_LENGTH` >= 32
       AND `CHARACTER_OCTET_LENGTH` >= 32) = 1
    AND
    (SELECT COUNT(*)
     FROM `w_order`
     WHERE `no` IS NULL OR NULLIF(TRIM(`no`), '') IS NULL) = 0
    AND
    (SELECT COUNT(*)
     FROM (
       SELECT `INDEX_NAME`, `NON_UNIQUE`,
              GROUP_CONCAT(`COLUMN_NAME` ORDER BY `SEQ_IN_INDEX` SEPARATOR ',')
                AS `ordered_columns`,
              SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
       FROM `information_schema`.`STATISTICS`
       WHERE `TABLE_SCHEMA` = DATABASE()
         AND `TABLE_NAME` = 'w_order'
       GROUP BY `INDEX_NAME`, `NON_UNIQUE`
       HAVING `NON_UNIQUE` = 0
          AND `ordered_columns` = 'no'
          AND `prefix_columns` = 0
     ) AS `w_order_no_unique_fingerprint`) = 1
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_order_checkout_no_guard`;

-- Require the exact migration-20260808 invoice admission baseline. Besides
-- enforcing rollout order, this prevents code that directly selects the
-- generated key from reaching an unknown-column or case-insensitive schema.
CREATE TEMPORARY TABLE `_w_order_checkout_invoice_baseline_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_invoice_baseline_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_order_checkout_invoice_baseline_guard` (`guard_key`)
SELECT CASE
  WHEN
    (SELECT COUNT(*)
     FROM `information_schema`.`COLUMNS`
     WHERE `TABLE_SCHEMA` = DATABASE()
       AND `TABLE_NAME` = 'w_order'
       AND `COLUMN_NAME` = 'invoice_idempotency_key'
       AND `DATA_TYPE` = 'varchar'
       AND `CHARACTER_MAXIMUM_LENGTH` = 64
       AND `IS_NULLABLE` = 'YES'
       AND `CHARACTER_SET_NAME` = 'utf8mb4'
       AND `COLLATION_NAME` = 'utf8mb4_bin'
       AND UPPER(`EXTRA`) LIKE '%STORED GENERATED%'
       AND LOWER(
         REPLACE(
           REPLACE(
             REPLACE(`GENERATION_EXPRESSION`, '`', ''),
             ' ',
             ''
           ),
           '_utf8mb4',
           ''
         )
       ) = 'nullif(trim(invoice),'''')') = 1
    AND
    (SELECT COUNT(*)
     FROM (
       SELECT `INDEX_NAME`, `NON_UNIQUE`,
              GROUP_CONCAT(`COLUMN_NAME` ORDER BY `SEQ_IN_INDEX` SEPARATOR ',')
                AS `ordered_columns`,
              SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
       FROM `information_schema`.`STATISTICS`
       WHERE `TABLE_SCHEMA` = DATABASE()
         AND `TABLE_NAME` = 'w_order'
         AND `INDEX_NAME` = 'uk_w_order_invoice_idempotency'
       GROUP BY `INDEX_NAME`, `NON_UNIQUE`
       HAVING `NON_UNIQUE` = 0
          AND `ordered_columns` = 'invoice_idempotency_key'
          AND `prefix_columns` = 0
     ) AS `w_order_invoice_unique_fingerprint`) = 1
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_order_checkout_invoice_baseline_guard`;

-- Never turn an abandoned or provider-ambiguous legacy unpaid Stripe order
-- into the owner of a new Checkout Session merely because additive defaults
-- are blank. Operator reconciliation must prove its provider outcome first.
CREATE TEMPORARY TABLE `_w_order_checkout_legacy_unpaid_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_legacy_unpaid_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_order_checkout_legacy_unpaid_guard` (`guard_key`)
SELECT CASE WHEN COUNT(*) = 0 THEN 1 ELSE 0 END
FROM `w_order`
WHERE UPPER(TRIM(`status`)) = 'UNPAID'
  AND LOWER(TRIM(`pay_method`)) = 'stripe';

DROP TEMPORARY TABLE `_w_order_checkout_legacy_unpaid_guard`;

-- A clean pre-migration schema has no checkout_session_id column, so there is
-- no historical value to scan. If schema drift introduced that source column,
-- inspect its data using exactly the final binary, trim-normalized identity
-- semantics before the fingerprint guard rejects the drifted schema.
CREATE TEMPORARY TABLE `_w_order_checkout_duplicate_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_duplicate_guard` (`guard_key`) VALUES (0);

SET @w_order_checkout_has_session_source = (
  SELECT COUNT(*) = 1
  FROM `information_schema`.`COLUMNS`
  WHERE `TABLE_SCHEMA` = DATABASE()
    AND `TABLE_NAME` = 'w_order'
    AND `COLUMN_NAME` = 'checkout_session_id'
    AND `DATA_TYPE` IN ('char', 'varchar')
);
SET @w_order_checkout_duplicate_sql = IF(
  @w_order_checkout_has_session_source,
  'INSERT INTO `_w_order_checkout_duplicate_guard` (`guard_key`)
   SELECT CASE WHEN COUNT(*) = 0 THEN 1 ELSE 0 END
   FROM (
     SELECT `checkout_session_idempotency_key`
     FROM (
       SELECT CONVERT(NULLIF(TRIM(`checkout_session_id`), '''') USING utf8mb4)
                COLLATE utf8mb4_bin AS `checkout_session_idempotency_key`
       FROM `w_order`
     ) AS `normalized_checkout_session`
     WHERE `checkout_session_idempotency_key` IS NOT NULL
     GROUP BY `checkout_session_idempotency_key`
     HAVING COUNT(*) > 1
   ) AS `duplicate_nonempty_checkout_session`',
  'INSERT INTO `_w_order_checkout_duplicate_guard` (`guard_key`) VALUES (1)'
);
PREPARE `w_order_checkout_duplicate_stmt`
  FROM @w_order_checkout_duplicate_sql;
EXECUTE `w_order_checkout_duplicate_stmt`;
DEALLOCATE PREPARE `w_order_checkout_duplicate_stmt`;

DROP TEMPORARY TABLE `_w_order_checkout_duplicate_guard`;

-- A provider billing period is an indivisible half-open pair [start,end).
-- If drift introduced both target columns, reject a missing mate or start>=end.
CREATE TEMPORARY TABLE `_w_order_checkout_billing_pair_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_billing_pair_guard` (`guard_key`) VALUES (0);

SET @w_order_checkout_has_billing_pair = (
  SELECT COUNT(*) = 2
  FROM `information_schema`.`COLUMNS`
  WHERE `TABLE_SCHEMA` = DATABASE()
    AND `TABLE_NAME` = 'w_order'
    AND `COLUMN_NAME` IN ('billing_period_start', 'billing_period_end')
    AND `DATA_TYPE` IN ('datetime', 'timestamp')
);
SET @w_order_checkout_billing_pair_sql = IF(
  @w_order_checkout_has_billing_pair,
  'INSERT INTO `_w_order_checkout_billing_pair_guard` (`guard_key`)
   SELECT CASE WHEN COUNT(*) = 0 THEN 1 ELSE 0 END
   FROM `w_order`
   WHERE (`billing_period_start` IS NULL) <> (`billing_period_end` IS NULL)
      OR (`billing_period_start` IS NOT NULL
          AND `billing_period_end` IS NOT NULL
          AND `billing_period_start` >= `billing_period_end`)',
  'INSERT INTO `_w_order_checkout_billing_pair_guard` (`guard_key`) VALUES (1)'
);
PREPARE `w_order_checkout_billing_pair_stmt`
  FROM @w_order_checkout_billing_pair_sql;
EXECUTE `w_order_checkout_billing_pair_stmt`;
DEALLOCATE PREPARE `w_order_checkout_billing_pair_stmt`;

DROP TEMPORARY TABLE `_w_order_checkout_billing_pair_guard`;

-- A persisted non-empty checkout session without its immutable provider price
-- cannot be replay-validated. Refuse that history instead of guessing a price.
CREATE TEMPORARY TABLE `_w_order_checkout_provider_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_provider_guard` (`guard_key`) VALUES (0);

SET @w_order_checkout_has_provider_pair = (
  SELECT COUNT(*) = 2
  FROM `information_schema`.`COLUMNS`
  WHERE `TABLE_SCHEMA` = DATABASE()
    AND `TABLE_NAME` = 'w_order'
    AND `COLUMN_NAME` IN ('checkout_session_id', 'provider_price_id')
    AND `DATA_TYPE` IN ('char', 'varchar')
);
SET @w_order_checkout_provider_sql = IF(
  @w_order_checkout_has_provider_pair,
  'INSERT INTO `_w_order_checkout_provider_guard` (`guard_key`)
   SELECT CASE WHEN COUNT(*) = 0 THEN 1 ELSE 0 END
   FROM `w_order`
   WHERE NULLIF(TRIM(`checkout_session_id`), '''') IS NOT NULL
     AND NULLIF(TRIM(`provider_price_id`), '''') IS NULL',
  'INSERT INTO `_w_order_checkout_provider_guard` (`guard_key`) VALUES (1)'
);
PREPARE `w_order_checkout_provider_stmt`
  FROM @w_order_checkout_provider_sql;
EXECUTE `w_order_checkout_provider_stmt`;
DEALLOCATE PREPARE `w_order_checkout_provider_stmt`;

DROP TEMPORARY TABLE `_w_order_checkout_provider_guard`;

-- This migration owns the complete target fingerprint. Refuse already-applied,
-- partially-applied or reserved-name definitions before persistent DDL. The
-- unique-index check includes renamed full-column equivalents when the target
-- generated column already exists.
CREATE TEMPORARY TABLE `_w_order_checkout_fingerprint_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_order_checkout_fingerprint_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_order_checkout_fingerprint_guard` (`guard_key`)
SELECT CASE
  WHEN
    (SELECT COUNT(*)
     FROM `information_schema`.`COLUMNS`
     WHERE `TABLE_SCHEMA` = DATABASE()
       AND `TABLE_NAME` = 'w_order'
       AND `COLUMN_NAME` IN (
         'provider_price_id',
         'billing_period_start',
         'billing_period_end',
         'checkout_session_id',
         'checkout_session_idempotency_key'
       )) = 0
    AND
    (SELECT COUNT(*)
     FROM `information_schema`.`STATISTICS`
     WHERE `TABLE_SCHEMA` = DATABASE()
       AND `TABLE_NAME` = 'w_order'
       AND `INDEX_NAME` = 'uk_w_order_checkout_session_identity') = 0
    AND
    (SELECT COUNT(*)
     FROM `information_schema`.`TABLE_CONSTRAINTS`
     WHERE `CONSTRAINT_SCHEMA` = DATABASE()
       AND `TABLE_NAME` = 'w_order'
       AND `CONSTRAINT_NAME` IN (
         'chk_w_order_billing_period_pair',
         'chk_w_order_checkout_provider_price'
       )) = 0
    AND
    (SELECT COUNT(*)
     FROM (
       SELECT `INDEX_NAME`, `NON_UNIQUE`,
              GROUP_CONCAT(`COLUMN_NAME` ORDER BY `SEQ_IN_INDEX` SEPARATOR ',')
                AS `ordered_columns`,
              SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
       FROM `information_schema`.`STATISTICS`
       WHERE `TABLE_SCHEMA` = DATABASE()
         AND `TABLE_NAME` = 'w_order'
       GROUP BY `INDEX_NAME`, `NON_UNIQUE`
       HAVING `NON_UNIQUE` = 0
          AND `ordered_columns` = 'checkout_session_idempotency_key'
          AND `prefix_columns` = 0
     ) AS `equivalent_checkout_session_identity`) = 0
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_order_checkout_fingerprint_guard`;

-- One persistent atomic dictionary operation. utf8mb4_bin makes provider
-- identifiers case-sensitive. The generated key remains nullable, so any
-- number of legacy/default blank session values stay outside UNIQUE admission.
ALTER TABLE `w_order`
  ADD COLUMN `provider_price_id` varchar(64)
    CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
    NOT NULL DEFAULT ''
    COMMENT 'Immutable payment-provider price identity',
  ADD COLUMN `billing_period_start` datetime(6) NULL DEFAULT NULL
    COMMENT 'Immutable provider billing-period start, inclusive',
  ADD COLUMN `billing_period_end` datetime(6) NULL DEFAULT NULL
    COMMENT 'Immutable provider billing-period end, exclusive',
  ADD COLUMN `checkout_session_id` varchar(255)
    CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
    NOT NULL DEFAULT ''
    COMMENT 'Durable payment-provider checkout session identity',
  ADD COLUMN `checkout_session_idempotency_key` varchar(255)
    CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
    GENERATED ALWAYS AS (NULLIF(TRIM(`checkout_session_id`), '')) STORED
    COMMENT 'Trimmed non-empty checkout identity; NULL for legacy blanks',
  ADD UNIQUE KEY `uk_w_order_checkout_session_identity`
    (`checkout_session_idempotency_key`),
  ADD CONSTRAINT `chk_w_order_billing_period_pair`
    CHECK (
      (`billing_period_start` IS NULL AND `billing_period_end` IS NULL)
      OR
      (`billing_period_start` IS NOT NULL
       AND `billing_period_end` IS NOT NULL
       AND `billing_period_start` < `billing_period_end`)
    ),
  ADD CONSTRAINT `chk_w_order_checkout_provider_price`
    CHECK (
      NULLIF(TRIM(`checkout_session_id`), '') IS NULL
      OR NULLIF(TRIM(`provider_price_id`), '') IS NOT NULL
    );
