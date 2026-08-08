-- SD-S2.2: team-shared user templates.
--
-- Pre-S2.2: w_drama_template carries system templates (uid=0,
-- is_system=1) + user templates (uid=owner, is_system=0). User
-- templates were owner-only; no way to share a sweet-love-genre
-- template with the rest of the team.
--
-- Fix: optional team_id column on user templates. NULL = solo
-- (owner-only). Non-NULL = visible to every active member of that
-- team. System rows ignore the column entirely.

ALTER TABLE `w_drama_template`
  ADD COLUMN `team_id` BIGINT UNSIGNED DEFAULT NULL
    COMMENT 'team scope for user templates; NULL = solo (owner-only); ignored for system rows',
  ADD INDEX `idx_team` (`team_id`);
