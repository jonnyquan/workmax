-- Drops w_corporate_video. The corporate-comms vertical was retired
-- — its frontend pages, backend handlers, model, router, and the
-- 20260535_add_corporate_video.sql migration that created the table
-- all removed in the same change. This drop catches any environment
-- that already applied the create migration so the schema converges.
--
-- The shared compliance subsystem (w_compliance_audit +
-- w_compliance_review_assignment) was retired separately in the
-- news-video cleanup since news-video was its only other consumer.
--
-- IF EXISTS keeps fresh environments (which never created the table)
-- happy.

DROP TABLE IF EXISTS `w_corporate_video`;
