-- Adds w_generation_record.parent_record_id — the upstream record
-- reference for end-frame continuation chains in the video generator.
-- NULL for organic (non-continuation) generations, which is the vast
-- majority. Set on submissions that came through the "continue from
-- end frame" flow so a clip's lineage is queryable server-side and
-- syncs across devices, replacing the localStorage-only fallback
-- shipped earlier.
--
-- The index supports both query directions:
--   ↑ "what produced this clip?"
--      SELECT * FROM w_generation_record WHERE id = <child>.parent_record_id
--   ↓ "what continuations did this clip spawn?"
--      SELECT * FROM w_generation_record WHERE parent_record_id = <id>
--
-- No FK constraint by design — soft-deletes (deleted_at) on the parent
-- shouldn't cascade-null the children's lineage, and the validation
-- that the parent belongs to the same uid lives in the API layer
-- where it can produce a 4xx instead of a DB error.

ALTER TABLE `w_generation_record`
    ADD COLUMN `parent_record_id` bigint unsigned DEFAULT NULL COMMENT '续写源记录ID（end-frame 续写链的父节点；NULL=非续写）' AFTER `origin`,
    ADD INDEX `idx_parent_record_id` (`parent_record_id`);
