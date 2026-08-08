-- Phase B1 + B3: per-user external MCP connector registry with
-- at-rest encrypted secrets.
--
-- Each row is one MCP server the agent should load on every turn
-- for the owning user. Transports: stdio (subprocess), sse, http.
-- env / headers columns hold AES-GCM ciphertext envelopes
-- (v1:nonce:ciphertext) produced by server/service/secrets; the
-- master key lives in $WORKMAX_SECRETS_KEY.

CREATE TABLE `w_global_mcp_connector` (
  `id`         bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  `uid`        bigint NOT NULL DEFAULT 0 COMMENT 'owning user',
  `name`       varchar(120) NOT NULL DEFAULT '' COMMENT 'user-facing label',
  `transport`  varchar(16) NOT NULL DEFAULT 'stdio' COMMENT 'stdio/sse/http',
  `command`    varchar(255) NOT NULL DEFAULT '' COMMENT 'stdio binary path',
  `args`       json DEFAULT NULL COMMENT 'stdio argv (JSON array of strings; not secret)',
  `env`        text DEFAULT NULL COMMENT 'stdio env overlay (encrypted-at-rest envelope: v1:nonce:ciphertext)',
  `url`        varchar(2048) NOT NULL DEFAULT '' COMMENT 'sse/http endpoint',
  `headers`    text DEFAULT NULL COMMENT 'sse/http auth headers (encrypted-at-rest envelope)',
  `enabled`    tinyint(1) DEFAULT 1 COMMENT 'per-uid kill switch',
  PRIMARY KEY (`id`),
  KEY `idx_mcp_connector_uid_enabled` (`uid`, `enabled`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Per-user MCP connector registry';
