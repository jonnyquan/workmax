-- P0.2 — initial WorkAgent tables + _local_meta (SQLite version).
--
-- Mirrors the four MySQL w_workagent_* schemas plus the local-only
-- _local_meta table. Platform context lives in
-- ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md.
--
-- Notes
--   - SQLite is single-writer; we do not need FK enforcement for the
--     workagent tables (cloud GORM models reference each other by uid/thread_id
--     without FK constraints either).
--   - DATETIME columns use TEXT (ISO 8601) for cross-cache portability.
--   - JSON columns are TEXT; app layer marshals.
--   - `dedup_key` on w_workagent_thread_file is normally a MySQL GENERATED
--     column. SQLite declares the column + index only; the P2 file consumer
--     must compute it in app code before writing rows.
--   - Default 'synced' for cloud_sync_state matches the pull-sync contract.

CREATE TABLE IF NOT EXISTS w_workagent_thread (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    uid                         INTEGER NOT NULL DEFAULT 0,
    uuid                        TEXT    NOT NULL,
    project_id                  INTEGER          DEFAULT 0,
    agent_session_id            TEXT,
    agent_session_created_at    TEXT,
    name                        TEXT    NOT NULL DEFAULT '',
    agent_mode                  TEXT    NOT NULL DEFAULT 'ppt',
    agent_type                  TEXT    NOT NULL DEFAULT 'general_agent',
    workspace_path              TEXT    NOT NULL DEFAULT '',
    model                       TEXT    NOT NULL DEFAULT '',
    max_tokens                  INTEGER NOT NULL DEFAULT 0,
    context_count               INTEGER NOT NULL DEFAULT 0,
    presence_penalty            REAL    NOT NULL DEFAULT 0,
    frequency_penalty           REAL    NOT NULL DEFAULT 0,
    temperature                 REAL    NOT NULL DEFAULT 0,
    prompt                      TEXT    NOT NULL DEFAULT '',
    message_count               INTEGER NOT NULL DEFAULT 0,
    msg_preview                 TEXT    NOT NULL DEFAULT '',
    file_count                  INTEGER NOT NULL DEFAULT 0,
    is_public                   INTEGER NOT NULL DEFAULT 0,
    latest_plan                 TEXT,
    plan_history                TEXT,
    recipe_id                   TEXT    NOT NULL DEFAULT '',
    cloud_sync_state            TEXT    NOT NULL DEFAULT 'synced',
    cloud_thread_id             TEXT,
    last_synced_at              TEXT,
    created_at                  TEXT    DEFAULT CURRENT_TIMESTAMP,
    updated_at                  TEXT    DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_w_workagent_thread_uuid
    ON w_workagent_thread(uuid);

CREATE INDEX IF NOT EXISTS idx_w_workagent_thread_listing
    ON w_workagent_thread(uid, agent_type, updated_at DESC, id DESC);

-- Partial index: only the rows the sync worker actually has to scan.
CREATE INDEX IF NOT EXISTS idx_w_workagent_thread_sync_worker
    ON w_workagent_thread(cloud_sync_state, last_synced_at)
    WHERE cloud_sync_state IN ('pending', 'failed');


CREATE TABLE IF NOT EXISTS w_workagent_message (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    uid                         INTEGER NOT NULL DEFAULT 0,
    uuid                        TEXT    NOT NULL,
    thread_id                   INTEGER NOT NULL DEFAULT 0,
    user_text                   TEXT,
    ai_text                     TEXT,
    total_prompt                TEXT,
    ip                          TEXT    NOT NULL DEFAULT '',
    task_id                     TEXT    NOT NULL DEFAULT '',
    model                       TEXT    NOT NULL DEFAULT '',
    deduct_integral             INTEGER NOT NULL DEFAULT 0,
    refund_integral             INTEGER NOT NULL DEFAULT 0,
    use_tokens                  INTEGER NOT NULL DEFAULT 0,
    prompt_tokens               INTEGER NOT NULL DEFAULT 0,
    completion_tokens           INTEGER NOT NULL DEFAULT 0,
    context_tokens              INTEGER NOT NULL DEFAULT 0,
    use_images                  TEXT,
    ai_audio                    TEXT,
    user_audio                  TEXT,
    append_deduct_integral      INTEGER NOT NULL DEFAULT 0,
    use_files                   TEXT,
    chat_mode                   TEXT    NOT NULL DEFAULT '',
    content_type                TEXT    NOT NULL DEFAULT '',
    structured_content          TEXT,
    actions                     TEXT,
    metadata                    TEXT,
    message_idempotency_key     TEXT,
    user_rating                 INTEGER NOT NULL DEFAULT 0,
    user_feedback               TEXT,
    stage_tag                   TEXT    NOT NULL DEFAULT '',
    streaming_state             TEXT    NOT NULL DEFAULT 'complete',
    created_at                  TEXT    DEFAULT CURRENT_TIMESTAMP,
    updated_at                  TEXT    DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_w_workagent_message_uuid
    ON w_workagent_message(uuid);

CREATE INDEX IF NOT EXISTS idx_w_workagent_message_thread_rating
    ON w_workagent_message(thread_id, user_rating, id);

CREATE INDEX IF NOT EXISTS idx_w_workagent_message_thread_id_desc
    ON w_workagent_message(thread_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_w_workagent_message_thread_created
    ON w_workagent_message(thread_id, created_at, id);


CREATE TABLE IF NOT EXISTS w_workagent_thread_file (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    uid                         INTEGER NOT NULL DEFAULT 0,
    thread_id                   INTEGER NOT NULL DEFAULT 0,
    message_id                  INTEGER NOT NULL DEFAULT 0,
    file_name                   TEXT    NOT NULL DEFAULT '',
    display_name                TEXT    NOT NULL DEFAULT '',
    file_size                   INTEGER NOT NULL DEFAULT 0,
    file_type                   TEXT    NOT NULL DEFAULT '',
    mime_type                   TEXT    NOT NULL DEFAULT '',
    file_path                   TEXT    NOT NULL DEFAULT '',
    file_source                 TEXT    NOT NULL DEFAULT 'upload',
    description                 TEXT,
    file_hash                   TEXT    NOT NULL DEFAULT '',
    global_asset_id             INTEGER NOT NULL DEFAULT 0,
    last_synced_at              TEXT,
    exists_on_disk              INTEGER NOT NULL DEFAULT 1,
    dedup_key                   TEXT,
    created_at                  TEXT    DEFAULT CURRENT_TIMESTAMP,
    updated_at                  TEXT    DEFAULT CURRENT_TIMESTAMP,
    deleted_at                  TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_w_workagent_thread_file_dedup
    ON w_workagent_thread_file(uid, thread_id, dedup_key);

CREATE INDEX IF NOT EXISTS idx_w_workagent_thread_file_name
    ON w_workagent_thread_file(uid, thread_id, file_name);

CREATE INDEX IF NOT EXISTS idx_w_workagent_thread_file_listing
    ON w_workagent_thread_file(uid, thread_id, created_at DESC, id DESC);


CREATE TABLE IF NOT EXISTS w_generation_task (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id                     TEXT    NOT NULL,
    uid                         INTEGER NOT NULL DEFAULT 0,
    tool_id                     TEXT    NOT NULL DEFAULT '',
    model                       TEXT    NOT NULL DEFAULT '',
    status                      INTEGER NOT NULL DEFAULT 0,
    progress                    INTEGER          DEFAULT 0,
    request_data                TEXT,
    result_data                 TEXT,
    error_msg                   TEXT,
    credits_used                INTEGER NOT NULL DEFAULT 0,
    duration_ms                 INTEGER NOT NULL DEFAULT 0,
    thread_id                   INTEGER NOT NULL DEFAULT 0,
    message_id                  INTEGER NOT NULL DEFAULT 0,
    started_at                  TEXT,
    completed_at                TEXT,
    record_id                   INTEGER NOT NULL DEFAULT 0,
    heartbeat_at                TEXT,
    created_at                  TEXT    DEFAULT CURRENT_TIMESTAMP,
    updated_at                  TEXT    DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_w_generation_task_task_id
    ON w_generation_task(task_id);

CREATE INDEX IF NOT EXISTS idx_w_generation_task_uid_tool_thread
    ON w_generation_task(uid, tool_id, thread_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_w_generation_task_uid_tool_status
    ON w_generation_task(uid, tool_id, status, id DESC);


-- App-level KV store. See sqlite-migration.md §7.1.
--   device_id            — 32-char hex device ID generated on first launch
--   app_first_launch_at  — ISO 8601 timestamp
--   sync_cursor_*        — opaque cloud sync cursors
--   auth_session_tombstone_v1 — fixed non-secret fail-closed session bit
-- Schema versions live in _schema_migrations, managed by the runner.
CREATE TABLE IF NOT EXISTS _local_meta (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT DEFAULT CURRENT_TIMESTAMP
);
