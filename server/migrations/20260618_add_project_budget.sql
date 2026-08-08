-- P1 #6 slice 1 — project-level credit budget.
--
-- The Team / Enterprise pricing tier's "this campaign can spend at
-- most N credits" promise lands as two columns on w_global_project.
-- Reservations (slice 2) check + increment against this cap so the
-- gate is enforced at the credit-spending boundary, not at billing
-- reconciliation time.
--
-- Why NULL vs DEFAULT 0 for the cap:
--   - NULL = no cap (the default for every existing row + every
--     newly created project until the owner opts in). Reservation
--     path skips the cap check.
--   - 0   = "cap of zero credits" — paused project. Reservation
--     path rejects any new spend until the cap is raised.
-- The DEFAULT 0 / DEFAULT NULL choice has user-visible semantics;
-- pin the NULL meaning so a future "let's pre-fill 0 for everyone"
-- change knows it's flipping every existing project into a paused
-- state.
--
-- budget_credits_used is the running tally. Default 0 because a
-- fresh row hasn't spent anything yet.

ALTER TABLE `w_global_project`
  ADD COLUMN `budget_credits_cap` int DEFAULT NULL
    COMMENT 'optional per-project credit cap; NULL = uncapped',
  ADD COLUMN `budget_credits_used` int NOT NULL DEFAULT 0
    COMMENT 'running tally of credits spent against this project';
