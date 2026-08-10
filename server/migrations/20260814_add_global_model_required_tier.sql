-- Desktop conversation-model catalog (GET /api/desktop/models).
--
-- Goal: after binding an account, the Desktop client can ask "which
-- conversation models does my membership allow" and the user picks a name.
-- No endpoint, no key, no provider identity is ever handed to the client —
-- inference still runs through POST /api/work-agent/chat/agent, which keeps
-- owning routing and credentials. This table only makes the two existing
-- model-tier values discoverable, nameable and explainable.
--
-- Why w_global_model rather than a new table:
--   - It already is "the platform model catalog": display_name, status
--     (enable/disable without a deploy), sort_order, metadata JSON. A new
--     table would duplicate all of it for two rows.
--   - media_type is a plain varchar, not an enum, so 'text' costs nothing.
--     The existing UNIQUE (model_id, media_type) keeps the conversation rows
--     from colliding with any same-named image/video model.
--   - Operations can already reach it through service/globalmodel.Repository,
--     so gating a model behind a tier is a data change, not a code change.
--
-- required_tier is the new axis: the membership level a caller needs before
-- the catalog grants "use" on the row. Values are the tier vocabulary from
-- model/user.go (free / pro / enterprise). DEFAULT 'free' is the safe
-- backfill for every existing image/video row — it preserves today's
-- behaviour, where the catalog itself gates nothing.

ALTER TABLE `w_global_model`
  ADD COLUMN `required_tier` varchar(20) NOT NULL DEFAULT 'free'
    COMMENT '使用该模型所需的最低会员等级：free/pro/enterprise';

-- Seed the two conversation models. model_id MUST stay in lockstep with
-- allowedModelTiers in api/pro/tools/workagent/conversation_api.go — the
-- catalog is only useful if the name it hands the user is a value the chat
-- endpoint actually accepts.
--
-- work-plus carries required_tier='pro' to mirror the runtime gate in
-- agent_turn_phases.go (IsPremiumMember before a work-plus turn runs). The
-- catalog is the explanation; that check remains the enforcement.
INSERT INTO `w_global_model`
  (`model_id`, `media_type`, `provider_type`, `display_name`, `status`,
   `pricing_status`, `sort_order`, `required_tier`, `capabilities`, `metadata`)
VALUES
  ('work-pro', 'text', '', 'Work Pro', 1, '', 200, 'free',
   JSON_OBJECT('conversation', true, 'default', true),
   JSON_OBJECT('source', 'desktop-conversation-catalog',
               'description', 'Balanced everyday model. Available on every plan.')),
  ('work-plus', 'text', '', 'Work Plus', 1, '', 100, 'pro',
   JSON_OBJECT('conversation', true, 'default', false),
   JSON_OBJECT('source', 'desktop-conversation-catalog',
               'description', 'Highest-capability model. Requires a paid membership.'))
ON DUPLICATE KEY UPDATE
  `display_name`  = VALUES(`display_name`),
  `status`        = VALUES(`status`),
  `sort_order`    = VALUES(`sort_order`),
  `required_tier` = VALUES(`required_tier`),
  `capabilities`  = VALUES(`capabilities`),
  `metadata`      = VALUES(`metadata`);
