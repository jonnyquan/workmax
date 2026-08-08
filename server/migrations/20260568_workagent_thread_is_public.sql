-- WorkAgent ChatThread: per-thread share gate.
--
-- Adds w_workagent_thread.is_public so an owner can revoke a share URL
-- without deleting/recreating the thread. Default TRUE preserves
-- backward compatibility with already-shared links — every existing
-- row stays accessible until the owner explicitly toggles it off.
--
-- Public endpoints (GetConversationDetail, GetMessageDetail in
-- server/api/pro/tools/workagent/conversation_share.go) check this
-- column and return a generic "not found" when false, matching the
-- existing pattern for non-existent UUIDs so the response doesn't
-- distinguish "never shared" from "share revoked".
--
-- A future tightening can flip the application-level default in
-- CreateAIChatThread to false (require an explicit "share" action)
-- without another migration.

ALTER TABLE `w_workagent_thread`
  ADD COLUMN `is_public` TINYINT(1) NOT NULL DEFAULT 1
    COMMENT '是否允许通过 UUID 公开分享（owner 可在分享后切回 0 撤销访问，不需要轮换 UUID）';
