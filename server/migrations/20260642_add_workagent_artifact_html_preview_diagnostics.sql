-- Persist browser-runtime diagnostics emitted by sandboxed HTML artifact previews.
-- Static validation remains computed from the file content; this column records
-- client-observed issues such as resource load errors for later revise/export prompts.

ALTER TABLE `w_workagent_artifact`
  ADD COLUMN `html_preview_diagnostics` text AFTER `comparison_decision`;
