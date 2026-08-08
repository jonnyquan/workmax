-- Local / Official model route preference for OSS Desktop.
-- Non-secret fields only; API keys live in the OS keychain.

CREATE TABLE IF NOT EXISTS w_desktop_model_settings (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    preferred_route      TEXT    NOT NULL DEFAULT 'official',
    local_protocol       TEXT    NOT NULL DEFAULT '',
    local_base_url       TEXT    NOT NULL DEFAULT '',
    local_model_id       TEXT    NOT NULL DEFAULT '',
    local_api_key_present INTEGER NOT NULL DEFAULT 0,
    updated_at           TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO w_desktop_model_settings (
    id, preferred_route, local_protocol, local_base_url, local_model_id, local_api_key_present, updated_at
) VALUES (
    1, 'official', '', '', '', 0, CURRENT_TIMESTAMP
);
