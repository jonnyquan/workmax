-- P1 #4 slice 2 — recipe binding on chat_thread.
--
-- A Recipe is the Plugin-shaped descriptor (service/recipe) that
-- carries scene-specific system-prompt direction. When a thread
-- is bound to a recipe, the preflight composer injects the
-- recipe's SystemPrompt as a <recipe-context> block so the agent
-- reads scene-specific direction (e.g. "5-second vertical social
-- ad, kinetic-modern director style, 9:16, hook in first frame")
-- before generating.
--
-- Why a varchar column instead of a foreign-key to a recipes
-- table: recipes are CODE registrations (service/recipe/recipes/
-- *.go init() blocks), not DB rows. The string here is the
-- registry key the backend looks up at preflight time. Unknown
-- IDs degrade gracefully (loadRecipeContext returns empty, no
-- injection) — same posture as a missing discovery_form_result.
--
-- NULL / empty default means "no recipe bound" — the existing
-- threads continue to operate as before this column existed.

ALTER TABLE `w_workagent_thread`
  ADD COLUMN `recipe_id` varchar(64) NOT NULL DEFAULT ''
    COMMENT 'recipe registry id (service/recipe); empty = no recipe bound';
