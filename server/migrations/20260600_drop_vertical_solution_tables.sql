-- 20260600_drop_vertical_solution_tables.sql
--
-- Drop the 22 orphan tables left behind by commit 1d3332ee (2026-05-07,
-- "remove 4 vertical solutions, return to general video tool platform").
-- That commit deliberately left the schema intact pending a separate,
-- explicit production op — this is that op.
--
-- After this migration runs, the database matches the model code: the
-- only drama/ad/ecom tables that remain are the ones still used by the
-- canvas asset injector and character API (Character / Location /
-- DirectorPreset / CharacterFamily / BrandProfile / *_reference).
--
-- Also drops w_drama_character_relationship — the model exists in
-- server/model/drama_character.go but has zero callers, and the table
-- was never created on this DB anyway. The DROP IF EXISTS is harmless
-- and keeps environments consistent.
--
-- Rollback: restore from the corresponding create migrations
--   (20260417_create_short_drama_*.sql, 20260514_add_ad_creative.sql,
--    20260518_add_comic_schema.sql, 20260516_add_product_asset.sql,
--    20260517_add_ecommerce_video.sql, 20260555_split_w_project_per_solution.sql,
--    20260556_split_w_storyboard_per_solution.sql,
--    20260557_split_w_project_template_per_solution.sql, etc.)
-- Data is permanently lost — take a backup before running.

-- Ad / 广告
DROP TABLE IF EXISTS `w_ad_creative`;
DROP TABLE IF EXISTS `w_ad_creative_export_version`;
DROP TABLE IF EXISTS `w_ad_project`;
DROP TABLE IF EXISTS `w_ad_scene_render`;
DROP TABLE IF EXISTS `w_ad_template`;
DROP TABLE IF EXISTS `w_ad_test`;
DROP TABLE IF EXISTS `w_ad_test_variant`;

-- Comic / 漫剧
DROP TABLE IF EXISTS `w_comic_chapter`;
DROP TABLE IF EXISTS `w_comic_page`;
DROP TABLE IF EXISTS `w_comic_panel`;
DROP TABLE IF EXISTS `w_comic_project`;
DROP TABLE IF EXISTS `w_comic_storyboard`;
DROP TABLE IF EXISTS `w_comic_template`;

-- Drama / 短剧
-- Kept (still used by canvas/character paths):
--   w_drama_character, w_drama_character_reference
DROP TABLE IF EXISTS `w_drama_character_relationship`;
DROP TABLE IF EXISTS `w_drama_character_family`;
DROP TABLE IF EXISTS `w_drama_director_preset`;
DROP TABLE IF EXISTS `w_drama_episode`;
DROP TABLE IF EXISTS `w_drama_episode_activity`;
DROP TABLE IF EXISTS `w_drama_episode_export_version`;
DROP TABLE IF EXISTS `w_drama_location`;
DROP TABLE IF EXISTS `w_drama_location_reference`;
DROP TABLE IF EXISTS `w_drama_panel_shot`;
DROP TABLE IF EXISTS `w_drama_project`;
DROP TABLE IF EXISTS `w_drama_script`;
DROP TABLE IF EXISTS `w_drama_script_revision`;
DROP TABLE IF EXISTS `w_drama_storyboard`;
DROP TABLE IF EXISTS `w_drama_template`;

-- character_family was a "S2.4 phase 2" hook — never wired up. Drop the
-- placeholder column on w_drama_character so schema and code stay in sync.
ALTER TABLE `w_drama_character` DROP COLUMN `family_id`;

-- Ad / 广告 brand_profile + brand_reference were canvas asset-injector
-- placeholders ("brand channel stubbed until brand model lands"); the
-- frontend service exists but no UI calls it.
DROP TABLE IF EXISTS `w_ad_brand_profile`;
DROP TABLE IF EXISTS `w_ad_brand_reference`;

-- Ecom / 电商
DROP TABLE IF EXISTS `w_ecom_product_asset`;
DROP TABLE IF EXISTS `w_ecom_product_image`;
DROP TABLE IF EXISTS `w_ecom_project`;
DROP TABLE IF EXISTS `w_ecom_template`;
DROP TABLE IF EXISTS `w_ecom_video`;
