-- P1.A.5b — tombstone table for delete-propagation to desktop sync.
--
-- Cloud-side handlers delete rows from w_workagent_thread /
-- w_workagent_message; without tombstones the desktop client's
-- pull-sync (cloud-sync.md §3) has no way to learn "this row got
-- deleted" — it would only see "this row is no longer in the
-- delta page" which is indistinguishable from "this row didn't
-- update in the cursor window".
--
-- Wire shape: GET /api/desktop/sync/threads + /messages merge
-- tombstone rows into items[] as action="delete" (cloud-sync.md
-- §5.1). Sidecar's UpsertThreads / UpsertMessages handle the
-- delete action (P1.A.5a, b545f12e).
--
-- Retention: 90 days. A future cron job deletes rows where
-- deleted_at < now() - 90d. Desktop clients that haven't synced
-- in 90 days won't see the delete, but their threads/messages
-- cursor would also be 90 days behind, meaning they'd re-sync
-- everything from scratch on next connect anyway.

CREATE TABLE IF NOT EXISTS `w_workagent_tombstone` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT,
  `uid`         int NOT NULL,
  `entity_type` varchar(32) NOT NULL,
  `entity_id`   bigint unsigned NOT NULL,
  -- entity_uuid is the cross-cloud-and-local stable identifier
  -- (same uuid the sidecar's local row indexes on). Carried in the
  -- tombstone so the sync endpoint doesn't have to look up the
  -- now-deleted row.
  `entity_uuid` varchar(64) NOT NULL,
  `deleted_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  -- Sync queries scan by (uid, deleted_at > cursor), filtered by
  -- entity_type. Composite index covers the common case + the
  -- 90-day GC scan.
  KEY `idx_w_workagent_tombstone_uid_deleted_at` (`uid`, `deleted_at`),
  KEY `idx_w_workagent_tombstone_entity` (`entity_type`, `entity_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Delete-propagation table for desktop sync (P1.A.5b).';
