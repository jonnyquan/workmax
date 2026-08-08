-- Rename w_agent_accounts → w_agent_account. The table holds rows
-- of one entity (one row = one provider account); plural was an
-- early-project habit that's out of step with the rest of the
-- schema (w_user / w_team_member / w_audio_asset / ...).

RENAME TABLE `w_agent_accounts` TO `w_agent_account`;
