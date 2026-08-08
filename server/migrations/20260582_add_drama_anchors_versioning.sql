-- SD-S0.2: anchor versioning + per-shot anchor snapshots.
--
-- Problem before this migration: the panel-prompt resolver merged the
-- *current* character/location anchors at render time, but stored
-- nothing about which version of those anchors actually fired. If a user
-- re-calibrated characters mid-render, half the panel videos used the
-- old anchors and half the new — and the system had no way to detect
-- which were stale.
--
-- Fix: bump a monotonic counter on each character/location whenever its
-- anchors change, and snapshot the full anchor blob into the panel-shot
-- row at the moment a video is generated. Later (S1.2) the
-- stale-shot detector compares the snapshot's version map against the
-- current counter and surfaces "shots rendered against stale anchors".

ALTER TABLE `w_drama_character`
  ADD COLUMN `anchors_version` INT NOT NULL DEFAULT 1
    COMMENT 'monotonic counter; +1 on identity_anchors / negative_anchors / appearance change';

ALTER TABLE `w_drama_location`
  ADD COLUMN `anchors_version` INT NOT NULL DEFAULT 1
    COMMENT 'monotonic counter; +1 on identity_anchors_json / negative_anchors_json / description change';

ALTER TABLE `w_drama_panel_shot`
  ADD COLUMN `anchors_snapshot_json` JSON DEFAULT NULL
    COMMENT 'snapshot of character + location anchors used at render time. Shape: {"characters":{"<id>":{version,identityAnchors,negativeAnchors,name}}, "locations":{...}}';
