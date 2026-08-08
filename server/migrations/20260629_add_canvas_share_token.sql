-- C-11 — share-link revocation via token rotation.
--
-- w_global_project.share_token gates the public share-link surface
-- (GET /api/tools/canvas/public/projects/uuid/:uuid?t=...). NULL is
-- the legacy state: existing share URLs continue working without a
-- token. Rotation (POST /api/tools/canvas/projects/:id/share/rotate)
-- assigns a cryptographically random 64-char hex token; subsequent
-- public reads must pass it via the ?t= query param.
--
-- Idempotent — the column-existence guard means a re-run on a
-- partially migrated db is a no-op.

SET @schema_name := DATABASE();

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_global_project'
        AND COLUMN_NAME = 'share_token'
    ),
    'SELECT 1',
    'ALTER TABLE `w_global_project`
       ADD COLUMN `share_token` VARCHAR(64) DEFAULT NULL
         COMMENT ''C-11: public share-link token. NULL = legacy URL (no token required); set = ?t=<token> required on public reads.'''
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
