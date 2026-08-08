-- Adds voice_preset to w_character for TTS voiceover assignment.
--
-- Holds a provider-agnostic preset ID (e.g. "neutral", "female-warm")
-- resolved at render time via service/tts/voices.go#LookupProviderVoice.
-- Nullable — characters without an assigned voice fall back to the
-- project default at export time, so existing rows stay functional
-- without a backfill.

ALTER TABLE `w_character`
  ADD COLUMN `voice_preset` VARCHAR(64) DEFAULT NULL
    COMMENT 'TTS voice preset ID; resolved via service/tts/voices.go';
