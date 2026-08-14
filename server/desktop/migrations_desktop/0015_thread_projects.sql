-- 0015: thread projects — a local view preference for grouping conversations.
--
-- Exactly the same reasoning as 0006_thread_pins: a preference stored inside
-- w_workagent_thread is one cloud sync away from being overwritten. A separate
-- table keyed by (uid, thread_uuid) survives every sync pass and scopes per
-- identity. Each local account groups its own view.
--
-- Projects are implicit (tag-style, not a separate entity). The project list
-- is derived from the distinct project_key values in use — no w_desktop_project
-- table is needed for the minimum viable feature.

CREATE TABLE IF NOT EXISTS w_desktop_thread_project (
    uid          INTEGER NOT NULL,
    thread_uuid  TEXT    NOT NULL,
    project_key  TEXT    NOT NULL DEFAULT '',
    assigned_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (uid, thread_uuid)
);

CREATE INDEX IF NOT EXISTS idx_thread_project_uid_key
    ON w_desktop_thread_project(uid, project_key);
