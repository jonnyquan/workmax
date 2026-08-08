-- Add the index used by local message history chronological reads.
--
-- 0001 originally indexed (thread_id, id DESC), which is useful for
-- newest-row lookups but not for the renderer's oldest-first chat
-- transcript. Existing Desktop caches that already applied 0001 need
-- this as a separate migration; fresh caches also keep the matching
-- index in 0001.

CREATE INDEX IF NOT EXISTS idx_w_workagent_message_thread_created
    ON w_workagent_message(thread_id, created_at, id);
