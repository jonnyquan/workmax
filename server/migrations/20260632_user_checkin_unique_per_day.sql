-- Adds a unique index on w_user_checkin (uid, checkin_date) so the
-- database itself enforces one-checkin-per-day. Closes a TOCTOU race
-- in CheckinService.DoCheckin where two concurrent requests could
-- both pass the "already checked in?" SELECT and then both succeed
-- on INSERT, double-granting the daily credit reward.
--
-- The original schema had non-unique indexes on `uid` and on
-- `checkin_date` separately; dropping both is unnecessary — leave
-- the per-column indexes alone (they're still useful for per-user
-- history scans and date-range queries) and add a composite UNIQUE
-- on top.

SET @schema_name := DATABASE();

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_checkin'
        AND INDEX_NAME = 'idx_user_checkin_uid_date'
    ),
    'SELECT 1',
    'ALTER TABLE `w_user_checkin` ADD UNIQUE INDEX `idx_user_checkin_uid_date` (`uid`, `checkin_date`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
