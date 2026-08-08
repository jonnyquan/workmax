-- Rename the short-drama-specific tables to generic names so upcoming
-- vertical solutions (comic-video, video-ad, ecommerce) can share the same
-- Project → Episode → Script → Storyboard schema, discriminated by a
-- solution_type column.
--
-- Rationale: `vertical-solutions-overview.md §5` specs a single Project
-- entity spanning all solution types. The M1-M7 sprint shipped with
-- short-drama-prefixed tables; while no other solution is live yet, now is
-- the cheapest moment to generalise — no data to migrate, and all
-- short-drama rows get defaulted to solution_type = 'short_drama'.
--
-- Forward-compat: when comic-video lands, it just inserts with
-- solution_type = 'comic_video' into the same tables.

RENAME TABLE
  `w_short_drama_project`    TO `w_project`,
  `w_short_drama_episode`    TO `w_episode`,
  `w_short_drama_script`     TO `w_script`,
  `w_short_drama_storyboard` TO `w_storyboard`;

ALTER TABLE `w_project`
  ADD COLUMN `solution_type` varchar(32) NOT NULL DEFAULT 'short_drama'
    COMMENT '方案类型 short_drama/comic_video/video_ad/ecommerce/...'
    AFTER `uid`,
  ADD INDEX `idx_solution_type` (`solution_type`);

ALTER TABLE `w_episode`
  ADD COLUMN `solution_type` varchar(32) NOT NULL DEFAULT 'short_drama'
    COMMENT '方案类型,与父 project 保持一致'
    AFTER `uid`;

ALTER TABLE `w_script`
  ADD COLUMN `solution_type` varchar(32) NOT NULL DEFAULT 'short_drama'
    COMMENT '方案类型,与父 project 保持一致'
    AFTER `uid`;

ALTER TABLE `w_storyboard`
  ADD COLUMN `solution_type` varchar(32) NOT NULL DEFAULT 'short_drama'
    COMMENT '方案类型,与父 project 保持一致'
    AFTER `uid`;
