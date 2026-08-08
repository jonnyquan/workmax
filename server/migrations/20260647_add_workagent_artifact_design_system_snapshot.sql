-- Freeze the selected design system on each artifact version so historical
-- artifacts do not drift when the thread-level visual direction changes.

ALTER TABLE `w_workagent_artifact`
  ADD COLUMN `design_system_basename` varchar(320) NOT NULL DEFAULT '' AFTER `html_preview_diagnostics`,
  ADD COLUMN `design_system_title` varchar(255) NOT NULL DEFAULT '' AFTER `design_system_basename`,
  ADD COLUMN `design_system_derived_from` varchar(255) NOT NULL DEFAULT '' AFTER `design_system_title`,
  ADD KEY `idx_workagent_artifact_design_system` (`uid`, `design_system_basename`);
