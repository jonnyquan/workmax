-- w_global_director_style + w_global_director_style_reference —
-- Sprint-E 2/8 platform-level director-style asset tables. Sibling
-- of w_global_brand (20260611_create_platform_brand.sql); same
-- design contract (uid + project_id + lang scoping, soft-delete,
-- flat Status int8 + Confirmed bool + ConfirmedAt lifecycle,
-- 6-layer anchors machinery, source traceability, prompt
-- scaffolding).
--
-- Director-style-specific: 5 cinematic axes (composition / color /
-- lighting / motion / texture) — each in its own JSON column so
-- preflight injectors can slice (a still-image generator might
-- pull only color + lighting; a motion-graphics tool pulls all 5).
--
-- Distinct from skills/_shared/visual-directions.yaml (M2 fallback
-- presets):
--   - yaml is global, 5 fallback options
--   - this table is per-uid (or system row), unbounded library
--   - the preflight read is choose-this-one, not pick-from-five
--
-- See model/director_style.go for the canonical schema source-of-
-- truth.

CREATE TABLE IF NOT EXISTS `w_global_director_style` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,

  -- Scoping. Same posture as w_global_brand / w_global_character.
  `uid`                   int NOT NULL DEFAULT 0,
  `project_id`            bigint unsigned DEFAULT NULL,
  `team_id`               bigint unsigned DEFAULT NULL,
  `lang`                  varchar(16) NOT NULL DEFAULT 'en',

  -- Identity. era / genre help the library UI categorise without
  -- re-parsing description (e.g. era="60s", genre="noir").
  `name`                  varchar(120) NOT NULL DEFAULT '',
  `slug`                  varchar(160) NOT NULL DEFAULT '',
  `era`                   varchar(64) NOT NULL DEFAULT '',
  `genre`                 varchar(64) NOT NULL DEFAULT '',

  -- 5 cinematic axes. Each independent enough that preflight may
  -- pull just one slice. Empty (NULL / null / {}) when the source
  -- didn't surface that axis; the composer drops empty fields
  -- cleanly.
  `composition`           json DEFAULT NULL,  -- 构图 / 几何 / 对称
  `color`                 json DEFAULT NULL,  -- 调色 / grading
  `lighting`              json DEFAULT NULL,  -- 光照
  `motion`                json DEFAULT NULL,  -- 运镜 / 节奏
  `texture`               json DEFAULT NULL,  -- 质感 / grain / post

  -- Staleness machinery (parity with w_global_brand + w_global_character).
  `identity_anchors`      json DEFAULT NULL,
  `negative_anchors`      json DEFAULT NULL,
  `anchors_version`       int NOT NULL DEFAULT 1,
  `appearance_hash`       char(16) NOT NULL DEFAULT '',
  `calibrated_at`         datetime DEFAULT NULL,

  -- Prompt scaffolding.
  `prompt_suffix`         text,
  `negative_prompt`       text,

  -- Source traceability.
  `source_kind`           varchar(32) NOT NULL DEFAULT 'manual',
  `source_url`            varchar(2048) NOT NULL DEFAULT '',
  `source_thread_id`      bigint unsigned DEFAULT NULL,
  `raw_spec_md`           mediumtext,

  -- Lifecycle (same shape as w_global_brand).
  `status`                tinyint NOT NULL DEFAULT 1,
  `confirmed`             tinyint(1) NOT NULL DEFAULT 1,
  `confirmed_at`          datetime DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_director_uid_status`    (`uid`, `status`),
  KEY `idx_global_director_project`       (`project_id`),
  KEY `idx_global_director_team`          (`team_id`),
  KEY `idx_global_director_lang_slug`     (`lang`, `slug`),
  KEY `idx_global_director_genre`         (`genre`),
  KEY `idx_global_director_source_thread` (`source_thread_id`),
  KEY `idx_global_director_deleted_at`    (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- w_global_director_style_reference — reference imagery for a
-- DirectorStyle: reel clips, stills, lookbooks, lighting refs.
-- Sibling of w_global_character_reference + w_global_brand_reference;
-- same column shape with a kind-specific reference_type taxonomy.
CREATE TABLE IF NOT EXISTS `w_global_director_style_reference` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`        datetime DEFAULT NULL,

  `director_style_id` bigint unsigned NOT NULL,
  `uid`               int NOT NULL DEFAULT 0,
  `image_url`         varchar(2048) NOT NULL DEFAULT '',
  -- Director-style reference types:
  --   reel_clip / still / lookbook / lighting_ref
  `reference_type`    varchar(32) NOT NULL DEFAULT 'still',
  `label`             varchar(80) NOT NULL DEFAULT '',
  `sort_order`        int NOT NULL DEFAULT 0,
  -- metadata can carry e.g. timecode for reel_clip, frame count for
  -- still sequences, etc.
  `metadata`          json DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_director_ref`         (`director_style_id`),
  KEY `idx_global_director_ref_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
