-- Drops the compliance review subsystem. Its only consumer
-- (news-video) was retired alongside corporate-comms, leaving the
-- audit log + role-assignment tables with no callers.
--
-- Removed in the same change:
--   - server/api/pro/tools/compliance_*
--   - server/router/pro/tools/compliance_*
--   - server/model/compliance_*
--   - server/service/tools/compliance/
--   - web/lib/compliance/
--   - web/components/compliance/
--   - web/lib/services/compliance-*
--   - 20260536_add_compliance_audit.sql
--   - 20260537_add_compliance_review_assignment.sql
--
-- IF EXISTS keeps fresh environments (which never created the
-- tables) happy.

DROP TABLE IF EXISTS `w_compliance_audit`;
DROP TABLE IF EXISTS `w_compliance_review_assignment`;
