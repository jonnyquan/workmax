-- SD-P2-5: ecommerce locale-aware prompts.
--
-- The EcommerceScriptSystemPromptFor accessor exists since P2-3
-- (commit 7ce64644) and the zh prompt file is in place at
-- prompts/short-drama/zh/ecommerce-script.md, but the handler
-- couldn't pass a real lang because w_ecom_project had no column
-- to store one. This migration closes that gap.

ALTER TABLE `w_ecom_project`
  ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT 'en' COMMENT 'BCP47 lang tag — drives LLM prompt selection';
