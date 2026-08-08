-- SD-FU5: add lang column to w_drama_project so every project carries
-- a BCP47 locale tag that drives LLM prompt selection.
-- DEFAULT 'en' means all pre-migration rows silently fall back to the
-- existing English prompts — no data migration required.
ALTER TABLE `w_drama_project`
  ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT 'en'
  COMMENT 'BCP47 lang tag — drives LLM prompt selection (outline/script/storyboard)';
