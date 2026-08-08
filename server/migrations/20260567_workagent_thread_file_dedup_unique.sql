-- WorkAgent thread_file: unique constraint on the dedupe tuple.
--
-- file_service.go:65 has carried a "still TODO" comment for the unique
-- index on (uid, thread_id, file_name, file_path). The direct form
-- doesn't fit: file_path is varchar(1000) and the four-column key would
-- exceed InnoDB's 3072-byte limit on utf8mb4 (4×1000 + 4×500 ≈ 6000 B).
--
-- Approach: add a STORED generated column dedup_key that hashes
-- file_name + '|' + file_path with SHA2-256 (64 hex chars), then a
-- composite unique index on (uid, thread_id, dedup_key) which fits in
-- ~268 bytes. The hash collision space is large enough that two distinct
-- (file_name, file_path) tuples colliding is not a realistic concern.
--
-- AddFile in server/service/tools/workagent/file_service.go is updated
-- in the same change to map MySQL duplicate-key errors back to the
-- existing-record return path, so the unique constraint enforces the
-- application-level dedupe contract instead of just narrowing it.
--
-- Production deployment note: this migration assumes no pre-existing
-- duplicates. If `SELECT uid, thread_id, file_name, file_path,
-- COUNT(*) FROM w_workagent_thread_file WHERE deleted_at IS NULL
-- GROUP BY uid, thread_id, file_name, file_path HAVING COUNT(*) > 1`
-- returns rows on prod, dedupe them first (keep MIN(id), soft-delete
-- the rest) before running this migration. Dev seed data has 1 row,
-- so this isn't an issue here.

ALTER TABLE `w_workagent_thread_file`
  ADD COLUMN `dedup_key` CHAR(64) GENERATED ALWAYS AS
    (SHA2(CONCAT(`file_name`, '|', `file_path`), 256)) STORED
    COMMENT '生成列：file_name+file_path 的 SHA2-256 摘要，用于支持 (uid,thread_id,dedup_key) 唯一索引（file_path varchar(1000) 直接做唯一键超过 InnoDB 3072B 限制）';

ALTER TABLE `w_workagent_thread_file`
  ADD UNIQUE KEY `uk_uid_thread_dedup` (`uid`, `thread_id`, `dedup_key`);
