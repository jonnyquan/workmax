-- P0-#3 critique loop: explicit user feedback on assistant messages.
--
-- Today the agent has no observation of "did the user like that
-- output". Adding two columns to w_workagent_message gives the
-- product two new affordances:
--
--   1. UI side: 👍/👎 + optional "what should I change" textarea
--      on each assistant message.
--   2. Server side: the next-turn preflight composer reads the
--      most recent thumbs-down + feedback on this thread and
--      injects it as a <previous-critique> block in the system
--      prompt — so the agent literally sees the user's correction
--      instead of having to re-derive it from the next user turn.
--
-- Defaults chosen so existing rows backfill to "unrated" without
-- a separate UPDATE pass. NULL would have worked for user_rating
-- but tinyint NOT NULL + DEFAULT 0 keeps the column index-friendly
-- (every query for "give me messages with feedback" can scan on
-- equality, not IS NULL).

ALTER TABLE `w_workagent_message`
  ADD COLUMN `user_rating` tinyint NOT NULL DEFAULT 0
    COMMENT 'user feedback: -1=thumbs-down 0=unrated 1=thumbs-up',
  ADD COLUMN `user_feedback` text DEFAULT NULL
    COMMENT 'free-text critique paired with a thumbs-down; nullable';

-- idx_thread_rating supports the FindLatestUserCritique query
-- ("most recent thumbs-down with non-empty feedback in this thread").
-- thread_id is high-selectivity; user_rating cuts the scan down to
-- rated rows. ORDER BY id DESC then walks the index in reverse so
-- LIMIT 1 returns the most recent without a sort.
CREATE INDEX `idx_thread_rating_id`
  ON `w_workagent_message` (`thread_id`, `user_rating`, `id`);
