-- Drops w_product_demo_video. The product-demo vertical was retired
-- — its frontend pages, backend handlers, model, router, and the
-- 20260534_add_product_demo_video.sql migration that created the
-- table all removed in the same change. This drop catches any
-- environment that already applied the create migration so the
-- schema converges.
--
-- IF EXISTS keeps fresh environments (which never created the table)
-- happy.

DROP TABLE IF EXISTS `w_product_demo_video`;
