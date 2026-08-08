-- M5-B-06 cleanup: rename asset-ledger source label from
-- canvas_version_thumbnail (misnomer after Retire-T5 dropped
-- w_canvas_version) to canvas_snapshot_thumbnail. Idempotent
-- single-statement backfill.
--
-- Writers/readers in code update to use the new label in the same
-- PR; this migration brings existing rows in line so dashboard
-- queries don't lose data after the literal change.

UPDATE `w_user_asset_ledger`
   SET `source` = 'canvas_snapshot_thumbnail'
 WHERE `source` = 'canvas_version_thumbnail';
