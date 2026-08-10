-- Desktop model gateway (POST /api/desktop/model-gateway/*).
--
-- The gateway lets the Desktop's LOCAL tool loop use official models without
-- ever holding a provider key: the client sends our URL and its own Desktop
-- OAuth bearer, and the server attaches the platform credential on the way
-- out. That means every call spends platform money on a user's behalf, so
-- every call must leave a row.
--
-- Why a new table rather than the existing billing machinery:
--
--   * w_credit_reservation (service/account/credit_reservation_service.go) is
--     the live path for a cloud agent TURN. It reserves an up-front credit
--     amount and settles it against total_cost_usd reported by the SDK. The
--     gateway has neither input: it is called many times per user-visible
--     turn, and the upstream reports TOKENS, not USD. Reserving per HTTP hop
--     would invent a per-model token price the catalog does not carry and
--     would re-shape billing from per-turn to per-hop.
--
--   * w_agent_turn_reservation_binding / w_agent_turn_settlement_outcome
--     (service/agentbilling) is a complete, tested settlement ledger — and is
--     currently wired to nothing in production. Using it would require first
--     landing the durable Turn kernel (w_agent_turn + attempts + fencing
--     tokens) on this path. That is a larger change than the gateway itself.
--
-- So this release meters and does not charge. The row is the evidence a later
-- settlement pass needs: who called, which catalog model, which platform
-- credential paid, and how many tokens went out and came back.
--
-- Nothing in this table is ever serialized to a client. provider_account_id
-- in particular is an internal join onto w_workagent_account.

CREATE TABLE IF NOT EXISTS `w_desktop_model_gateway_usage` (
  `id`                      bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`              datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`              datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

  `uid`                     bigint unsigned NOT NULL COMMENT '调用者用户ID（w_user.id）',
  `request_id`              varchar(64)  NOT NULL COMMENT '网关请求ID（我们自己签发，可用于支持工单对账）',
  `protocol`                varchar(16)  NOT NULL COMMENT 'anthropic / openai',
  `model_id`                varchar(128) NOT NULL COMMENT '目录 modelId（w_global_model.model_id）',
  `upstream_model`          varchar(128) NOT NULL DEFAULT '' COMMENT '实际出网的供应商模型名',
  `provider_account_id`     bigint unsigned NOT NULL DEFAULT 0 COMMENT 'w_workagent_account.id，仅服务端可见',
  `stream`                  tinyint(1)   NOT NULL DEFAULT 0 COMMENT '是否流式',

  `status`                  varchar(16)  NOT NULL DEFAULT 'completed' COMMENT 'completed / failed',
  `http_status`             int          NOT NULL DEFAULT 0 COMMENT '网关返回给 Desktop 的状态码（不是上游的）',
  `error_class`             varchar(32)  NOT NULL DEFAULT '' COMMENT '我们的错误归类，绝不写入上游原文',

  `input_tokens`            int NOT NULL DEFAULT 0,
  `output_tokens`           int NOT NULL DEFAULT 0,
  `cache_read_tokens`       int NOT NULL DEFAULT 0,
  `cache_creation_tokens`   int NOT NULL DEFAULT 0,
  `total_tokens`            int NOT NULL DEFAULT 0 COMMENT '落库而非派生：报表不必知道各协议填了哪几列',

  `duration_ms`             int NOT NULL DEFAULT 0,
  `started_at`              datetime NULL DEFAULT NULL COMMENT '开始请求上游的时刻',

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_dmg_usage_request` (`request_id`),
  KEY `idx_dmg_usage_uid_created` (`uid`, `started_at`),
  KEY `idx_dmg_usage_model` (`model_id`),
  KEY `idx_dmg_usage_account` (`provider_account_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Desktop 模型网关用量记录（计量，本期不扣费）';

-- The catalog modelId is a product name ("work-pro"), not something a provider
-- will accept. metadata.upstreamModel carries the real provider model name;
-- the gateway fails closed with a clear error when it is absent, rather than
-- sending the product name upstream and surfacing a confusing provider 404.
--
-- These values are operator-editable data, not code. Retune them here (or via
-- service/globalmodel.Repository.Upsert) when the provider catalogue moves.
UPDATE `w_global_model`
   SET `metadata` = JSON_SET(COALESCE(`metadata`, JSON_OBJECT()), '$.upstreamModel', 'claude-sonnet-5')
 WHERE `model_id` = 'work-pro' AND `media_type` = 'text';

UPDATE `w_global_model`
   SET `metadata` = JSON_SET(COALESCE(`metadata`, JSON_OBJECT()), '$.upstreamModel', 'claude-opus-5')
 WHERE `model_id` = 'work-plus' AND `media_type` = 'text';
