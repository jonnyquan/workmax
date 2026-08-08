-- R3-d · Split global cross-solution resource tables per solution.
--
-- 用户决策走 Path B 到底 —— 全局资产库（character / character_reference /
-- character_relationship / location / location_reference / director_preset）
-- 按方案物理拆分。
--
-- 当前真实写入方只有 short-drama，本 migration 只把 6 张共享表 RENAME
-- 成 drama-prefix（数据天然带过去）；comic / ad / ecom 在各自方案
-- 真正接入资产库时再各自建（precedent: w_comic_character /
-- w_ad_character / w_ecom_character 等）。
--
-- project_id 列保留 —— R3-a 拆 w_project 后，drama 的资产 project_id
-- 指向 w_drama_project.id，单一 solution 表 / 不再 polymorphic 模糊，
-- "项目专属角色" 功能保留。
--
-- 同步改动（详见 PR）:
--   server/model/character.go → drama_character.go (Character →
--     DramaCharacter, +Reference / +Relationship 同名 prefix)
--   server/model/location.go → drama_location.go
--   server/model/director_preset.go → drama_director_preset.go
--   ~30 个 .go 文件 model.X 引用改 model.DramaX
--   常量 CharacterRoleType* / CharacterSource* / CharacterStatus* /
--     CharacterRelation* / LocationType* / LocationSource* /
--     LocationStatus* / DirectorPresetStatus* / DirectorPresetSystemUID
--     全部 prefix 加 Drama
--   server/utils/testutil/testdb.go SQLite schema 同步
--
-- canvas mention_resolver 改读 w_drama_character —— 短期没问题，因为
-- 只有 drama 用 character；其他方案接入时再 UNION 4 张表（届时
-- 写一个 readAllUserCharacters helper 集中处理）。

RENAME TABLE
  `w_character`              TO `w_drama_character`,
  `w_character_reference`    TO `w_drama_character_reference`,
  `w_character_relationship` TO `w_drama_character_relationship`,
  `w_location`               TO `w_drama_location`,
  `w_location_reference`     TO `w_drama_location_reference`,
  `w_director_preset`        TO `w_drama_director_preset`;
