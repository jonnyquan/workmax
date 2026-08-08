-- SD-P2-2: project-level budget tracking for w_ad_project.
-- Mirrors 20260573_add_drama_project_budget.sql exactly.
-- budget_credits: 0 = no cap; otherwise warn at 80 % and block at 100 %.
-- credits_spent_cache / credits_spent_at: rolling cache refreshed by the
-- estimate-cost endpoint (stale > 60s triggers a re-sum).
ALTER TABLE `w_ad_project`
  ADD COLUMN `budget_credits`      INT NOT NULL DEFAULT 0
    COMMENT '0 = no cap; warn at 80% and block at 100%',
  ADD COLUMN `credits_spent_cache` INT NOT NULL DEFAULT 0
    COMMENT 'cached SUM(generation_task.credits_used); refreshed on estimate-cost read',
  ADD COLUMN `credits_spent_at`    DATETIME DEFAULT NULL
    COMMENT 'last refresh time of credits_spent_cache';
