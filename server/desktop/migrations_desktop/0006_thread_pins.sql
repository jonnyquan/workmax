-- Thread pins: a local view preference, deliberately NOT a column on
-- w_workagent_thread. That table is upserted by cloud sync, and a preference
-- stored inside a sync-owned row is a preference waiting to be overwritten.
-- A separate table keyed by (uid, thread_uuid) survives every sync pass and
-- scopes naturally per identity — each local account pins its own view.

CREATE TABLE IF NOT EXISTS w_desktop_thread_pin (
    uid         INTEGER NOT NULL,
    thread_uuid TEXT    NOT NULL,
    pinned_at   TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (uid, thread_uuid)
);
