-- Drops the knowledge-education tables. The knowledge vertical was
-- retired — its frontend pages, backend handlers (unit + unit-video
-- + translate), router, model, and the
-- 20260524_add_knowledge_unit.sql migration that created the tables
-- all removed in the same change. This drop catches any environment
-- that already applied the create migration so the schema converges.
--
-- IF EXISTS keeps fresh environments (which never created the
-- tables) happy.

DROP TABLE IF EXISTS `w_knowledge_unit_video`;
DROP TABLE IF EXISTS `w_knowledge_unit`;
