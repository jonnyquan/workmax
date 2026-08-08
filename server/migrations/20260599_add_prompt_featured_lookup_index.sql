-- Speeds up /api/prompts/featured?lang=...&medium=... lookups used by
-- generator example chips. The query filters by status/is_featured/lang/medium
-- and orders by sort/popular_score before a small LIMIT.
ALTER TABLE `w_prompt`
  ADD INDEX `idx_prompt_featured_lookup` (`status`, `is_featured`, `lang`, `medium`, `sort`, `popular_score`);
