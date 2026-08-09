-- Local accounts: named identities for the signed-out local route.
--
-- Every local-history query already scopes by uid; accounts give those uids
-- names and let one machine hold separated local workspaces. uid is derived
-- as (1<<62) + (id-1): the FIRST account lands exactly on the reserved
-- single-user uid, so everything created before accounts existed belongs to
-- it with no data migration. AUTOINCREMENT keeps deleted ids from being
-- reused, so a uid can never be re-issued to a different person.
--
-- No password column, deliberately: the database is plaintext on disk, so a
-- password here would be security theater. Accounts are identity and
-- separation; protection is an encryption decision for another migration.

CREATE TABLE IF NOT EXISTS w_desktop_local_account (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    is_active    INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_desktop_local_account_name
    ON w_desktop_local_account(name);
