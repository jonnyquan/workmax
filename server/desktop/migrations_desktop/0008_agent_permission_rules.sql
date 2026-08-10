-- 0008: durable tool approvals ("always allow").
--
-- One row per (uid, tool): the user answered "always allow" for that tool on
-- an approval card. Read-only tools never ask, so rows here are write-surface
-- tools (Write, Edit today). uid-scoped: local accounts must not inherit each
-- other's grants. Deny rules do not exist yet — denial is per-call.
CREATE TABLE IF NOT EXISTS w_desktop_agent_permission_rule (
    uid        INTEGER NOT NULL,
    tool       TEXT NOT NULL,
    decision   TEXT NOT NULL DEFAULT 'allow',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (uid, tool)
);
