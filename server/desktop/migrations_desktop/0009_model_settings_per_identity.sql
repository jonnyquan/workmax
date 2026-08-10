-- 0009: model settings stop being a single global row.
--
-- 0004 created w_desktop_model_settings with CHECK (id = 1): one row for the
-- whole machine. That was true when a machine had exactly one user. It is not
-- true now — local accounts (0005) put several identities on one machine, and
-- a connected cloud account is a further identity on top of those — so the
-- route preference of whoever configured it last was silently answering for
-- everyone.
--
-- The split is drawn where the product decision draws it:
--
--   * The local ENDPOINT is a property of the MACHINE. Somebody installed
--     Ollama on this computer; it listens on this port and serves this model
--     whoever is logged in. Making each identity re-enter the same
--     http://127.0.0.1:11434/v1 would be bookkeeping, not privacy. So
--     w_desktop_model_settings keeps exactly those three columns and stays
--     the single id=1 row it always was.
--
--   * The CHOICE is a property of the IDENTITY. Which route I prefer, and
--     which official model I picked, is mine. So is my API key — but that
--     lives in the Keychain, keyed per uid by the sidecar, not here.
--
-- Zero migration, the 0005 trick again: the pre-accounts row belonged to the
-- machine's only user, whose uid is the reserved single-user uid — exactly
-- (1<<62) + (first account id - 1). Handing the existing preference to that
-- uid means the person who configured this machine sees no change at all.
-- COALESCE covers a database whose account table has not been seeded yet
-- (accounts are created lazily on first read): the first account it ever
-- creates will be id 1, so 1<<62 is the right destination either way.

CREATE TABLE IF NOT EXISTS w_desktop_model_preference (
    uid                   INTEGER PRIMARY KEY,
    preferred_route       TEXT    NOT NULL DEFAULT 'official',
    official_model_id     TEXT    NOT NULL DEFAULT '',
    local_api_key_present INTEGER NOT NULL DEFAULT 0,
    updated_at            TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO w_desktop_model_preference (
    uid, preferred_route, official_model_id, local_api_key_present, updated_at
)
SELECT
    (1 << 62) + (COALESCE((SELECT MIN(id) FROM w_desktop_local_account), 1) - 1),
    preferred_route,
    '',
    local_api_key_present,
    updated_at
FROM w_desktop_model_settings
WHERE id = 1;

-- Dropped rather than left behind. A column that still holds the old global
-- answer, that nothing reads and nothing updates, is a trap for the next
-- person who greps for preferred_route and finds two of them.
ALTER TABLE w_desktop_model_settings DROP COLUMN preferred_route;
ALTER TABLE w_desktop_model_settings DROP COLUMN local_api_key_present;
