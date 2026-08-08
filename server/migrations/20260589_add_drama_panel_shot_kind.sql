-- T1.A — track preview attempts as panel-shot rows.
--
-- Problem before this migration: storyboard panels had a two-step UX
-- (Preview → Video) but only video renders were persisted to
-- w_drama_panel_shot. Preview images were written to
-- w_drama_storyboard.panels_json[].referenceImageUrl, a single field
-- that gets overwritten on every regeneration. Users lost preview
-- iteration history and the cost ledger couldn't tell the difference
-- between cheap image previews and expensive video renders.
--
-- Fix: discriminate panel-shot rows by `kind` (video|preview) and add
-- a dedicated image_url column. Preview rows carry image_url; video
-- rows carry video_url. ListPanelShots returns both grouped so the
-- candidates drawer can offer "browse my last 5 previews" without a
-- separate query path.
--
-- Soft cap (panelShotMaxPerPanel = 20) applies per-kind, not shared:
-- otherwise 20 previews would lock the user out of recording any
-- video, which is the opposite of the intent. Index covers the
-- list-by-kind read path.
--
-- Backward-compatible: existing rows default to kind='video' so any
-- pre-migration shot continues to behave as a video candidate.

ALTER TABLE `w_drama_panel_shot`
  ADD COLUMN `kind` VARCHAR(16) NOT NULL DEFAULT 'video'
    COMMENT 'video|preview - 区分视频候选与图片预览';

ALTER TABLE `w_drama_panel_shot`
  ADD COLUMN `image_url` VARCHAR(2048) NOT NULL DEFAULT ''
    COMMENT 'kind=preview 时的图片资源URL';

ALTER TABLE `w_drama_panel_shot`
  ADD INDEX `idx_panel_kind` (`storyboard_id`, `panel_uuid`, `kind`);
