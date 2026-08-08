-- P-1.1 — OAuth Authorization Server tables for the desktop client.
--
-- Adds the three tables needed for workmax to act as an OAuth 2.0
-- Authorization Server (alongside the existing role as Google OAuth
-- *client*). All endpoints that consume these land under /api/desktop/oauth/*
-- in P-1.2 through P-1.7.
--
-- Design refs:
--   ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
--   (platform model; device_id remains part of the client binding)
--
-- Migration scope:
--   1. w_desktop_oauth_client                — registered clients (one row, workmax-desktop)
--   2. w_desktop_oauth_authorization_code    — short-lived auth codes (TTL ≤ 10 min)
--   3. w_desktop_oauth_refresh_token         — refresh chain with rotation + replay detection
--
-- Seed:
--   One row in w_desktop_oauth_client identifying workmax-desktop as a
--   public client with loopback redirect pattern allowed.
--
-- Naming: `w_desktop_*` prefix follows workmax's `w_<feature>_*` table
-- convention (e.g. w_workagent_thread). The `desktop_` infix is
-- because these tables today serve only the workmax-desktop client;
-- if a future use case extends OAuth to third-party clients we'd
-- rename / generalize via a follow-up migration.
--
-- These tables are deliberately scoped narrow: only what the desktop
-- client (the only OAuth consumer today) needs. Adding third-party
-- clients later is a row insert plus optional client_secret + the
-- redirect_uris JSON adjustment — no further schema changes.

CREATE TABLE IF NOT EXISTS `w_desktop_oauth_client` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `client_id`       VARCHAR(64)     NOT NULL                              COMMENT '客户端标识，OAuth client_id 参数',
    `client_name`     VARCHAR(255)    NOT NULL                              COMMENT '展示名称（同意页 + 设备管理页用）',
    `client_type`     VARCHAR(20)     NOT NULL                              COMMENT 'public | confidential（desktop 是 public，无 client_secret）',
    `client_secret`   VARCHAR(255)    DEFAULT NULL                          COMMENT '仅 confidential 客户端使用',
    `redirect_uris`   JSON            NOT NULL                              COMMENT '允许的 redirect URI 模式数组（如 ["http://127.0.0.1:*/oauth/callback"]）',
    `allowed_scopes`  JSON            NOT NULL                              COMMENT '允许的 scope 列表（如 ["workagent"]）',
    `is_active`       TINYINT(1)      NOT NULL DEFAULT 1                    COMMENT '0=禁用（拒绝所有请求） 1=启用',
    `created_at`      DATETIME        DEFAULT NULL,
    `updated_at`      DATETIME        DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_w_desktop_oauth_client_client_id` (`client_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OAuth Authorization Server 已注册的客户端';


CREATE TABLE IF NOT EXISTS `w_desktop_oauth_authorization_code` (
    `id`                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `code`                   VARCHAR(64)     NOT NULL                       COMMENT '一次性 code（≥ 32 bytes base64url 随机）',
    `client_id`              VARCHAR(64)     NOT NULL                       COMMENT '发起 authorize 请求的 client',
    `uid`                    INT             NOT NULL                       COMMENT '已登录用户 id（workmax user）',
    `redirect_uri`           VARCHAR(500)    NOT NULL                       COMMENT '与 code 绑定的具体 redirect URI（不是模式）',
    `code_challenge`         VARCHAR(128)    NOT NULL                       COMMENT 'PKCE base64url(SHA256(verifier))',
    `code_challenge_method`  VARCHAR(10)     NOT NULL                       COMMENT '固定 S256；plain 不接受',
    `scope`                  VARCHAR(255)    NOT NULL                       COMMENT '授予的 scope（同意页确认后写入）',
    `used`                   TINYINT(1)      NOT NULL DEFAULT 0             COMMENT '0=未消费 1=已消费（消费后永不再发）',
    `expires_at`             DATETIME        NOT NULL                       COMMENT '签发后 10 分钟过期',
    `created_at`             DATETIME        DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_w_desktop_oauth_auth_code_code` (`code`),
    KEY `idx_w_desktop_oauth_auth_code_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OAuth 短期 authorization code（10 min TTL，一次性）';


CREATE TABLE IF NOT EXISTS `w_desktop_oauth_refresh_token` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `token`           VARCHAR(128)    NOT NULL                              COMMENT '不透明 refresh token（≥ 32 bytes 随机）',
    `chain_id`        VARCHAR(64)     NOT NULL                              COMMENT 'rotation 链 id：新 refresh 共享 chain，旧 refresh 仍可 trace 到链',
    `device_id`       VARCHAR(64)     NOT NULL                              COMMENT 'Q4 决策：设备身份（独立 UUID，desktop 首次启动生成），跨登出登入持续',
    `client_id`       VARCHAR(64)     NOT NULL                              COMMENT '发起的 client（与同一 chain 内一致）',
    `uid`             INT             NOT NULL                              COMMENT 'workmax user id',
    `scope`           VARCHAR(255)    NOT NULL                              COMMENT '此 token 授予的 scope',
    `parent_id`       BIGINT UNSIGNED DEFAULT NULL                          COMMENT 'rotation 链：指向被本 token 替换的上一个 refresh id',
    `revoked`         TINYINT(1)      NOT NULL DEFAULT 0                    COMMENT '0=有效 1=已撤销（rotated / replay_detected / user_revoked）',
    `revoked_reason`  VARCHAR(50)     DEFAULT NULL                          COMMENT 'rotated | replay_detected | user_revoked | expired',
    `expires_at`      DATETIME        NOT NULL                              COMMENT '签发后 90 天过期',
    `last_used_at`    DATETIME        DEFAULT NULL                          COMMENT '最近一次成功 refresh 的时间（用户设备管理页展示）',
    `device_info`     JSON            DEFAULT NULL                          COMMENT '{os, app_version, hostname?}，仅展示用，不参与安全决策',
    `created_at`      DATETIME        DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_w_desktop_oauth_refresh_token_token` (`token`),
    KEY `idx_w_desktop_oauth_refresh_token_chain` (`chain_id`),
    KEY `idx_w_desktop_oauth_refresh_token_uid_device` (`uid`, `device_id`),
    KEY `idx_w_desktop_oauth_refresh_token_uid_chain` (`uid`, `chain_id`),
    KEY `idx_w_desktop_oauth_refresh_token_revoked_expires` (`revoked`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OAuth refresh token chain（rotation + replay 检测）';


-- Seed: workmax-desktop client. Public client (no secret); accepts any
-- loopback port on the /oauth/callback path (RFC 8252 §7.3 pattern).
-- Re-running this migration won't duplicate: ON DUPLICATE KEY UPDATE
-- noop on unique client_id.
INSERT INTO `w_desktop_oauth_client`
    (`client_id`, `client_name`, `client_type`, `redirect_uris`, `allowed_scopes`, `is_active`, `created_at`, `updated_at`)
VALUES
    (
        'workmax-desktop',
        'WorkMax Desktop',
        'public',
        JSON_ARRAY('http://127.0.0.1:*/oauth/callback'),
        JSON_ARRAY('workagent'),
        1,
        NOW(),
        NOW()
    )
ON DUPLICATE KEY UPDATE `updated_at` = NOW();
