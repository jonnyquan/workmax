-- Per-Shot collaborative lock columns on w_shot.
--
-- Three columns, no separate table:
--
--   lock_user_id      uid currently holding the lock; 0 = unlocked.
--   lock_job_id       opaque caller-supplied id for the operation that
--                     took the lock (e.g. "regen-<elementId>"). Acquire
--                     by the same uid+jobId is idempotent (renews);
--                     release/heartbeat must match the holder's jobId.
--   lock_acquired_at  when the current holder grabbed the lock. Combined
--                     with a server-side TTL (90 s), this gives stale-lock
--                     detection without a cron — the next acquire just
--                     steals if `now - lock_acquired_at > TTL`.
--
-- Unlocked sentinel: lock_user_id = 0 (matches the existing default-zero
-- convention on uid columns in this schema).

ALTER TABLE `w_shot`
    ADD COLUMN `lock_user_id`     int      NOT NULL DEFAULT 0  COMMENT 'uid holding the lock; 0 = unlocked',
    ADD COLUMN `lock_job_id`      varchar(64) NOT NULL DEFAULT '' COMMENT 'caller-supplied operation id; matched on heartbeat/release',
    ADD COLUMN `lock_acquired_at` datetime DEFAULT NULL          COMMENT 'when the current holder grabbed the lock; null when unlocked';
