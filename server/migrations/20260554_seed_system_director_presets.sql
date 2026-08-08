-- Seeds w_director_preset with system-owned presets (uid=0, project_id=NULL).
--
-- Why DB-side instead of Go constants:
--   The 10 built-ins in service/tools/director_presets.go are short-drama
--   leaning and intentionally tight. This migration adds a broader, vertical-
--   tagged catalogue (drama extras + manga + ad + ecommerce) that an admin
--   can later edit/extend without a code release. The handler merges these
--   under the same "Builtin" badge so the picker shows one flat library.
--
-- Scope marker: uid=0 + project_id=NULL = system row. Visible to all users
-- via the OR-branch added to the List handler in this PR. User/project rows
-- with the same slug shadow these (override semantics, same as Go built-ins).
--
-- Slug namespace: sys-{vertical}-{handle}. The sys- prefix makes it obvious
-- in the DB which rows are seeded vs hand-authored, and guarantees no
-- collision with the Go built-in slugs.
--
-- Idempotency: per-row WHERE NOT EXISTS keyed on (slug, uid=0, project_id IS
-- NULL). Re-running the migration is a no-op; adding a new preset later just
-- means appending another block — already-seeded rows skip themselves.
--
-- Tag shape: {"labels": [...]}. Matches tagsToJSONMap / tagsFromJSONMap so
-- the wire layer round-trips cleanly without a code change.
--
-- i18n contract: each row's `name` and `description` are the raw fallback
-- the UI renders when no localized message is found. The frontend looks up
-- `directorPresets.<slug>.name` and `directorPresets.<slug>.description` in
-- the active locale's messages file (web/messages/{locale}.json) before
-- falling back to these literals — see the localizeName helper in
-- web/components/short-drama/storyboard/director-preset-picker.tsx and the
-- prior-art pattern in app/[locale]/(tools)/tools/short-drama/templates/
-- page.tsx. When adding a new sys-* preset here, add the matching
-- directorPresets.<slug>.{name,description} entries in en.json AND zh.json
-- (other 16 locales fall through to the literal until translated). The
-- per-row WHERE NOT EXISTS guard means re-running this migration won't
-- overwrite literals if you tweak them in place.

-- ─── Drama extras (5) ────────────────────────────────────────────────
INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '闪回淡入', 'sys-drama-flashback-fade',
  'Soft fade-in evoking memory or flashback. Use to bridge present and past beats.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '夜戏低照度', 'sys-drama-low-light-night',
  'Night-interior shot with practical light only. Use for tense or intimate beats after dark.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '对峙双人', 'sys-drama-confrontation-twoshot',
  'Symmetrical two-shot for confrontation. Both faces readable, tension in the gap between them.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '仰角威压', 'sys-drama-power-low-angle',
  'Low-angle medium emphasising dominance or threat of the subject.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '第一人称手持', 'sys-drama-pov-handheld',
  'Handheld POV from the protagonist''s vantage. Use for immersion or chase beats.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `uid` = 0 AND `project_id` IS NULL
);

-- ─── Manga / IP video (5) ────────────────────────────────────────────
INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '漫格放大入场', 'sys-manga-panel-zoom',
  'Manga-panel zoom-in: start framed like a comic panel, push past the borders into live motion.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '拟声速度线', 'sys-manga-speedline-burst',
  'Action shot with radial speed lines bursting from the subject. Manga-style impact emphasis.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '动作定格', 'sys-manga-action-freeze',
  'Hold on the apex of an action — the manga "splash page" moment. Brief still before continuing.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '分格并置', 'sys-manga-split-screen',
  'Split-screen showing two simultaneous beats — manga-style multi-panel layout in motion.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '水墨过场', 'sys-manga-ink-transition',
  'Ink-wash transition between scenes. Use as a stylised wipe between manga sequences.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `uid` = 0 AND `project_id` IS NULL
);

-- ─── Video ad (5) ────────────────────────────────────────────────────
INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '产品 hero 旋转', 'sys-ad-hero-product-spin',
  'Hero product on plain backdrop with slow rotational reveal. Default for product launch ads.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'UGC 自拍口播', 'sys-ad-ugc-selfie',
  'Authentic UGC look — handheld selfie framing, casual setting, talent speaking to camera.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '痛点对比剪', 'sys-ad-pain-point-cut',
  'Two-beat cut juxtaposing pain (before) and relief (after). Common ad hook structure.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'CTA 收口', 'sys-ad-cta-end-card',
  'End-card composition for the CTA / brand mark. Hold long enough to read.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '对比前后', 'sys-ad-before-after-split',
  'Single-frame before/after split. No cut — split-screen comparison in one take.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `uid` = 0 AND `project_id` IS NULL
);

-- ─── Ecommerce video (5) ─────────────────────────────────────────────
INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '商品 360°', 'sys-ecom-product-360',
  'Full 360° turntable rotation of the product. Standard ecommerce listing video.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '细节微距', 'sys-ecom-detail-macro',
  'Macro insert highlighting material, stitching, or texture. Use to convey craft and quality.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '使用场景', 'sys-ecom-usage-scene',
  'Lifestyle shot showing the product in real use. Connects feature to context.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '卖点字幕浮现', 'sys-ecom-feature-callout',
  'Feature callout — product on screen with text labels animating in to call out selling points.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `uid` = 0 AND `project_id` IS NULL
);

INSERT INTO `w_director_preset`
  (`uid`, `project_id`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, '开箱第一视角', 'sys-ecom-unboxing',
  'First-person unboxing angle — talent''s hands opening packaging, package centred in frame.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `uid` = 0 AND `project_id` IS NULL
);
