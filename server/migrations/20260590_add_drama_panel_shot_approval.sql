-- T3 — feedback loop seed.
--
-- Two pieces of data the platform doesn't capture today:
--   1. Which panel-shot did the team actually like? Right now we have
--      "selected" (used as the active take) but no signal about
--      whether it's "good", "bad", or just "default fallback". That
--      makes it impossible to mine successful patterns later.
--   2. After an export, where did the episode actually get published?
--      Right now `episode.output_url` only points at the platform's
--      generated MP4. We don't know if/where the user uploaded it
--      to TikTok, Douyin, YouTube, etc. — which is the only signal
--      that connects creative output to business result.
--
-- Both fields are append-only and zero-cost-to-skip:
--   - approval defaults to 0 (unrated). Existing rows stay untouched.
--   - published_urls defaults to NULL. Existing episodes unaffected.
--
-- The approval index is composite (storyboard_id, panel_uuid, approval)
-- so the future "show me top-rated panels for this storyboard" query
-- doesn't full-scan the table.

ALTER TABLE `w_drama_panel_shot`
  ADD COLUMN `approval` TINYINT NOT NULL DEFAULT 0
    COMMENT 'T3 反馈: -1=差 / 0=未评 / 1=好';

ALTER TABLE `w_drama_panel_shot`
  ADD COLUMN `approval_by_uid` INT NOT NULL DEFAULT 0
    COMMENT '最后一次评分的用户 uid';

ALTER TABLE `w_drama_panel_shot`
  ADD COLUMN `approval_at` DATETIME DEFAULT NULL
    COMMENT '最后一次评分时间';

ALTER TABLE `w_drama_panel_shot`
  ADD INDEX `idx_panel_approval` (`storyboard_id`, `panel_uuid`, `approval`);

ALTER TABLE `w_drama_episode`
  ADD COLUMN `published_urls_json` JSON DEFAULT NULL
    COMMENT 'T3 发布去向: [{platform, url, postedAt, note}]';
