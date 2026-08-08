-- Desktop idempotent thread creation uses a caller-stable UUID as both the
-- resource identifier and the retry key. That contract is safe only when the
-- cloud table enforces one row per UUID under concurrent PUT requests.
--
-- Historical installations normally received this index from the original
-- GORM `unique` model tag before the table was renamed to
-- w_workagent_thread, but the SQL migration ledger did not independently pin
-- it. Add the invariant only when no equivalent single-column unique index
-- already exists, regardless of that index's historical name.
--
-- If legacy duplicate UUID rows exist, ALTER TABLE fails closed. Operators
-- must audit and resolve those rows; this migration intentionally does not
-- guess ownership or delete user data.

SET @db_name := DATABASE();

SET @has_thread_uuid_unique := (
  SELECT COUNT(*)
  FROM (
    SELECT `INDEX_NAME`
    FROM information_schema.STATISTICS
    WHERE `TABLE_SCHEMA` = @db_name
      AND `TABLE_NAME` = 'w_workagent_thread'
      AND `NON_UNIQUE` = 0
    GROUP BY `INDEX_NAME`
    HAVING COUNT(*) = 1
      AND SUM(CASE WHEN `COLUMN_NAME` = 'uuid' THEN 1 ELSE 0 END) = 1
  ) AS `thread_uuid_unique_indexes`
);

SET @ddl := IF(
  @has_thread_uuid_unique = 0,
  'ALTER TABLE `w_workagent_thread` ADD UNIQUE KEY `uk_workagent_thread_uuid` (`uuid`)',
  'SELECT 1'
);

PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
