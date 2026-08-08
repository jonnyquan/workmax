-- SD-S0/S1/S2 + T1.A + T3 + T4 — read-only diagnostic for the 10 new
-- drama migrations (20260582 → 20260591). Run this against workmaxdev to
-- see which ones still need to be applied. Output is a single
-- result-set with one row per migration: status="OK" means already
-- applied, "MISSING" means run the file.
--
-- Usage:
--   mysql -h 47.82.152.9 -P 13308 -u workmaxdev -p workmaxdev \
--     < server/scripts/check_drama_migrations.sql

SET @schema := DATABASE();

SELECT '20260582 anchors_version (character)' AS migration,
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_character'
      AND COLUMN_NAME = 'anchors_version'
  ), 'OK', 'MISSING') AS status
UNION ALL
SELECT '20260582 anchors_version (location)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_location'
      AND COLUMN_NAME = 'anchors_version'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260582 anchors_snapshot_json (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'anchors_snapshot_json'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260583 w_drama_episode_activity table',
  IF(EXISTS(
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_episode_activity'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260584 credits_reserved_pending (project)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_project'
      AND COLUMN_NAME = 'credits_reserved_pending'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260585 heartbeat_at (generation_task)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_generation_task'
      AND COLUMN_NAME = 'heartbeat_at'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260585 idx_status_heartbeat (generation_task)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_generation_task'
      AND INDEX_NAME = 'idx_status_heartbeat'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260586 editing_by_uid (episode)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_episode'
      AND COLUMN_NAME = 'editing_by_uid'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260586 editing_expires_at (episode)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_episode'
      AND COLUMN_NAME = 'editing_expires_at'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260587 team_id (template)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_template'
      AND COLUMN_NAME = 'team_id'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260588 w_drama_character_family table',
  IF(EXISTS(
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_character_family'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260588 family_id (character)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_character'
      AND COLUMN_NAME = 'family_id'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260589 kind (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'kind'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260589 image_url (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'image_url'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260589 idx_panel_kind (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND INDEX_NAME = 'idx_panel_kind'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260590 approval (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'approval'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260590 approval_by_uid (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'approval_by_uid'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260590 idx_panel_approval (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND INDEX_NAME = 'idx_panel_approval'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260590 published_urls_json (episode)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_episode'
      AND COLUMN_NAME = 'published_urls_json'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260591 appearance_hash (character)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_character'
      AND COLUMN_NAME = 'appearance_hash'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260591 appearance_hash (location)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_location'
      AND COLUMN_NAME = 'appearance_hash'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260592 script_generation_version (panel_shot)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'script_generation_version'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260593 w_drama_script_revision table',
  IF(EXISTS(
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_script_revision'
  ), 'OK', 'MISSING')
UNION ALL
SELECT '20260594 team_id (character_family)',
  IF(EXISTS(
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @schema
      AND TABLE_NAME = 'w_drama_character_family'
      AND COLUMN_NAME = 'team_id'
  ), 'OK', 'MISSING');
