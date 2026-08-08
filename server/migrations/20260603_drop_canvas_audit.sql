-- Drop Canvas audit log table.
--
-- Canvas audit writes were removed from the application hot path because
-- they created high-frequency write pressure without current product value.
-- The table is no longer mapped by GORM or referenced by Canvas routes.
--
-- Rollback: recreate the removed Canvas audit model/table from backup if
-- historical audit rows are still required.

DROP TABLE IF EXISTS `w_canvas_audit`;
