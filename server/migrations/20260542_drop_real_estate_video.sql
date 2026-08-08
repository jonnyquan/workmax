-- Drops w_real_estate_video. The real-estate / tourism vertical was
-- retired — its frontend pages, backend handlers, model, router,
-- and the create / rename migrations
-- (20260527_add_realestate_video.sql + 20260528_rename_real_estate_video.sql)
-- all removed in the same change. This drop catches any environment
-- that already applied either migration so the schema converges.
--
-- Drops both the original `w_realestate_video` (pre-rename) and the
-- renamed `w_real_estate_video`, since environments may be on either
-- depending on which migration ran last.
--
-- IF EXISTS keeps fresh environments (which never created the table)
-- happy.

DROP TABLE IF EXISTS `w_real_estate_video`;
DROP TABLE IF EXISTS `w_realestate_video`;
