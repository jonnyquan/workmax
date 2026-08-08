-- w_global_character + w_global_character_reference — Sprint-E 2/8
-- platform-level character asset tables. Sibling of w_global_brand
-- (20260611_create_platform_brand.sql) and w_global_director_style
-- (20260611_create_platform_director_style.sql); same design
-- contract (uid + project_id + lang scoping, soft-delete, flat
-- Status int8 + Confirmed bool + ConfirmedAt lifecycle, 6-layer
-- anchors machinery, source traceability, prompt scaffolding).
--
-- Character-specific (additional to the shared shape): identity
-- + visual fields the canvas @-mention pipeline + TTS voice
-- preset reader consume (avatar_image_url, role_type, gender,
-- age_range, appearance, personality, visual_dna_json,
-- voice_preset, previous_avatar_image_url, lora_model_id).
--
-- Fresh CREATE TABLE rather than a rename + ALTER because the
-- legacy w_character (drama era) has been cleared. Any environment
-- running migrations from scratch lands at w_global_character
-- directly with no intermediate naming.
--
-- i18n model: system rows (uid=0 + project_id IS NULL) fan out by
-- (slug, lang);
-- user / project rows carry lang as authoring-locale metadata only.
--
-- See model/character.go for the canonical schema source-of-truth.

CREATE TABLE IF NOT EXISTS `w_global_character` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,

  -- Owner + scoping. uid is the canonical owner; project_id is
  -- NULL for the user's global character library, non-NULL for a
  -- project-scoped character.
  `uid`                   int NOT NULL DEFAULT 0,
  `project_id`            bigint unsigned DEFAULT NULL,
  `lang`                  varchar(16) NOT NULL DEFAULT 'en',

  -- Identity.
  `name`                  varchar(120) NOT NULL DEFAULT '',
  `slug`                  varchar(160) NOT NULL DEFAULT '',

  -- Visual + role attributes.
  `avatar_image_url`          varchar(2048) NOT NULL DEFAULT '',
  `role_type`                 varchar(32)   NOT NULL DEFAULT 'supporting' COMMENT '角色定位 protagonist/supporting/extra',
  `gender`                    varchar(32)   NOT NULL DEFAULT '',
  `age_range`                 varchar(32)   NOT NULL DEFAULT '',
  `appearance`                text                                       COMMENT '外观描述',
  `personality`               text                                       COMMENT '性格描述',
  `visual_dna_json`           json          DEFAULT NULL                 COMMENT '结构化视觉特征',
  `previous_avatar_image_url` varchar(2048) NOT NULL DEFAULT ''          COMMENT '上一次AI生成前的头像URL (one-level undo)',
  `lora_model_id`             bigint unsigned DEFAULT NULL               COMMENT '关联LoRA模型',
  `voice_preset`              varchar(64)   DEFAULT NULL                 COMMENT 'TTS声音预设ID',

  -- Staleness machinery (parity with w_global_brand + w_global_director_style).
  --
  -- identity_anchors is the structured 6-layer character consistency
  -- anchor (bone structure / features / unique marks / color / skin /
  -- hair). Populated by the AI calibrator. Consumed by image+video
  -- prompt assembly to hold cross-shot visual consistency.
  --
  -- appearance_hash is T4's facet-aware staleness signal: a 16-hex-
  -- char digest over the visually-affecting subset (appearance +
  -- identity_anchors + negative_anchors). Identity-only edits (e.g.
  -- personality, role_type) leave the hash unchanged so the
  -- stale-shot detector doesn't flag visually-correct renders on
  -- every metadata tweak.
  `identity_anchors`      json DEFAULT NULL,
  `negative_anchors`      json DEFAULT NULL,
  `anchors_version`       int NOT NULL DEFAULT 1,
  `appearance_hash`       char(16) NOT NULL DEFAULT '',
  `calibrated_at`         datetime DEFAULT NULL,

  -- Prompt scaffolding.
  `prompt_suffix`         text,
  `negative_prompt`       text,

  -- Source traceability.
  `source_kind`           varchar(32) NOT NULL DEFAULT 'manual' COMMENT '来源 manual/comic_extracted/template/extracted/uploaded/imported',
  `source_thread_id`      bigint unsigned DEFAULT NULL          COMMENT '来源线程ID (workagent extraction)',

  -- Lifecycle. Status int8=1 means active. Confirmed bool +
  -- ConfirmedAt back the M4 Vocalize step: canvas-created
  -- characters default Confirmed=1 via the DEFAULT below; workagent
  -- draft extractions write Confirmed=0 explicitly via Select("*")
  -- to survive GORM's zero-value-skip.
  `status`                tinyint NOT NULL DEFAULT 1,
  `confirmed`             tinyint(1) NOT NULL DEFAULT 1 COMMENT '用户已确认 (workagent M4 vocalize 后置位)',
  `confirmed_at`          datetime DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_character_uid_status`    (`uid`, `status`),
  KEY `idx_global_character_project`       (`project_id`),
  KEY `idx_global_character_lang_slug`     (`lang`, `slug`),
  KEY `idx_global_character_lora_model`    (`lora_model_id`),
  KEY `idx_global_character_source_thread` (`source_thread_id`),
  KEY `idx_global_character_deleted_at`    (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- w_global_character_reference stores reference images used to
-- maintain visual consistency across renders for the parent
-- character. Sibling of w_global_brand_reference +
-- w_global_director_style_reference; character-specific
-- reference_type taxonomy.
CREATE TABLE IF NOT EXISTS `w_global_character_reference` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`        datetime DEFAULT NULL,

  `character_id`      bigint unsigned NOT NULL,
  `uid`               int NOT NULL DEFAULT 0,
  `image_url`         varchar(2048) NOT NULL DEFAULT '',
  -- Character reference types:
  --   face / body / outfit / expression / pose
  `reference_type`    varchar(32) NOT NULL DEFAULT 'face',
  `label`             varchar(80) NOT NULL DEFAULT '',
  `sort_order`        int NOT NULL DEFAULT 0,
  -- metadata can carry e.g. boundingBox / confidence from the
  -- calibrator, or per-reference notes.
  `metadata`          json DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_character_ref`         (`character_id`),
  KEY `idx_global_character_ref_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
