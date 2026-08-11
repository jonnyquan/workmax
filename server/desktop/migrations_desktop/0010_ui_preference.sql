-- 0010: where a display preference lives.
--
-- The appearance switch (follow the system / light / dark) used to be written
-- to the renderer's localStorage, and it never survived a relaunch — not
-- because the write failed, but because localStorage is scoped to an origin
-- and the UI origin's port is minted fresh on every launch (uiserver.go binds
-- 127.0.0.1:0). Every start was a new origin, so every start read an empty
-- store. The preference was therefore never persisted at all.
--
-- It moves here, next to the model settings, for the same reason those are
-- here: the sidecar owns the only storage on this machine whose identity does
-- not change between launches.
--
-- MACHINE-scoped, not per identity, and that is a product judgement rather
-- than an implementation shortcut. 0009 drew the line at "the endpoint belongs
-- to the machine, the choice belongs to the identity" because a route decides
-- whose quota is spent and whose key is used. Appearance decides none of that:
-- it is about this screen, in this room, at this hour. The two local
-- identities on a machine are one pair of eyes, and a theme that flipped
-- when you switched identity would be a bug report, not a feature. It is also
-- what lets the shell answer "which theme?" while serving index.html, before
-- the page exists to say who is looking at it — which is what makes the first
-- frame correct instead of corrected.
--
-- A table of its own rather than a column on w_desktop_model_settings: that
-- table's name and every column in it say "model", and a theme parked in it
-- would be the kind of thing a reader finds by accident. This one is named for
-- what it holds, and the next machine-level UI preference has an obvious home.

CREATE TABLE IF NOT EXISTS w_desktop_ui_preference (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    appearance TEXT    NOT NULL DEFAULT 'system',
    updated_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO w_desktop_ui_preference (id, appearance, updated_at)
VALUES (1, 'system', CURRENT_TIMESTAMP);
