-- w_global_brand + w_global_brand_reference — Sprint-E 2/8
-- platform-level brand asset tables. Sprint-D had built
-- `asset_library.Descriptor` as an abstraction inside the
-- workagent module; Sprint-E promotes the abstraction to platform
-- scope, so brand identity is reachable from canvas tools, TTS,
-- video-ad surfaces, and any future generator without going
-- through the workagent module.
--
-- Schema mirrors w_global_character's design contract (the
-- platform's existing canonical asset table): uid + project_id +
-- lang (i18n fan-out for system rows), soft-delete, flat Status
-- int8 + Confirmed bool + ConfirmedAt for the M4 Vocalize
-- lifecycle, 6-layer identity_anchors + negative_anchors +
-- anchors_version + appearance_hash machinery for staleness
-- detection, source traceability, and prompt scaffolding.
--
-- Platform context: ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md.
-- See also: model/brand.go (canonical schema source-of-truth).

CREATE TABLE IF NOT EXISTS `w_global_brand` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,

  -- Owner + scoping. uid is the canonical owner; project_id is
  -- NULL for the user's global brand library, non-NULL for a
  -- project-scoped brand. team_id (Sprint-C reservation) groups
  -- brands shared inside a team.
  `uid`                   int NOT NULL DEFAULT 0,
  `project_id`            bigint unsigned DEFAULT NULL,
  `team_id`               bigint unsigned DEFAULT NULL,

  -- i18n fan-out. System rows (uid=0, project_id IS NULL) get one
  -- row per (slug, lang); user/project rows carry lang as
  -- authoring-locale metadata only.
  `lang`                  varchar(16) NOT NULL DEFAULT 'en',

  -- Identity.
  `name`                  varchar(120) NOT NULL DEFAULT '',
  `slug`                  varchar(160) NOT NULL DEFAULT '',

  -- M4 brand-spec sections. Schema-flexible JSON; the composer's
  -- <brand-spec> XML walks each populated section into a YAML-ish
  -- line. Empty / null / {} sections drop cleanly so partial
  -- extractions don't pollute the prompt.
  `colors`                json DEFAULT NULL,  -- §1 — palette tokens, hex+OKLch
  `typography`            json DEFAULT NULL,  -- §2 — font_stack {display, body, mono}
  `spacing`               json DEFAULT NULL,  -- §3 — base unit, scale, breakpoints
  `layout`                json DEFAULT NULL,  -- §4 — grid, density, alignment posture
  `components`            json DEFAULT NULL,  -- §5 — button/card/input variants
  `motion`                json DEFAULT NULL,  -- §6 — easing, duration, transition palette
  `voice`                 json DEFAULT NULL,  -- §7 — tone descriptors, do/don't lexicon

  -- Staleness machinery (parity with w_character).
  --
  -- identity_anchors is the structured 6-layer brand consistency
  -- anchor (palette / type / spacing / layout / motion / voice).
  -- Populated by the AI calibrator. Consumed by image / video
  -- prompt assembly to hold cross-render brand consistency.
  --
  -- appearance_hash is a 16-hex-char digest over visually-affecting
  -- fields (colors + typography + identity_anchors +
  -- negative_anchors). Identity-only edits (e.g. name / slug)
  -- leave the hash unchanged so the stale-render detector doesn't
  -- flag visually-correct renders on every metadata tweak.
  `identity_anchors`      json DEFAULT NULL,
  `negative_anchors`      json DEFAULT NULL,
  `anchors_version`       int NOT NULL DEFAULT 1,
  `appearance_hash`       char(16) NOT NULL DEFAULT '',
  `calibrated_at`         datetime DEFAULT NULL,

  -- Prompt scaffolding — appended verbatim to downstream prompts.
  `prompt_suffix`         text,
  `negative_prompt`       text,

  -- Source traceability.
  `source_kind`           varchar(32) NOT NULL DEFAULT 'manual',
  `source_url`            varchar(2048) NOT NULL DEFAULT '',
  `source_thread_id`      bigint unsigned DEFAULT NULL,
  `raw_spec_md`           mediumtext,

  -- Lifecycle. Status int8=1 means active (1=active is the
  -- canvas-era convention; matches w_character's pattern).
  -- Confirmed bool + ConfirmedAt back the M4 Vocalize step:
  -- canvas-created brands default Confirmed=1 via the DEFAULT
  -- below; workagent draft extractions write Confirmed=0
  -- explicitly via Select("*") to survive GORM's zero-value-skip.
  `status`                tinyint NOT NULL DEFAULT 1,
  `confirmed`             tinyint(1) NOT NULL DEFAULT 1,
  `confirmed_at`          datetime DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_brand_uid_status`    (`uid`, `status`),
  KEY `idx_global_brand_project`       (`project_id`),
  KEY `idx_global_brand_team`          (`team_id`),
  KEY `idx_global_brand_lang_slug`     (`lang`, `slug`),
  KEY `idx_global_brand_source_thread` (`source_thread_id`),
  KEY `idx_global_brand_deleted_at`    (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- w_global_brand_reference — sibling of w_global_character_reference. Stores
-- reference imagery (logos / mood boards / screenshots / pattern
-- swatches) for a brand. Per-kind reference_type taxonomy lets
-- the asset library UI render kind-specific affordances.
CREATE TABLE IF NOT EXISTS `w_global_brand_reference` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`        datetime DEFAULT NULL,

  `brand_id`          bigint unsigned NOT NULL,
  `uid`               int NOT NULL DEFAULT 0,
  `image_url`         varchar(2048) NOT NULL DEFAULT '',
  -- Brand reference types: logo / mood_board / screenshot / pattern
  `reference_type`    varchar(32) NOT NULL DEFAULT 'mood_board',
  `label`             varchar(80) NOT NULL DEFAULT '',
  `sort_order`        int NOT NULL DEFAULT 0,
  `metadata`          json DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_brand_ref`         (`brand_id`),
  KEY `idx_global_brand_ref_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
