-- One-level undo for AI regenerations. Pattern borrowed from
-- waoowaoo (previousImageUrl / previousDescription fields on
-- CharacterAppearance + LocationImage). Single best UX-per-LOC
-- improvement across the competitor audit.
--
-- On a successful regenerate the handler copies the OLD value into
-- previous_* before writing the new value. A single "Undo" button
-- in the UI swaps them back. Two consecutive undos do nothing — this
-- is deliberately one-level, not a version log, so the schema cost
-- stays at two columns instead of a new table.
--
-- Character: avatar is the most-regenerated field, so it gets the
-- undo slot. Identity anchors rarely regenerate and are already
-- surfaceable via audit queries, so we don't invest in snapshotting
-- them here.
--
-- Location: description is what the calibrator rewrites, so that
-- carries the snapshot. Identity anchors follow the same "skip" rule
-- as characters.

ALTER TABLE `w_character`
  ADD COLUMN `previous_avatar_image_url` varchar(2048) NOT NULL DEFAULT ''
    COMMENT '上一次AI生成前的头像URL,用于一键撤销';

ALTER TABLE `w_location`
  ADD COLUMN `previous_description` text
    COMMENT '上一次AI生成前的场景描述,用于一键撤销';
