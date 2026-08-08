-- Recipe v2.0 — schema slice (no UI, infrastructure only).
--
-- Two columns land in two existing tables. No new tables: the whole
-- v2 surface rides on (a) a single JSON column on
-- the recipe catalog row and (b) a single varchar tag on the
-- message row. Pipeline state is *derived* by reading messages
-- back through the registered Recipe.Config.Stages — no state
-- machine table, no "current_stage" pointer.
--
-- --------------------------------------------------------------------
-- 1) w_global_recipe.config_json — v2 configuration carrier.
--
-- The current production registry is in-process (Go init()
-- registrations under `service/recipe/recipes/`); this column is
-- the forward-compatible storage shape for the day recipes need
-- to be DB-backed (e.g. customer-authored recipes — design doc
-- v2.3). Until then it stays NULL on every row and the runtime
-- reads Descriptor.Config from the in-memory registry.
--
-- Why we ship the column NOW instead of waiting for v2.3: adding
-- a JSON column on a forecast-empty table is cheap, but altering
-- it later when the table has live recipes-from-DB rows would
-- require a backfill story. The cost asymmetry says "land the
-- column now, populate later".
ALTER TABLE `w_global_recipe`
  ADD COLUMN `config_json` json DEFAULT NULL
    COMMENT 'Recipe v2 config (stages / assetHints / uiHints). NULL = v1 recipe, shell takes fallback path.';

-- --------------------------------------------------------------------
-- 2) w_workagent_message.stage_tag — production-tool pipeline tag.
--
-- Written by AppendToolOutputMarker (server/service/tools/workagent/
-- production_tools.go) when the active recipe declares Stages that
-- claim the firing tool. Read by the pipeline-derivation endpoint
-- (GET /api/work-agent/chat/conversation/:id/pipeline) to project
-- the latest message per (thread, stage) tuple back to the FE.
--
-- Empty string (default) means "this message is not stage-tagged":
-- either no recipe is bound, the bound recipe is v1 (Config==NULL),
-- or the firing tool isn't claimed by any of the v2 stages.
-- That posture matches user_rating's "0 = unrated" — equality-
-- filterable in the index, no IS NULL contortions.
--
-- Length 16 is generous for stage IDs ("script", "shotboard",
-- "render", "post" are the kinds we expect). The column index is
-- (thread_id, stage_tag, id) so "latest message at stage X on
-- thread Y" — the pipeline endpoint's only query — is a single
-- range scan rather than a thread sweep.
ALTER TABLE `w_workagent_message`
  ADD COLUMN `stage_tag` varchar(16) NOT NULL DEFAULT ''
    COMMENT 'Recipe v2 pipeline stage id, derived at tool completion. Empty = no stage tag.',
  ADD KEY `idx_message_thread_stage` (`thread_id`, `stage_tag`, `id`);
