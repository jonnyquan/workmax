-- SD-S1.1: in-flight project budget reservations.
--
-- Pre-S1.1 budget gate was a stateless read: it summed
-- credits_spent_cache + estimated, returned ok/reject, then forgot. Two
-- parallel requests both saw cache=80 / cap=100 / est=20 and both
-- succeeded — collectively spending 120. The action was real (each got
-- a w_generation_task row) but no longer correctable post-hoc.
--
-- Fix: a "reserved pending" counter on the project, incremented atomically
-- inside the gate's transaction (with SELECT … FOR UPDATE on the project
-- row). Concurrent gates serialise on the row lock; the second sees the
-- first's reservation in the counter and either fits or rejects.
--
-- Released after the action completes — successful actions release into
-- the credit-spent cache (refreshed from w_generation_task), failures
-- just decrement back to 0.

ALTER TABLE `w_drama_project`
  ADD COLUMN `credits_reserved_pending` INT NOT NULL DEFAULT 0
    COMMENT 'in-flight credit reservations from the budget gate; reset to 0 after action completes';
