-- Rename w_shot → w_canvas_shot. The Shot model is canvas-only
-- (the foreign key is `canvas_project_id`, set in 20260421), but
-- the table name was missing the `canvas_` prefix that all other
-- canvas-tool tables (w_canvas_project, w_canvas_version,
-- w_canvas_asset, w_canvas_task_binding) carry.
--
-- The neighbouring vertical-solution table w_panel_shot keeps its
-- name — that one's a Phase-2 generic table partitioned by
-- SolutionType, not a canvas-tool concept.

RENAME TABLE `w_shot` TO `w_canvas_shot`;
