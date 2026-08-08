-- P1 #6 slice 2 — bind credit reservations to platform projects.
--
-- The reservation row already tracks user-side debits via the
-- CreditsPack allocations. This column lets the same row also
-- track which project the spend is charged against, so the
-- project budget cap (slice 1) gates Reserve and the actual
-- settlement reaches the project's running tally at Finalize.
--
-- 0 = not project-scoped (the default for every existing
-- reservation + every non-agent reservation going forward).
-- A non-zero value is the w_global_project.id the agent's
-- thread is bound to at Reserve time — the reservation service
-- carries it through Finalize / Release so refunds reach the
-- right project.

ALTER TABLE `w_credit_reservation`
  ADD COLUMN `project_id` int NOT NULL DEFAULT 0
    COMMENT 'optional w_global_project.id this reservation charges against; 0 = no project scope';
