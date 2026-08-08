-- Drops the batch queue + per-vertical render tables.
--
-- The batch queue (w_batch_job/w_batch_item) and its three vertical
-- output tables (w_ad_variant, w_ecommerce_render, w_panel_shot) only
-- ever shipped stub renders — no real video output ever flowed
-- through. The feature has been retired in favour of submitting
-- through the existing single-task generation pipeline. Removed in
-- the same change:
--   - server/api/pro/tools/{batch_job,ad_variant_matrix,
--     comic_panel_render,product_sku_batch,canvas_batch_generation}_*
--   - server/service/tools/{batch,batchworker,admatrix,comicpanel,
--     skurender,skumatrix}/
--   - server/service/tools/canvas/batch_matrix*
--   - server/model/{batch_job,ad_variant,ecommerce_render}.go
--   - server/router/pro/tools/batch_job_router.go and the
--     /api/batch-job mount, /tasks/batch canvas route, /chapter/render
--     comic route, /sku-batch product route, /variants/matrix video-ad
--     routes
--   - main.go startBatchWorker / batchworker.Stop wiring
--   - web/{components,lib,app}/.../batch* + the canvas batch-matrix
--     UI under tools/canvas/_lib/_components/_store
--
-- w_panel_shot is kept — it backs the short-drama panel-render
-- pipeline and the name overlap with comic-panel batch is incidental.
--
-- IF EXISTS keeps fresh environments (which never created the
-- tables) happy. Drop order: items before parent (FK), then vertical
-- output tables.

DROP TABLE IF EXISTS `w_batch_item`;
DROP TABLE IF EXISTS `w_batch_job`;
DROP TABLE IF EXISTS `w_ad_variant`;
DROP TABLE IF EXISTS `w_ecommerce_render`;
