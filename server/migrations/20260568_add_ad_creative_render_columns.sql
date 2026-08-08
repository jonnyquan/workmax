-- Phase 4: per-scene render + concat lifecycle on AdCreative.
--
-- render_status is a separate axis from the legacy `status` (which
-- tracks the authoring workflow draft→scripting→generating→reviewing
-- →approved). render_status tracks the video-output lifecycle:
--   0 idle, 1 rendering, 2 rendered, 3 assembling, 4 ready, 5 failed
--
-- output_url / output_thumbnail_url / output_duration_sec hold the
-- result of the ffmpeg concat task (Phase 4 group 5).
-- assemble_task_id points back at the GenerationTask that ran concat.

ALTER TABLE `w_ad_creative`
  ADD COLUMN `render_status` TINYINT NOT NULL DEFAULT 0 COMMENT 'render lifecycle: 0=idle 1=rendering 2=rendered 3=assembling 4=ready 5=failed',
  ADD COLUMN `render_status_message` VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'user-facing failure detail',
  ADD COLUMN `output_url` VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'final concatenated mp4',
  ADD COLUMN `output_thumbnail_url` VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'first-frame thumbnail',
  ADD COLUMN `output_duration_sec` INT NOT NULL DEFAULT 0 COMMENT 'actual final duration after concat',
  ADD COLUMN `assemble_task_id` BIGINT NOT NULL DEFAULT 0 COMMENT 'fk to w_generation_task row that ran concat';
