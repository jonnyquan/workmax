-- P0-047 - durable Commerce Provider Event Inbox and Transactional Outbox.
--
-- Runtime contract: Oracle MySQL 8.0.19+ only; MariaDB and older compatible
-- servers are rejected mechanically before persistent DDL. This migration is
-- schema-first: it does not fabricate historical Provider events from Order,
-- Invoice, Checkout Session or Credits Pack rows. Those sources do not retain
-- the signed raw event or its exact Provider identity and cannot be backfilled
-- safely.
--
-- Rollout safety:
--   1. apply 20260807 through 20260810 first;
--   2. stop webhook/order/credits writers and drain in-flight transactions;
--   3. run every statement on one physical MySQL session because guards use
--      TEMPORARY tables and session variables;
--   4. stop on the first error; a duplicate-key guard failure is intentional;
--   5. enable receipt/processing traffic only after both table fingerprints
--      and the application schema preflight have passed.
--
-- The two CREATE TABLE statements are individually atomic, but not atomic as a pair.
-- A clean run starts with neither target table and ends with both. If an
-- ambiguous failure leaves only w_commerce_provider_event, do not rerun the whole file:
-- verify that table against this exact DDL, then execute the
-- separately reviewed w_commerce_outbox CREATE statement. Any other partial,
-- renamed-equivalent or reserved-name fingerprint is schema drift.

-- Mechanical Oracle MySQL 8.0.19+ gate. The incompatible branch inserts the
-- existing sentinel value 0 and therefore aborts independently of CHECK
-- enforcement or sql_mode.
CREATE TEMPORARY TABLE `_w_commerce_version_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_commerce_version_guard` (`guard_key`) VALUES (0);

SET @w_commerce_version_core =
  REGEXP_SUBSTR(VERSION(), '^[0-9]+[.][0-9]+[.][0-9]+');
SET @w_commerce_version_major =
  CAST(SUBSTRING_INDEX(@w_commerce_version_core, '.', 1) AS UNSIGNED);
SET @w_commerce_version_minor =
  CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(@w_commerce_version_core, '.', 2), '.', -1) AS UNSIGNED);
SET @w_commerce_version_patch =
  CAST(SUBSTRING_INDEX(@w_commerce_version_core, '.', -1) AS UNSIGNED);

INSERT INTO `_w_commerce_version_guard` (`guard_key`)
SELECT CASE
  WHEN LOCATE('mariadb', LOWER(VERSION())) = 0
    AND @w_commerce_version_core IS NOT NULL
    AND (
      @w_commerce_version_major > 8
      OR (
        @w_commerce_version_major = 8
        AND (
          @w_commerce_version_minor > 0
          OR (
            @w_commerce_version_minor = 0
            AND @w_commerce_version_patch >= 19
          )
        )
      )
    )
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_commerce_version_guard`;

-- Require one physical, non-partitioned InnoDB table for every financial owner
-- or reservation state table consumed by the processor. Distinctive 20260807
-- columns, CHECKs, ordered indexes and its Allocation FK are verified together
-- with the exact 20260808-20260810 index fingerprints. A same-name index with
-- reordered or additional columns must fail closed before either target table
-- is published.
CREATE TEMPORARY TABLE `_w_commerce_baseline_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_commerce_baseline_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_commerce_baseline_guard` (`guard_key`)
SELECT CASE
  WHEN @@innodb_page_size >= 8192
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLES`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_order', 'w_user', 'w_credits_pack',
          'w_credit_reservation', 'w_credit_reservation_allocation'
        )
        AND `TABLE_TYPE` = 'BASE TABLE'
        AND UPPER(`ENGINE`) = 'INNODB'
    ) = 5
    AND NOT EXISTS (
      SELECT 1
      FROM `information_schema`.`PARTITIONS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` IN (
          'w_order', 'w_user', 'w_credits_pack',
          'w_credit_reservation', 'w_credit_reservation_allocation'
        )
        AND `PARTITION_NAME` IS NOT NULL
    )
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_order'
        AND `COLUMN_NAME` IN (
          'id', 'uid', 'no', 'status', 'invoice_idempotency_key',
          'checkout_session_idempotency_key', 'provider_price_id',
          'billing_period_start', 'billing_period_end'
        )
    ) = 9
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_user'
        AND `COLUMN_NAME` IN (
          'id', 'ban', 'ban_note', 'member', 'member_start_time',
          'member_end_time', 'member_subscription'
        )
    ) = 7
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credit_reservation'
        AND `COLUMN_NAME` IN (
          'request_digest', 'hold_review_id', 'hold_settlement_key',
          'hold_request_digest', 'review_held_at', 'refund_target_status',
          'refund_target_used', 'refund_due', 'refund_attempts',
          'next_refund_at', 'last_refund_error_code', 'state_changed_at',
          'state_version'
        )
    ) = 13
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credit_reservation_allocation'
        AND `COLUMN_NAME` IN ('id', 'reservation_id', 'pack_id', 'credits')
    ) = 4
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`COLUMNS`
      WHERE `TABLE_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credits_pack'
        AND `COLUMN_NAME` IN (
          'id', 'uid', 'source_type', 'source_id', 'credits_total',
          'credits_used', 'expires_at'
        )
    ) = 7
    AND (
      SELECT COUNT(*)
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
      ) AS `w_order_no_unique`
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_order'
          AND `INDEX_NAME` IN (
            'uk_w_order_invoice_idempotency',
            'uk_w_order_checkout_session_identity'
          )
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`
        HAVING `prefix_columns` = 0
          AND (
            (`INDEX_NAME` = 'uk_w_order_invoice_idempotency'
              AND `NON_UNIQUE` = 0
              AND `ordered_columns` = 'invoice_idempotency_key')
            OR
            (`INDEX_NAME` = 'uk_w_order_checkout_session_identity'
              AND `NON_UNIQUE` = 0
              AND `ordered_columns` = 'checkout_session_idempotency_key')
          )
      ) AS `w_order_idempotency_fingerprints`
    ) = 2
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_credits_pack'
          AND `INDEX_NAME` IN (
            'idx_w_credits_pack_uid_id',
            'uk_w_credits_pack_source_identity'
          )
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`
        HAVING `prefix_columns` = 0
          AND (
            (`INDEX_NAME` = 'idx_w_credits_pack_uid_id'
              AND `NON_UNIQUE` = 1
              AND `ordered_columns` = 'uid,id')
            OR
            (`INDEX_NAME` = 'uk_w_credits_pack_source_identity'
              AND `NON_UNIQUE` = 0
              AND `ordered_columns` = 'uid,source_type,source_id')
          )
      ) AS `w_credits_pack_index_fingerprints`
    ) = 2
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_credit_reservation'
          AND `INDEX_NAME` IN (
            'uk_w_credit_reservation_hold_settlement',
            'idx_w_credit_reservation_sweep',
            'idx_w_credit_reservation_refund'
          )
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`
        HAVING `prefix_columns` = 0
          AND (
            (`INDEX_NAME` = 'uk_w_credit_reservation_hold_settlement'
              AND `NON_UNIQUE` = 0
              AND `ordered_columns` = 'hold_settlement_key')
            OR
            (`INDEX_NAME` = 'idx_w_credit_reservation_sweep'
              AND `NON_UNIQUE` = 1
              AND `ordered_columns` = 'status,expires_at,id')
            OR
            (`INDEX_NAME` = 'idx_w_credit_reservation_refund'
              AND `NON_UNIQUE` = 1
              AND `ordered_columns` = 'status,next_refund_at,id')
          )
      ) AS `w_credit_reservation_index_fingerprints`
    ) = 3
    AND (
      SELECT COUNT(*)
      FROM (
        SELECT `INDEX_NAME`, `NON_UNIQUE`,
               GROUP_CONCAT(COALESCE(`COLUMN_NAME`, '<expression>')
                 ORDER BY `SEQ_IN_INDEX` SEPARATOR ',') AS `ordered_columns`,
               SUM(`SUB_PART` IS NOT NULL) AS `prefix_columns`
        FROM `information_schema`.`STATISTICS`
        WHERE `TABLE_SCHEMA` = DATABASE()
          AND `TABLE_NAME` = 'w_credit_reservation_allocation'
          AND `INDEX_NAME` = 'uk_w_credit_reservation_allocation_pair'
        GROUP BY `INDEX_NAME`, `NON_UNIQUE`
        HAVING `NON_UNIQUE` = 0
          AND `ordered_columns` = 'reservation_id,pack_id'
          AND `prefix_columns` = 0
      ) AS `w_credit_reservation_allocation_index_fingerprint`
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`TABLE_CONSTRAINTS`
      WHERE `CONSTRAINT_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credit_reservation'
        AND `CONSTRAINT_TYPE` = 'CHECK'
        AND `CONSTRAINT_NAME` IN (
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
      FROM `information_schema`.`TABLE_CONSTRAINTS`
      WHERE `CONSTRAINT_SCHEMA` = DATABASE()
        AND `TABLE_NAME` = 'w_credit_reservation_allocation'
        AND `CONSTRAINT_TYPE` = 'CHECK'
        AND `CONSTRAINT_NAME` = 'chk_w_credit_reservation_allocation_credits'
    ) = 1
    AND (
      SELECT COUNT(*)
      FROM `information_schema`.`REFERENTIAL_CONSTRAINTS` AS `rc`
      INNER JOIN `information_schema`.`KEY_COLUMN_USAGE` AS `kcu`
        ON `kcu`.`CONSTRAINT_SCHEMA` = `rc`.`CONSTRAINT_SCHEMA`
        AND `kcu`.`TABLE_NAME` = `rc`.`TABLE_NAME`
        AND `kcu`.`CONSTRAINT_NAME` = `rc`.`CONSTRAINT_NAME`
      WHERE `rc`.`CONSTRAINT_SCHEMA` = DATABASE()
        AND `rc`.`TABLE_NAME` = 'w_credit_reservation_allocation'
        AND `rc`.`CONSTRAINT_NAME` = 'fk_w_credit_reservation_allocation_reservation'
        AND `rc`.`UPDATE_RULE` = 'RESTRICT'
        AND `rc`.`DELETE_RULE` = 'RESTRICT'
        AND `kcu`.`COLUMN_NAME` = 'reservation_id'
        AND `kcu`.`REFERENCED_TABLE_SCHEMA` = DATABASE()
        AND `kcu`.`REFERENCED_TABLE_NAME` = 'w_credit_reservation'
        AND `kcu`.`REFERENCED_COLUMN_NAME` = 'id'
        AND `kcu`.`ORDINAL_POSITION` = 1
    ) = 1
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_commerce_baseline_guard`;

-- First-run-only target fingerprint. CREATE TABLE IF NOT EXISTS is
-- intentionally avoided because accepting a partial table would make the
-- application believe its state/fencing constraints exist when they do not.
CREATE TEMPORARY TABLE `_w_commerce_target_guard` (
  `guard_key` tinyint unsigned NOT NULL,
  PRIMARY KEY (`guard_key`)
) ENGINE=InnoDB;

INSERT INTO `_w_commerce_target_guard` (`guard_key`) VALUES (0);

INSERT INTO `_w_commerce_target_guard` (`guard_key`)
SELECT CASE
  WHEN (
    SELECT COUNT(*)
    FROM `information_schema`.`TABLES`
    WHERE `TABLE_SCHEMA` = DATABASE()
      AND `TABLE_NAME` IN (
        'w_commerce_provider_event',
        'w_commerce_outbox'
      )
  ) = 0
  AND (
    SELECT COUNT(*)
    FROM `information_schema`.`REFERENTIAL_CONSTRAINTS`
    WHERE `CONSTRAINT_SCHEMA` = DATABASE()
      AND `CONSTRAINT_NAME` = 'fk_w_commerce_outbox_provider_event'
  ) = 0
  THEN 1
  ELSE 0
END;

DROP TEMPORARY TABLE `_w_commerce_target_guard`;

CREATE TABLE `w_commerce_provider_event` (
  `id`                       bigint unsigned NOT NULL AUTO_INCREMENT,
  `provider`                 varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_account_id`      varchar(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `provider_api_version`     varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  `event_id`                 varchar(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `event_type`               varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `object_id`                varchar(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `live_mode`                tinyint unsigned NOT NULL DEFAULT 0,
  `provider_created_at`      datetime(6) DEFAULT NULL,
  `verification_key_digest`  char(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `payload_digest`           char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `payload_json`             mediumblob NOT NULL
                               COMMENT 'Exact signature-verified UTF-8 JSON bytes; never MySQL JSON-reserialized',
  `status`                   varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'received',
  `attempt_count`            int unsigned NOT NULL DEFAULT 0,
  `processing_version`       bigint unsigned NOT NULL DEFAULT 0
                               COMMENT 'Monotonic processing lease fence',
  `lease_owner_id`           varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  `lease_expires_at`         datetime(6) DEFAULT NULL,
  `next_attempt_at`          datetime(6) DEFAULT NULL,
  `processed_at`             datetime(6) DEFAULT NULL,
  `outcome_kind`             varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  `result_digest`            char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  `last_error_code`          varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  `created_at`               datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at`               datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                               COMMENT 'Written explicitly by inbox transitions; no ON UPDATE clause',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_commerce_provider_event_identity`
    (`provider`, `provider_account_id`, `live_mode`, `event_id`),
  KEY `idx_w_commerce_provider_event_received` (`status`, `id`),
  KEY `idx_w_commerce_provider_event_retry` (`status`, `next_attempt_at`, `id`),
  KEY `idx_w_commerce_provider_event_expired` (`status`, `lease_expires_at`, `id`),
  KEY `idx_w_commerce_provider_event_object`
    (`provider`, `provider_account_id`, `live_mode`, `object_id`, `provider_created_at`, `id`),
  CONSTRAINT `chk_w_commerce_provider_event_identity`
    CHECK (
      OCTET_LENGTH(`provider`) BETWEEN 1 AND 32
      AND BINARY `provider` = BINARY TRIM(`provider`)
      AND `provider` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`provider_account_id`) BETWEEN 1 AND 255
      AND BINARY `provider_account_id` = BINARY TRIM(`provider_account_id`)
      AND `provider_account_id` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`provider_api_version`) BETWEEN 0 AND 32
      AND BINARY `provider_api_version` = BINARY TRIM(`provider_api_version`)
      AND `provider_api_version` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`event_id`) BETWEEN 1 AND 255
      AND BINARY `event_id` = BINARY TRIM(`event_id`)
      AND `event_id` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`event_type`) BETWEEN 1 AND 128
      AND BINARY `event_type` = BINARY TRIM(`event_type`)
      AND `event_type` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`object_id`) BETWEEN 1 AND 255
      AND BINARY `object_id` = BINARY TRIM(`object_id`)
      AND `object_id` NOT REGEXP '[^!-~]'
      AND `live_mode` IN (0, 1)
    ),
  CONSTRAINT `chk_w_commerce_provider_event_digests`
    CHECK (
      OCTET_LENGTH(`verification_key_digest`) = 71
      AND LEFT(`verification_key_digest`, 7) = 'sha256:'
      AND BINARY SUBSTRING(`verification_key_digest`, 8)
        = BINARY LOWER(SUBSTRING(`verification_key_digest`, 8))
      AND SUBSTRING(`verification_key_digest`, 8) NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`payload_digest`) = 64
      AND BINARY `payload_digest` = BINARY LOWER(`payload_digest`)
      AND `payload_digest` NOT REGEXP '[^0-9a-f]'
      AND (
        `result_digest` = ''
        OR (
          OCTET_LENGTH(`result_digest`) = 64
          AND BINARY `result_digest` = BINARY LOWER(`result_digest`)
          AND `result_digest` NOT REGEXP '[^0-9a-f]'
        )
      )
    ),
  CONSTRAINT `chk_w_commerce_provider_event_payload`
    CHECK (
      OCTET_LENGTH(`payload_json`) BETWEEN 1 AND 65536
      AND JSON_VALID(CONVERT(`payload_json` USING utf8mb4))
      AND CONVERT(CONVERT(`payload_json` USING utf8mb4) USING binary)
        = `payload_json`
    ),
  CONSTRAINT `chk_w_commerce_provider_event_counters`
    CHECK (
      `attempt_count` BETWEEN 0 AND 64
      AND `processing_version` BETWEEN 0 AND 9223372036854775807
      AND `processing_version` >= `attempt_count`
    ),
  CONSTRAINT `chk_w_commerce_provider_event_status`
    CHECK (`status` IN (
      'received', 'processing', 'retry_wait',
      'processed', 'ignored', 'manual_review'
    )),
  CONSTRAINT `chk_w_commerce_provider_event_state_tuple`
    CHECK (
      (`status` = 'received'
        AND `attempt_count` = 0 AND `processing_version` = 0
        AND `lease_owner_id` = '' AND `lease_expires_at` IS NULL
        AND `next_attempt_at` IS NULL AND `processed_at` IS NULL
        AND `outcome_kind` = '' AND `result_digest` = ''
        AND `last_error_code` = '')
      OR
      (`status` = 'processing'
        AND `attempt_count` >= 1 AND `processing_version` >= 1
        AND NULLIF(TRIM(`lease_owner_id`), '') IS NOT NULL
        AND `lease_expires_at` IS NOT NULL
        AND `next_attempt_at` IS NULL AND `processed_at` IS NULL
        AND `outcome_kind` = '' AND `result_digest` = ''
        AND `last_error_code` = '')
      OR
      (`status` = 'retry_wait'
        AND `attempt_count` >= 1 AND `processing_version` >= 1
        AND `lease_owner_id` = '' AND `lease_expires_at` IS NULL
        AND `next_attempt_at` IS NOT NULL AND `processed_at` IS NULL
        AND `outcome_kind` = '' AND `result_digest` = ''
        AND NULLIF(TRIM(`last_error_code`), '') IS NOT NULL)
      OR
      (`status` IN ('processed', 'ignored')
        AND `attempt_count` >= 1 AND `processing_version` >= 1
        AND `lease_owner_id` = '' AND `lease_expires_at` IS NULL
        AND `next_attempt_at` IS NULL AND `processed_at` IS NOT NULL
        AND NULLIF(TRIM(`outcome_kind`), '') IS NOT NULL
        AND OCTET_LENGTH(`result_digest`) = 64
        AND `last_error_code` = '')
      OR
      (`status` = 'manual_review'
        AND `attempt_count` >= 1 AND `processing_version` >= 1
        AND `lease_owner_id` = '' AND `lease_expires_at` IS NULL
        AND `next_attempt_at` IS NULL AND `processed_at` IS NULL
        AND `outcome_kind` = '' AND `result_digest` = ''
        AND NULLIF(TRIM(`last_error_code`), '') IS NOT NULL)
    ),
  CONSTRAINT `chk_w_commerce_provider_event_mutable_text`
    CHECK (
      OCTET_LENGTH(`lease_owner_id`) BETWEEN 0 AND 128
      AND BINARY `lease_owner_id` = BINARY TRIM(`lease_owner_id`)
      AND `lease_owner_id` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`outcome_kind`) BETWEEN 0 AND 64
      AND BINARY `outcome_kind` = BINARY TRIM(`outcome_kind`)
      AND `outcome_kind` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`last_error_code`) BETWEEN 0 AND 64
      AND BINARY `last_error_code` = BINARY TRIM(`last_error_code`)
      AND `last_error_code` NOT REGEXP '[^!-~]'
    ),
  CONSTRAINT `chk_w_commerce_provider_event_times`
    CHECK (
      (`lease_expires_at` IS NULL OR `lease_expires_at` > `updated_at`)
      AND (`next_attempt_at` IS NULL OR `next_attempt_at` > `updated_at`)
      AND (`processed_at` IS NULL OR `processed_at` >= `created_at`)
      AND `updated_at` >= `created_at`
    )
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Durable signature-verified Commerce Provider Event Inbox';

CREATE TABLE `w_commerce_outbox` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `provider_event_id`     bigint unsigned NOT NULL,
  `ordinal`               int unsigned NOT NULL
                            COMMENT 'Zero-based stable Effect ordinal within one Provider Event result',
  `topic`                 varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `dedupe_key`            char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
                            COMMENT 'Stable Provider idempotency key; reused verbatim on every retry',
  `payload_digest`        char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `payload_json`          mediumblob NOT NULL
                            COMMENT 'Bounded UTF-8 JSON effect payload; never stores credentials',
  `status`                varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending',
  `available_at`          datetime(6) NOT NULL,
  `delivery_attempts`     bigint unsigned NOT NULL DEFAULT 0,
  `dispatch_version`      bigint unsigned NOT NULL DEFAULT 0
                            COMMENT 'Monotonic dispatcher lease fence independent of event processing',
  `lease_owner_id`        varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  `lease_expires_at`      datetime(6) DEFAULT NULL,
  `delivered_at`          datetime(6) DEFAULT NULL,
  `last_error_code`       varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  `created_at`            datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at`            datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                            COMMENT 'Written explicitly by dispatcher transitions; no ON UPDATE clause',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_w_commerce_outbox_event_ordinal` (`provider_event_id`, `ordinal`),
  UNIQUE KEY `uk_w_commerce_outbox_dedupe` (`topic`, `dedupe_key`),
  KEY `idx_w_commerce_outbox_pending` (`status`, `available_at`, `id`),
  KEY `idx_w_commerce_outbox_expired` (`status`, `lease_expires_at`, `id`),
  CONSTRAINT `chk_w_commerce_outbox_identity`
    CHECK (
      `ordinal` BETWEEN 0 AND 15
      AND OCTET_LENGTH(`topic`) BETWEEN 1 AND 128
      AND BINARY `topic` = BINARY TRIM(`topic`)
      AND `topic` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`dedupe_key`) = 64
      AND BINARY `dedupe_key` = BINARY LOWER(`dedupe_key`)
      AND `dedupe_key` NOT REGEXP '[^0-9a-f]'
      AND OCTET_LENGTH(`payload_digest`) = 64
      AND BINARY `payload_digest` = BINARY LOWER(`payload_digest`)
      AND `payload_digest` NOT REGEXP '[^0-9a-f]'
    ),
  CONSTRAINT `chk_w_commerce_outbox_payload`
    CHECK (
      OCTET_LENGTH(`payload_json`) BETWEEN 1 AND 65536
      AND JSON_VALID(CONVERT(`payload_json` USING utf8mb4))
      AND CONVERT(CONVERT(`payload_json` USING utf8mb4) USING binary)
        = `payload_json`
    ),
  CONSTRAINT `chk_w_commerce_outbox_counters`
    CHECK (
      `delivery_attempts` BETWEEN 0 AND 9223372036854775807
      AND `dispatch_version` BETWEEN 0 AND 9223372036854775807
    ),
  CONSTRAINT `chk_w_commerce_outbox_status`
    CHECK (`status` IN ('pending', 'delivering', 'delivered', 'dead_letter')),
  CONSTRAINT `chk_w_commerce_outbox_state_tuple`
    CHECK (
      (`status` = 'pending'
        AND `lease_owner_id` = '' AND `lease_expires_at` IS NULL
        AND `delivered_at` IS NULL)
      OR
      (`status` = 'delivering'
        AND `delivery_attempts` >= 1 AND `dispatch_version` >= 1
        AND NULLIF(TRIM(`lease_owner_id`), '') IS NOT NULL
        AND `lease_expires_at` IS NOT NULL
        AND `delivered_at` IS NULL)
      OR
      (`status` = 'delivered'
        AND `delivery_attempts` >= 1 AND `dispatch_version` >= 1
        AND `lease_owner_id` = '' AND `lease_expires_at` IS NULL
        AND `delivered_at` IS NOT NULL AND `last_error_code` = '')
      OR
      (`status` = 'dead_letter'
        AND `delivery_attempts` >= 1 AND `dispatch_version` >= 1
        AND `lease_owner_id` = '' AND `lease_expires_at` IS NULL
        AND `delivered_at` IS NULL
        AND NULLIF(TRIM(`last_error_code`), '') IS NOT NULL)
    ),
  CONSTRAINT `chk_w_commerce_outbox_mutable_text`
    CHECK (
      OCTET_LENGTH(`lease_owner_id`) BETWEEN 0 AND 128
      AND BINARY `lease_owner_id` = BINARY TRIM(`lease_owner_id`)
      AND `lease_owner_id` NOT REGEXP '[^!-~]'
      AND OCTET_LENGTH(`last_error_code`) BETWEEN 0 AND 64
      AND BINARY `last_error_code` = BINARY TRIM(`last_error_code`)
      AND `last_error_code` NOT REGEXP '[^!-~]'
    ),
  CONSTRAINT `chk_w_commerce_outbox_times`
    CHECK (
      (`lease_expires_at` IS NULL OR `lease_expires_at` > `updated_at`)
      AND (`delivered_at` IS NULL OR `delivered_at` >= `created_at`)
      AND `updated_at` >= `created_at`
    ),
  CONSTRAINT `fk_w_commerce_outbox_provider_event`
    FOREIGN KEY (`provider_event_id`)
    REFERENCES `w_commerce_provider_event` (`id`)
    ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Transactional Commerce side-effect Outbox';
