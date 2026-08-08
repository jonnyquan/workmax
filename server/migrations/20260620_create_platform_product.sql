-- w_global_product + w_global_product_reference — P1 #5 platform
-- product asset tables. Promotes Product to the 4th asset_library
-- kind alongside Brand / Character / DirectorStyle.
--
-- Schema mirrors w_global_brand's contract (the asset_library's
-- canonical platform-level template) so the Descriptor adapter
-- stays thin: uid + project_id + lang scoping, soft-delete, flat
-- Status int8 + Confirmed bool + ConfirmedAt for the M4 Vocalize
-- lifecycle, 6-layer identity_anchors + negative_anchors +
-- anchors_version + appearance_hash staleness machinery, source
-- traceability, prompt scaffolding.
--
-- Product-specific deltas vs Brand:
--   - sku + category as flat indexed columns (frequently queried
--     by the picker)
--   - description text (selling points body)
--   - specs / visual_guidance / target_audience JSON sections
--     replace Brand's M4 protocol sections (colors / typography /
--     spacing / layout / components / motion / voice)
--   - reference_type taxonomy: product_shot / lifestyle / detail /
--     packaging / swatch (vs brand's logo / mood_board /
--     screenshot / pattern)
--
-- See also model/product.go (Go source-of-truth).

CREATE TABLE IF NOT EXISTS `w_global_product` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,

  -- Owner + scoping.
  `uid`                   int NOT NULL DEFAULT 0,
  `project_id`            bigint unsigned DEFAULT NULL,
  `team_id`               bigint unsigned DEFAULT NULL,

  -- i18n fan-out (same contract as brand).
  `lang`                  varchar(16) NOT NULL DEFAULT 'en',

  -- Identity.
  `name`                  varchar(160) NOT NULL DEFAULT '',
  `slug`                  varchar(200) NOT NULL DEFAULT '',

  -- Frequently-queried identity. Flat columns instead of JSON so
  -- the picker can WHERE / index against them directly.
  `sku`                   varchar(120) NOT NULL DEFAULT '',
  `category`              varchar(80)  NOT NULL DEFAULT '',
  `description`           text,

  -- Product-specific JSON sections.
  `specs`                 json DEFAULT NULL,  -- variants / dimensions / materials / weight
  `visual_guidance`       json DEFAULT NULL,  -- photography direction
  `target_audience`       json DEFAULT NULL,  -- per-product audience

  -- Staleness machinery (parity with w_global_brand).
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

  -- Lifecycle. Status int8=1 active; Confirmed defaults 1 so
  -- canvas-created products auto-confirm. Workagent draft
  -- extractions write Confirmed=0 via Select("*").Create().
  `status`                tinyint NOT NULL DEFAULT 1,
  `confirmed`             tinyint(1) NOT NULL DEFAULT 1,
  `confirmed_at`          datetime DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_product_uid_status`    (`uid`, `status`),
  KEY `idx_global_product_project`       (`project_id`),
  KEY `idx_global_product_team`          (`team_id`),
  KEY `idx_global_product_lang_slug`     (`lang`, `slug`),
  KEY `idx_global_product_sku`           (`sku`),
  KEY `idx_global_product_category`      (`category`),
  KEY `idx_global_product_source_thread` (`source_thread_id`),
  KEY `idx_global_product_deleted_at`    (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_global_product_reference` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`        datetime DEFAULT NULL,

  `product_id`        bigint unsigned NOT NULL,
  `uid`               int NOT NULL DEFAULT 0,
  `image_url`         varchar(2048) NOT NULL DEFAULT '',
  -- Product reference types: product_shot / lifestyle / detail / packaging / swatch
  `reference_type`    varchar(32) NOT NULL DEFAULT 'product_shot',
  `label`             varchar(80) NOT NULL DEFAULT '',
  `sort_order`        int NOT NULL DEFAULT 0,
  `metadata`          json DEFAULT NULL,

  PRIMARY KEY (`id`),
  KEY `idx_global_product_ref`         (`product_id`),
  KEY `idx_global_product_ref_deleted` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
