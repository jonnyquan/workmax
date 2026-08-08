-- Rename w_prompt_builder_config -> w_image_prompt_config to mirror the
-- video-side naming (w_video_prompt_config). The frontend already says
-- "Image Prompt Builder"; the table name now matches.
--
-- Index names also get the new prefix so we stay symmetric with
-- w_video_prompt_config (idx_vp_uid <-> idx_ip_uid). Table comment is
-- refreshed to "Image Prompt Builder用户保存配置表".
--
-- Deployment order:
--   1. Stop API server (single-instance dev box; no zero-downtime needed).
--   2. Apply this migration.
--   3. Deploy Go code that references model.ImagePromptConfig / w_image_prompt_config.
--   4. Restart server.
--
-- Rollback (within window):
--   ALTER TABLE w_image_prompt_config COMMENT='Prompt Builder用户保存配置表';
--   ALTER TABLE w_image_prompt_config
--       RENAME INDEX idx_ip_uid TO idx_pb_uid,
--       RENAME INDEX idx_w_image_prompt_config_deleted_at TO idx_w_prompt_builder_config_deleted_at;
--   RENAME TABLE w_image_prompt_config TO w_prompt_builder_config;

RENAME TABLE `w_prompt_builder_config` TO `w_image_prompt_config`;

ALTER TABLE `w_image_prompt_config`
    RENAME INDEX `idx_pb_uid` TO `idx_ip_uid`,
    RENAME INDEX `idx_w_prompt_builder_config_deleted_at` TO `idx_w_image_prompt_config_deleted_at`;

ALTER TABLE `w_image_prompt_config`
    COMMENT='Image Prompt Builder用户保存配置表';
