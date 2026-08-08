-- SD-S2.4: cross-project character family.
--
-- Pre-S2.4: drama_character is project-scoped. Reusing "王总" across
-- a 5-season IP series means re-creating + re-calibrating the
-- character five times. Anchor drift between rebuilds is inevitable
-- — the same actor ends up looking subtly different across seasons.
--
-- Fix: a per-user family table that holds the canonical anchor
-- definition. Project-level character rows can link to a family via
-- family_id; importing a family into a new project mints a fresh
-- character row pre-loaded with the family's anchors. Anchor changes
-- in the family don't auto-propagate (project-level may have been
-- intentionally tweaked) — the user pulls in updates explicitly via
-- "sync from family".
--
-- Single-lang for Phase 1 — i18n fan-out (per-locale family names)
-- can come later via the same (slug, lang) pattern used by templates.

CREATE TABLE `w_drama_character_family` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `uid`                 INT             NOT NULL DEFAULT 0  COMMENT 'owner; families are personal — sharing rides on project-level character rows linking to the family',
  `slug`                VARCHAR(120)    NOT NULL DEFAULT '' COMMENT 'unique per uid; URL-safe identifier',
  `name`                VARCHAR(120)    NOT NULL DEFAULT '' COMMENT 'display name, e.g. "王总"',
  `description`         VARCHAR(500)    NOT NULL DEFAULT '' COMMENT 'free-text role / background notes shared across projects',
  `default_anchors_json` JSON           DEFAULT NULL        COMMENT 'canonical 6-layer identityAnchors blob; project-level rows copy this on import',
  `default_negative_json` JSON          DEFAULT NULL        COMMENT 'canonical negativeAnchors blob (avoid + styleExclusions)',
  `default_voice_preset` VARCHAR(64)    NOT NULL DEFAULT '' COMMENT 'TTS voice preset id, e.g. "female-warm"',
  `default_avatar_url`  VARCHAR(2048)   NOT NULL DEFAULT '' COMMENT 'representative avatar URL',
  `gender`              VARCHAR(32)     NOT NULL DEFAULT '',
  `age_range`           VARCHAR(32)     NOT NULL DEFAULT '',
  `version`             INT             NOT NULL DEFAULT 1  COMMENT 'monotonic counter; bumped on every anchor edit so linked characters can detect "family has new defaults"',
  `status`              TINYINT         NOT NULL DEFAULT 1  COMMENT '1=active 2=archived',
  `created_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`          DATETIME        DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_uid_slug` (`uid`, `slug`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='短剧跨项目角色族 — 同 IP 续作复用人设的锚点';

ALTER TABLE `w_drama_character`
  ADD COLUMN `family_id` BIGINT UNSIGNED DEFAULT NULL
    COMMENT 'optional link to w_drama_character_family.id; NULL = standalone character',
  ADD INDEX `idx_family` (`family_id`);
