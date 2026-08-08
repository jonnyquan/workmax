-- Project templates — opinionated starting points for a new vertical-solution
-- project. MVP ships 5 short-drama presets.
--
-- User-created templates are allowed by schema (is_system=0, uid=user) but
-- the CRUD surface is system-only for Phase 2. Users can save their own in
-- a later phase.

CREATE TABLE IF NOT EXISTS `w_project_template` (
  `id`                       bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`               datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`               datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`               datetime DEFAULT NULL,
  `solution_type`            varchar(32) NOT NULL DEFAULT 'short_drama',
  `slug`                     varchar(64) NOT NULL DEFAULT '',
  `name`                     varchar(120) NOT NULL DEFAULT '',
  `description`              varchar(500) NOT NULL DEFAULT '',
  `genre`                    varchar(64) NOT NULL DEFAULT '',
  `default_episode_count`    int NOT NULL DEFAULT 0 COMMENT '推荐默认集数',
  `default_episode_duration` int NOT NULL DEFAULT 0 COMMENT '推荐单集时长秒',
  `default_aspect_ratio`     varchar(16) NOT NULL DEFAULT '9:16',
  `default_style_preset`     varchar(64) NOT NULL DEFAULT '',
  `default_target_platform`  varchar(64) NOT NULL DEFAULT '',
  `narrative_structure`      varchar(500) NOT NULL DEFAULT '' COMMENT '叙事结构描述,展示给用户',
  `emotional_template`       varchar(64) NOT NULL DEFAULT '' COMMENT 'outline生成时传给LLM的模板hint',
  `is_system`                tinyint(1) NOT NULL DEFAULT 0 COMMENT '1=平台预置',
  `uid`                      int NOT NULL DEFAULT 0 COMMENT '系统模板为0,用户模板为owner',
  `sort_order`               int NOT NULL DEFAULT 0,
  `status`                   tinyint NOT NULL DEFAULT 1 COMMENT '1=active 2=archived',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_slug_solution` (`slug`, `solution_type`, `deleted_at`),
  KEY `idx_solution_system` (`solution_type`, `is_system`, `status`),
  KEY `idx_uid` (`uid`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Seed the 5 short-drama presets. Kept terse in default_* fields so users
-- aren't boxed in; narrative_structure + emotional_template carry the
-- opinion that differentiates each template.
INSERT INTO `w_project_template` (
  `solution_type`, `slug`, `name`, `description`,
  `genre`, `default_episode_count`, `default_episode_duration`,
  `default_aspect_ratio`, `default_style_preset`, `default_target_platform`,
  `narrative_structure`, `emotional_template`,
  `is_system`, `uid`, `sort_order`, `status`
) VALUES
  ('short_drama', 'sweet-romance', '甜宠日常', '轻松甜蜜的日常恋爱故事 — 每集一个小冲突,最终化解为甜蜜。适合日更矩阵号。',
   'sweet_love', 15, 90, '9:16', 'warm-soft', 'douyin,kuaishou',
   '每集: 误会 → 化解 → 甜蜜收尾', 'misunderstanding-reconciliation',
   1, 0, 10, 1),

  ('short_drama', 'ceo-drama', '霸总虐恋', '身份反差引发的虐恋故事 — 冷峻霸总遇上命定女主,虐恋反转最终 HE。短剧 APP 热门题材。',
   'domineering', 20, 90, '9:16', 'cinematic-cool', 'douyin,short-drama-app',
   '身份反差 → 虐恋累积 → 真相反转 → HE 结局', '3-act-reversal',
   1, 0, 20, 1),

  ('short_drama', 'mystery', '悬疑烧脑', '每集一个案件线索 — 通过误导和反转抓住观众,适合喜欢悬疑题材的全年龄观众。',
   'suspense', 12, 90, '9:16', 'dark-cinematic', 'all-platforms',
   '案件发生 → 线索收集 → 误导与反转 → 真相揭晓', '3-act-reversal',
   1, 0, 30, 1),

  ('short_drama', 'time-travel', '穿越逆袭', '现代人穿越到古代,运用现代知识逆袭翻盘 — 节奏明快,爽感直给。',
   'time_travel', 18, 90, '9:16', 'period-warm', 'douyin,kuaishou',
   '穿越 → 适应环境 → 利用现代知识 → 逐步逆袭', 'underdog-rise',
   1, 0, 40, 1),

  ('short_drama', 'urban-reversal', '都市爽文', '被欺压主角觉醒碾压对手 — 爽点密集,情绪释放型短剧。',
   'urban', 15, 90, '9:16', 'urban-cool', 'all-platforms',
   '被欺压 → 觉醒 → 碾压对手 → 爽感收尾', 'underdog-rise',
   1, 0, 50, 1);
