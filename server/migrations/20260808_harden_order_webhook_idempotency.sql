-- Harden w_order webhook idempotency without rewriting legacy order rows.
--
-- Rollout safety:
--   1. pause webhook/order writers before running the guards and ALTER;
--   2. resolve every reported non-empty invoice collision explicitly;
--   3. apply the generated key and unique index before restarting writers.
--
-- The ALTER also rechecks uniqueness, so a writer racing the guards makes the
-- migration fail closed. There is intentionally no UPDATE, backfill or deletion
-- in this migration. If the ALTER fails, no persistent schema change
-- from this migration has committed. Keep the generated key and unique index
-- in place on rollback; dropping them would reopen duplicate webhook admission.

-- Normalize exactly as the generated key will. Multiple NULL, empty and
-- space-only legacy invoice values are compatible because they all become
-- NULL, and MySQL UNIQUE indexes permit multiple NULL values. Any duplicate
-- non-empty normalized invoice must be reconciled before persistent DDL.
CREATE TEMPORARY TABLE `_w_order_invoice_duplicate_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_order_invoice_duplicate_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_order_invoice_duplicate_guard` (`incompatible_rows`)
SELECT COUNT(*)
FROM (
  SELECT `invoice_idempotency_key`
  FROM (
    SELECT
      CONVERT(NULLIF(TRIM(`invoice`), '') USING utf8mb4) COLLATE utf8mb4_bin
        AS `invoice_idempotency_key`
    FROM `w_order`
  ) AS `normalized_invoice`
  WHERE `invoice_idempotency_key` IS NOT NULL
  GROUP BY `invoice_idempotency_key`
  HAVING COUNT(*) > 1
) AS `duplicate_nonempty_invoice`;

DROP TEMPORARY TABLE `_w_order_invoice_duplicate_guard`;

-- The order-number generator contract is at most 32 ASCII bytes. Refuse the
-- migration when the physical `no` column is missing, is not character data,
-- or cannot hold all 32 characters/bytes. This guard intentionally changes no
-- persistent schema; widening, if required, must be a separately reviewed DDL.
CREATE TEMPORARY TABLE `_w_order_no_capacity_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_order_no_capacity_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_order_no_capacity_guard` (`incompatible_rows`)
SELECT CASE
  WHEN COUNT(*) = 1
    AND MIN(
      `DATA_TYPE` IN ('char', 'varchar')
      AND `CHARACTER_MAXIMUM_LENGTH` >= 32
      AND `CHARACTER_OCTET_LENGTH` >= 32
    ) = 1
  THEN 0
  ELSE 1
END
FROM `information_schema`.`COLUMNS`
WHERE `TABLE_SCHEMA` = DATABASE()
  AND `TABLE_NAME` = 'w_order'
  AND `COLUMN_NAME` = 'no';

DROP TEMPORARY TABLE `_w_order_no_capacity_guard`;

-- Generated-column and unique-index semantics are supported only on the
-- reviewed InnoDB, non-partitioned shape. Also require the provider invoice
-- source to be a bounded character column that fits the generated varchar(64)
-- without truncation or implicit binary reinterpretation.
CREATE TEMPORARY TABLE `_w_order_schema_compatibility_guard` (
  `incompatible_rows` bigint unsigned NOT NULL,
  CONSTRAINT `chk_w_order_schema_compatibility_guard`
    CHECK (`incompatible_rows` = 0)
) ENGINE=InnoDB;

INSERT INTO `_w_order_schema_compatibility_guard` (`incompatible_rows`)
SELECT
  (SELECT CASE
     WHEN COUNT(*) = 1
       AND MIN(UPPER(`ENGINE`) = 'INNODB') = 1
       AND MIN(LOWER(COALESCE(`CREATE_OPTIONS`, '')) NOT LIKE '%partitioned%') = 1
     THEN 0 ELSE 1 END
   FROM `information_schema`.`TABLES`
   WHERE `TABLE_SCHEMA` = DATABASE()
     AND `TABLE_NAME` = 'w_order')
  +
  (SELECT CASE
     WHEN COUNT(*) = 1
       AND MIN(`DATA_TYPE` IN ('char', 'varchar')) = 1
       AND MIN(`CHARACTER_MAXIMUM_LENGTH` BETWEEN 1 AND 64) = 1
       AND MIN(`CHARACTER_SET_NAME` IS NOT NULL) = 1
     THEN 0 ELSE 1 END
   FROM `information_schema`.`COLUMNS`
   WHERE `TABLE_SCHEMA` = DATABASE()
     AND `TABLE_NAME` = 'w_order'
     AND `COLUMN_NAME` = 'invoice');

DROP TEMPORARY TABLE `_w_order_schema_compatibility_guard`;

-- The binary collation preserves case-sensitive provider identities. TRIM
-- removes accidental surrounding spaces, while NULLIF keeps all blank legacy
-- values outside the uniqueness domain.
ALTER TABLE `w_order`
  ADD COLUMN `invoice_idempotency_key` varchar(64)
    CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
    GENERATED ALWAYS AS (NULLIF(TRIM(`invoice`), '')) STORED
    COMMENT 'Trimmed non-empty webhook invoice identity; NULL for legacy blanks',
  ADD UNIQUE KEY `uk_w_order_invoice_idempotency` (`invoice_idempotency_key`);
