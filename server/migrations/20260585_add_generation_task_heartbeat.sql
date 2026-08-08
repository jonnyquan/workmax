-- SD-S1.3: heartbeat tracking for in-flight generation tasks.
--
-- Pre-S1.3 a worker that crashed mid-run left its w_generation_task
-- row stuck at status=processing forever. Frontend poll loops never
-- terminated; the user had no signal the work was abandoned. The
-- one-export-per-user submit guard would also block all future exports
-- for that account.
--
-- Fix: workers stamp `heartbeat_at` every 15s while running. A
-- background sweeper finds rows in (pending, processing) whose
-- heartbeat is older than the dead-worker threshold (5 min) and
-- transitions them to status=failed with a system error message.
-- Once failed, the existing "submit again" frontend flow lets the
-- user retry without DB intervention.
--
-- Indexed alongside status so the sweeper's
--   WHERE status IN (0,1) AND heartbeat_at < NOW() - 5min
-- query can use the index without scanning every task ever.

ALTER TABLE `w_generation_task`
  ADD COLUMN `heartbeat_at` DATETIME DEFAULT NULL
    COMMENT 'last heartbeat from the worker; sweeper uses (status, heartbeat_at) to find dead-worker rows',
  ADD INDEX `idx_status_heartbeat` (`status`, `heartbeat_at`);
