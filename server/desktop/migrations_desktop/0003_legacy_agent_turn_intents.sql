-- Alpha.6 legacy recovery foundation.
--
-- This table does not make the legacy synchronous Cloud chat transport
-- durable. It only preserves one immutable, idempotent Desktop turn intent so
-- a user can explicitly replay the same request after a renderer/sidecar
-- interruption. Cloud execution remains bound to the legacy HTTP request.

CREATE TABLE IF NOT EXISTS w_desktop_agent_turn_intent (
    uid                 INTEGER NOT NULL,
    turn_uuid           TEXT    NOT NULL PRIMARY KEY,
    thread_id           INTEGER NOT NULL,
    thread_uuid         TEXT    NOT NULL,
    user_text           TEXT    NOT NULL,
    chat_mode           TEXT    NOT NULL,
    request_digest      TEXT    NOT NULL,
    state               TEXT    NOT NULL DEFAULT 'starting'
                               CHECK (state IN ('starting', 'streaming', 'interrupted', 'completed', 'canceled')),
    last_error_kind     TEXT    NOT NULL DEFAULT '',
    created_at          TEXT    NOT NULL,
    updated_at          TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_desktop_agent_turn_intent_recoverable
    ON w_desktop_agent_turn_intent(uid, state, updated_at DESC, turn_uuid);

CREATE INDEX IF NOT EXISTS idx_desktop_agent_turn_intent_thread
    ON w_desktop_agent_turn_intent(uid, thread_uuid, updated_at DESC, turn_uuid);
