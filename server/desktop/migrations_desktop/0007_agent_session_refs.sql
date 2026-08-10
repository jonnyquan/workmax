-- 0007: agent runtime session continuity handles.
--
-- One row per (thread, runtime): the opaque ref the runtime reported after
-- its last successful turn on that thread. Claude stores the SDK session id
-- (resumed via --resume); the Pi runtime will store its session file path.
-- A separate table rather than a column on w_workagent_thread: that table
-- mirrors the cloud MySQL schema and sync must keep being able to compare
-- them shape-for-shape.
--
-- session_ref is advisory. A missing or stale row costs one turn of
-- continuity (the engine falls back to flattening thread history into the
-- prompt), never correctness.
CREATE TABLE IF NOT EXISTS w_desktop_agent_session (
    thread_uuid TEXT NOT NULL,
    runtime     TEXT NOT NULL,
    session_ref TEXT NOT NULL,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (thread_uuid, runtime)
);
