-- Drops w_news_video. The news-video vertical was retired — its
-- frontend pages, backend handlers, model, router, and the
-- 20260523_add_news_video.sql migration that created the table all
-- removed in the same change. This drop catches any environment
-- that already applied the create migration so the schema converges.
--
-- IF EXISTS keeps fresh environments (which never created the table)
-- happy.

DROP TABLE IF EXISTS `w_news_video`;
