-- 0011: the mind (心智体) gets a row of its own.
--
-- A mind is a long-lived persona over this machine's shared knowledge base:
-- a BRAIN (which model it thinks with), a CEREBELLUM (the skills it has
-- mastered), and a MEMORY (the knowledge chunks marked as its own). One
-- identity trains several minds; one mind is a view over the identity's
-- library, not a library of its own — the knowledge store keeps one vec0
-- table partitioned by uid, and a mind's memory is the slice of it whose
-- source_id carries the mind's mark ("mind:<id>:...", see knowledge/indexer.go).
-- That is why this table holds identity and intent only: the knowledge itself
-- stays where retrieval can already reach it.
--
-- Identity-scoped (uid), like every table that owns user data here. The uid
-- is the local-account derivation (0005) or the connected cloud account's
-- subject; minds need no migration of their own because none existed before
-- this table did.
--
-- is_active follows the local-account pattern (exactly one active row per
-- uid, enforced by the store's transaction, not by a partial unique index —
-- the active set is per-uid and SQLite partial indexes over expressions of
-- two columns are a subtlety a reader should not have to verify). The default
-- mind is seeded lazily by the store, not here: uids are created at runtime
-- (a local account's first read, a cloud account's first turn), and a .sql
-- file cannot name them.

CREATE TABLE IF NOT EXISTS w_desktop_mind (
    id             TEXT PRIMARY KEY,
    uid            INTEGER NOT NULL,
    name           TEXT    NOT NULL,
    description    TEXT    NOT NULL DEFAULT '',
    role_hint      TEXT    NOT NULL DEFAULT '',
    model_override TEXT,             -- NULL or empty: inherit the identity's model
    is_active      INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_w_desktop_mind_uid ON w_desktop_mind(uid);
