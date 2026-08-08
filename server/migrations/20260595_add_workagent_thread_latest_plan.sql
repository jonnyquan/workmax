-- WorkAgent ChatThread: per-thread latest TodoWrite snapshot.
--
-- Adds w_workagent_thread.latest_plan so the Progress section in the
-- right-pane ContextPanel can rehydrate the agent's task plan even
-- when the message carrying the TodoWrite block was paginated out of
-- the first page. Today the frontend walks the loaded messages and
-- extracts the latest TodoWrite via findLatestTodoInMessages — that
-- works for short threads but fails on threads >50 messages where the
-- plan was set in an earlier turn. Surfacing the snapshot directly on
-- the thread row removes the message-pagination dependency.
--
-- Stored as the raw JSON value the SDK emits in TodoWrite.input.todos
-- (array of {id, content, status, priority}). The frontend already
-- has a tolerant parser (utils/todoExtractor.parseTodoInput) that
-- handles array / JSON-string / numbered-list shapes — keeping the
-- value verbatim means we don't have to mirror that parsing logic in
-- Go and survives schema changes on the SDK side.
--
-- Updated by saveAgentConversation when persisting a turn that
-- emitted a TodoWrite. Older threads stay NULL until the next turn
-- that fires a TodoWrite — they keep working via the existing
-- message-walk fallback, so this is a pure additive optimization
-- with no migration backfill required.

ALTER TABLE `w_workagent_thread`
  ADD COLUMN `latest_plan` JSON NULL
    COMMENT '最新一次 TodoWrite 工具的 input.todos 快照 (JSON)。读取时优先此列，缺失时退回扫描消息历史';
