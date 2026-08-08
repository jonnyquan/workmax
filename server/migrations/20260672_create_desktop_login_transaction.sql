-- Desktop Login Transaction Phase 1.
--
-- This is a Server-owned, short-lived identity transaction. It does not add a
-- Web client and it does not create a second token/session system: successful
-- transactions issue rows in w_desktop_oauth_authorization_code, then reuse
-- the existing /api/desktop/oauth/token refresh-chain path.

CREATE TABLE IF NOT EXISTS `w_desktop_login_transaction` (
    `id`                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `transaction_id`             VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `version`                    BIGINT UNSIGNED NOT NULL,
    `status`                     VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `secret_hash`                BINARY(32) NOT NULL,
    `client_id`                  VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `device_id`                  VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `redirect_uri`               VARCHAR(500) NOT NULL,
    `oauth_state_digest`         BINARY(32) NOT NULL,
    `oauth_state_ciphertext`     VARCHAR(1536) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `code_challenge`             VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `code_challenge_method`      VARCHAR(10) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `scope`                      VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    `provider_state_digest`      BINARY(32) DEFAULT NULL,
    `provider_pkce_ciphertext`   VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
    `exchange_token_digest`      BINARY(32) DEFAULT NULL,
    `uid`                        INT UNSIGNED DEFAULT NULL,
    `identity_method`            VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
    `failed_attempts`            SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    `last_failed_at`             DATETIME(6) DEFAULT NULL,
    `expires_at`                 DATETIME(6) NOT NULL,
    `authenticated_at`           DATETIME(6) DEFAULT NULL,
    `consumed_at`                DATETIME(6) DEFAULT NULL,
    `created_at`                 DATETIME(6) NOT NULL,
    `updated_at`                 DATETIME(6) NOT NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_w_desktop_login_tx_transaction` (`transaction_id`),
    UNIQUE KEY `uk_w_desktop_login_tx_secret` (`secret_hash`),
    UNIQUE KEY `uk_w_desktop_login_tx_provider_state` (`provider_state_digest`),
    KEY `idx_w_desktop_login_tx_status_expiry` (`status`, `expires_at`),
    KEY `idx_w_desktop_login_tx_uid_created` (`uid`, `created_at`),
    CONSTRAINT `chk_w_desktop_login_tx_status` CHECK (`status` IN (
        'pending', 'password_authenticating', 'google_pending',
        'google_exchanging', 'authenticated', 'exchanged', 'failed', 'expired'
    )),
    CONSTRAINT `chk_w_desktop_login_tx_pkce` CHECK (`code_challenge_method` = 'S256'),
    CONSTRAINT `chk_w_desktop_login_tx_identity` CHECK (
        (`uid` IS NULL AND `identity_method` IS NULL) OR
        (`uid` IS NOT NULL AND `identity_method` IN ('password', 'google'))
    ),
    CONSTRAINT `chk_w_desktop_login_tx_attempts` CHECK (`failed_attempts` <= 5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Short-lived Desktop first-sign-in transaction; secrets are hashed or sealed';

-- New login transactions freeze the device before issuing a code. Legacy
-- authorize/consent codes remain NULL during the compatibility window.
ALTER TABLE `w_desktop_oauth_authorization_code`
    ADD COLUMN `device_id` VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL
        COMMENT 'Frozen Desktop device_id; NULL only for legacy authorize compatibility' AFTER `uid`,
    ADD KEY `idx_w_desktop_oauth_auth_code_device` (`device_id`, `expires_at`);
