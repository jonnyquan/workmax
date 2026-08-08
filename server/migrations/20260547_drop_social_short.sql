-- Drops the social-short vertical + the audio-asset library.
-- Social-short was retired alongside its only consumer of audio-
-- asset (no other vertical was using it). Removed in the same
-- change:
--   - server/api/pro/tools/social_account_profile_*
--   - server/api/pro/tools/social_matrix_submit_*
--   - server/api/pro/tools/audio_asset_*
--   - server/router/pro/tools/{social_account_profile,audio_asset}_router.go
--   - server/model/{social_account_profile,audio_asset}.go
--   - server/service/tools/socialmatrix/
--   - web/lib/{social-short,audio}/, web/components/{social-short,audio}/
--   - web/app/[locale]/(tools)/dashboard/audio-library/
--   - web/lib/services/{social-account-profile,audio-asset}-service.ts
--   - 20260522_add_social_account_profile.sql
--   - 20260525_add_audio_asset.sql
--   - model.BatchKindSocialMultiPlatform constant + stub handler
--
-- IF EXISTS keeps fresh environments (which never created the
-- tables) happy.

DROP TABLE IF EXISTS `w_social_account_profile`;
DROP TABLE IF EXISTS `w_audio_asset`;
