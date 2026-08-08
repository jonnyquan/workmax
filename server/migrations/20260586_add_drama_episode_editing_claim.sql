-- SD-S2.1: episode-level editing claim (soft lock).
--
-- Pre-S2.1: two team members opening the same script / storyboard
-- editor would silently overwrite each other's edits — last write
-- wins, no UI signal that someone else was working there.
--
-- Soft lock: when a user enters the edit surface the frontend stamps
-- editing_by_uid + editing_expires_at, and renews via a 60-second
-- heartbeat. Default expiry 5 minutes — a tab abandoned mid-session
-- releases the lock without manual intervention.
--
-- "Soft" = writes from a non-claim-holder are rejected at the API
-- layer with a clear 409 (the frontend offers a "take over" button),
-- but a force-takeover always succeeds. No DB-level constraint.

ALTER TABLE `w_drama_episode`
  ADD COLUMN `editing_by_uid` INT NOT NULL DEFAULT 0
    COMMENT 'uid currently editing this episode; 0 = no claim',
  ADD COLUMN `editing_expires_at` DATETIME DEFAULT NULL
    COMMENT 'when the editing claim expires; 60s heartbeat from the client extends it';
