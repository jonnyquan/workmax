-- Drops w_music_video. The music-video vertical was retired — its
-- frontend pages, backend handlers, model, router, and the
-- 20260526_add_music_video.sql migration that created the table all
-- removed in the same change. This drop catches any environment that
-- already applied the create migration so the schema converges.
--
-- IF EXISTS keeps fresh environments (which never created the table)
-- happy.

DROP TABLE IF EXISTS `w_music_video`;
