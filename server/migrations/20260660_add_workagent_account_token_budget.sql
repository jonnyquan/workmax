-- Account-pool token-spend budget.
--
-- Work Agent receives upstream token cost as SDK result.total_cost_usd and
-- settles that into user credits. These account-level fields therefore track
-- "token spend budget" in settled credits rather than fake raw-token counts.
-- A cap of 0 means unlimited. monthly_token_budget_month is YYYY-MM and lets
-- routing ignore stale usage at month rollover until the first successful turn
-- resets the counter.

ALTER TABLE `w_workagent_account`
  ADD COLUMN `monthly_token_budget_credits` int NOT NULL DEFAULT 0
    COMMENT '月度token成本预算credits，0表示不限制'
    AFTER `last_error_at`,
  ADD COLUMN `monthly_token_credits_used` int NOT NULL DEFAULT 0
    COMMENT '当前月份已消耗token成本credits'
    AFTER `monthly_token_budget_credits`,
  ADD COLUMN `monthly_token_budget_month` varchar(7) NOT NULL DEFAULT ''
    COMMENT 'token预算统计月份YYYY-MM'
    AFTER `monthly_token_credits_used`,
  ADD INDEX `idx_workagent_account_token_budget`
    (`status`, `monthly_token_budget_month`, `monthly_token_budget_credits`, `monthly_token_credits_used`);
