-- Drops the virtual-avatar + digital-human-avatar tables. The
-- virtual-avatar vertical was retired alongside its only supporting
-- asset library (digital-human-avatar, used exclusively by virtual-
-- avatar-video). Removed in the same change:
--   - server/api/pro/tools/virtual_avatar_video_*
--   - server/api/pro/tools/digital_human_avatar_*
--   - server/router/pro/tools/{virtual_avatar_video,digital_human_avatar}_router.go
--   - server/model/{virtual_avatar_video,digital_human_avatar}.go
--   - server/service/tools/digitalhuman/
--   - web/lib/{virtual-avatar,digital-human}/
--   - web/components/digital-human/
--   - web/lib/services/{virtual-avatar-video,digital-human-avatar}-service.ts
--   - 20260519_add_digital_human_avatar.sql
--   - 20260521_add_virtual_avatar_video.sql
--
-- IF EXISTS keeps fresh environments (which never created the
-- tables) happy.

DROP TABLE IF EXISTS `w_virtual_avatar_video`;
DROP TABLE IF EXISTS `w_digital_human_avatar`;
