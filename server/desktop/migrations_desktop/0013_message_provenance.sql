-- 0013: which engine produced a cached answer, and which model it was told
-- to use.
--
-- Without these columns the provenance line under an answer lives exactly as
-- long as the streamed nodes it was attached to: the moment the finished turn
-- is reconciled against the cache — or the app is reopened — the transcript is
-- rebuilt from these rows and the line is gone. That is worse than not having
-- it, because a reader who learns to look for it finds it missing precisely
-- when they most want it, which is later.
--
-- Nullable with an empty default rather than NOT NULL: every row written
-- before this migration genuinely has no answer to give, and "" is how the
-- rest of this stack already spells "the engine chose its own default". The
-- renderer says nothing when both are empty, so an old row is silent rather
-- than mislabelled.
--
-- Bounded in Go and on the wire rather than here. SQLite would not enforce a
-- length anyway, and the two places that matter — the bridge that puts this on
-- an SSE frame and the shim that reads it back — already own that job.

ALTER TABLE w_workagent_message ADD COLUMN agent_engine TEXT NOT NULL DEFAULT '';
ALTER TABLE w_workagent_message ADD COLUMN agent_model TEXT NOT NULL DEFAULT '';
