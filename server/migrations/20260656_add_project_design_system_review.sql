ALTER TABLE w_workagent_project_design_system
  ADD COLUMN reviewed_by INT NOT NULL DEFAULT 0,
  ADD COLUMN reviewed_at DATETIME NULL,
  ADD COLUMN review_note TEXT NULL;

CREATE INDEX idx_w_workagent_project_design_system_reviewed_by
  ON w_workagent_project_design_system (reviewed_by);
