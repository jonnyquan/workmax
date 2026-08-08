-- Vertical-prefix sweep · Round 2 (B 类：伪通用 → drama 前缀)
--
-- 命名规则：单一 solution 写入的表必须带
-- `w_<solution>_*` 前缀。下面 3 张表 model 上都带 `solution_type`
-- 字段，但实际写入方都只有 short-drama —— discriminator 没真生效，
-- 名字伪通用。规则要求修齐：
--
--   w_episode     只 short-drama 写
--   w_script      只 short-drama 写
--   w_panel_shot  只 short-drama 写
--
-- `solution_type` 列保留，作为漫剧 / 视频广告 / 电商方案未来接入的
-- discriminator 锚点；当前所有行都是 'short_drama'。这条 migration
-- 不动列，只动表名。
--
-- 同步改动（详见 PR）：
--   server/model/{drama_episode,script,panel_shot}.go
--     struct rename：Episode → DramaEpisode, Script → DramaScript,
--                    PanelShot → DramaPanelShot
--     状态常量同步加 Drama 前缀
--     project.go 里 Episode 拆出到独立文件 drama_episode.go
--   server/api/pro/tools/* + tests        model.X 全仓替换 + raw SQL JOIN/INSERT 改名
--   server/utils/testutil/testdb.go       CREATE TABLE 改名
--   相关表名约定与迁移说明同步
--
-- 前端 URL `/tools/short-drama/[id]/episodes/[episodeId]/...` 保持不变 ——
-- 这条 migration 只是后端的表名整理，不影响 HTTP 路由。
--
-- RENAME TABLE 是 atomic 元数据操作，零数据丢失、表别名 / FK 透明。
-- 与 20260504_refactor_project_tables_to_generic.sql / 20260552_rename_*
-- 同款姿势。这条等于把 20260504 那次"短剧表去前缀"的方向改回来 ——
-- 当时假设漫剧/广告会很快共用同表，现实里只有 short-drama 在写。

RENAME TABLE
  `w_episode`    TO `w_drama_episode`,
  `w_script`     TO `w_drama_script`,
  `w_panel_shot` TO `w_drama_panel_shot`;
