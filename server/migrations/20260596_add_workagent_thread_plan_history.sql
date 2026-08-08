-- WorkAgent ChatThread: rolling TodoWrite snapshot history.
--
-- Adds w_workagent_thread.plan_history so the Progress section's
-- timeline UI rehydrates the FULL chronological history even when
-- the messages carrying earlier TodoWrites have been paginated out
-- of the first page. latest_plan (added in 20260595) only carries
-- the single most-recent snapshot — enough to seed an active plan,
-- but the UI's "earlier phases" disclosure was empty on long
-- threads (>50 messages) until the user paged backwards.
--
-- Stored as a JSON array of {todos, captured_at} objects, oldest-
-- first, capped at 5 entries via writer-side truncation. Five is
-- the smallest size that gives the timeline visible depth without
-- bloating per-thread row size; the cap is centralized in the
-- writer (saveAgentConversation) so a future tightening / loosening
-- is a single-line change rather than a schema migration.
--
-- saveAgentConversation push-and-truncates on every turn that emits
-- a TodoWrite. Older threads stay NULL until their next TodoWrite —
-- they keep working via the existing message-walk + latest_plan
-- fallback, so this is a pure additive optimization with no
-- migration backfill required.

ALTER TABLE `w_workagent_thread`
  ADD COLUMN `plan_history` JSON NULL
    COMMENT '最近 N=5 条 TodoWrite 快照数组，每条 {todos, captured_at}。供超长会话首屏 rehydrate 完整时间轴；写入由 saveAgentConversation push-and-truncate 维护';
