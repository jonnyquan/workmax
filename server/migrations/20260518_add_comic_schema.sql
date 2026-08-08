-- Comic-video foundation (step 8 in solutions-overview §6.4).
-- Last of the five vertical solutions — closes the §6.4 build order.
--
-- Scope of this migration:
--
--   w_comic_chapter — a chapter within a comic project (1 project
--                     = N chapters). Lighter than short-drama's
--                     Episode since comics are visually-driven;
--                     no synopsis/duration fields.
--
--   w_comic_panel   — one comic frame extracted from a page image.
--                     reading_order is 1-based, contiguous across
--                     the chapter (not reset per page) so the
--                     downstream storyboard pipeline sees a flat
--                     sequence it can render in order.
--
--   w_comic_page    — one imported page image; spawns N panels
--                     after segmentation. Kept separate from
--                     w_comic_panel because a page is the raw
--                     input and panels are the derived output —
--                     re-running the detector should not nuke
--                     the original upload.
--
-- Comic-specific fields on the project row come later when the
-- frontend needs them (e.g. reading_direction = ltr/rtl). For Phase
-- 1 we keep them in settings_json and promote only if the query
-- patterns justify it.

CREATE TABLE `w_comic_chapter` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL DEFAULT '' COMMENT '对外UUID',
  `project_id` bigint unsigned NOT NULL COMMENT '漫剧项目ID',
  `uid` int NOT NULL COMMENT '创建者',
  `chapter_number` int NOT NULL DEFAULT 0 COMMENT '章节号,在项目内唯一',
  `title` varchar(200) NOT NULL DEFAULT '' COMMENT '章节名',
  `summary` text COMMENT '章节简介',
  `page_count` int NOT NULL DEFAULT 0 COMMENT '已导入页数',
  `panel_count` int NOT NULL DEFAULT 0 COMMENT '已分格面板数',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=draft 2=segmenting 3=ready 4=storyboarded',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_comic_chapter_uuid` (`uuid`),
  UNIQUE KEY `uk_project_chapter_number` (`project_id`, `chapter_number`),
  KEY `idx_uid` (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='漫剧章节 — project下的内容单元';

CREATE TABLE `w_comic_page` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `chapter_id` bigint unsigned NOT NULL,
  `project_id` bigint unsigned NOT NULL COMMENT '冗余便于按项目筛选',
  `uid` int NOT NULL,
  `page_number` int NOT NULL DEFAULT 0 COMMENT '页码,章节内唯一',
  `image_url` varchar(2048) NOT NULL DEFAULT '' COMMENT '页面图URL',
  `width` int NOT NULL DEFAULT 0,
  `height` int NOT NULL DEFAULT 0,
  `segmentation_json` json DEFAULT NULL COMMENT '分格结果快照 [{x,y,w,h}]',
  `segmented_at` datetime DEFAULT NULL,
  `status` tinyint NOT NULL DEFAULT 1
    COMMENT '1=uploaded 2=segmenting 3=segmented 4=error',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_chapter_page` (`chapter_id`, `page_number`),
  KEY `idx_project` (`project_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='漫剧页面 — 分格前的原始上传';

CREATE TABLE `w_comic_panel` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL DEFAULT '' COMMENT '稳定UUID',
  `chapter_id` bigint unsigned NOT NULL,
  `project_id` bigint unsigned NOT NULL COMMENT '冗余便于筛选',
  `page_id` bigint unsigned NOT NULL COMMENT '来源页面',
  `uid` int NOT NULL,
  `reading_order` int NOT NULL DEFAULT 0
    COMMENT '章节内阅读顺序,1-based连续不重置',
  `page_panel_index` int NOT NULL DEFAULT 0
    COMMENT '在当前页的局部索引 (0-based)',
  `image_url` varchar(2048) NOT NULL DEFAULT '' COMMENT '已切分面板图',
  `bbox_json` json DEFAULT NULL COMMENT '在源页面的 {x,y,w,h} 坐标',
  `dialogue_json` json DEFAULT NULL
    COMMENT 'OCR或手动补录的对白 [{speaker,text,bubble_bbox}]',
  `visual_description` text COMMENT 'AI或手动补录的画面描述',
  `featured_character_slugs_json` json DEFAULT NULL
    COMMENT '检测到的角色slug数组,用于一致性prompt',
  `storyboard_panel_uuid` varchar(36) NOT NULL DEFAULT ''
    COMMENT '已生成的短剧分镜panelUUID (关联)',
  `status` tinyint NOT NULL DEFAULT 1
    COMMENT '1=segmented 2=ocr_done 3=storyboarded 4=error',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_comic_panel_uuid` (`uuid`),
  UNIQUE KEY `uk_chapter_reading_order` (`chapter_id`, `reading_order`),
  KEY `idx_page` (`page_id`),
  KEY `idx_project` (`project_id`),
  KEY `idx_storyboard_panel` (`storyboard_panel_uuid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='漫剧分格 — 每格对应生成视频中的一个镜头';
