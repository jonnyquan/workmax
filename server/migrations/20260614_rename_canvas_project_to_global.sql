-- Plan-A Phase A4: rename w_canvas_project → w_global_project.
--
-- The table started as canvas-specific (snapshots, shares, document
-- patches) but Phases A1-A3 turned it into the platform's notion of
-- a Project — brand / character / director_style / thread all carry
-- a project_id pointing at this table, and service/project owns the
-- platform-level read API.
--
-- No view shim is created (back-compat explicitly waived by the
-- product owner — current data volume is minimal). Code references
-- update in the same commit; deploying the SQL + binary atomically
-- keeps the system consistent.
--
-- Rollback: ALTER TABLE w_global_project RENAME TO w_canvas_project.

ALTER TABLE `w_canvas_project` RENAME TO `w_global_project`;
