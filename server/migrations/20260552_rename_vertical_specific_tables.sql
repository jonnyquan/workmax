-- Vertical-prefix sweep · Round 1 (C 类：solution-only 表前缀对齐)
--
-- 命名规则：单一 solution 写入的表必须带
-- `w_<solution>_*` 前缀。本 migration 修齐 5 张当前命名缺失/不齐的
-- 垂直方案专属表：
--
--   w_brand_profile / w_brand_reference   只 video-ad 写
--   w_product_asset / w_product_image     只 ecommerce 写
--   w_ecommerce_video                     只 ecommerce 写（前缀冗余成
--                                         "ecommerce"，统一为 ecom）
--
-- Solution 简称约定：drama / ad / comic / ecom（4 字母级别长度对齐）。
--
-- 同步改动（详见 PR）：
--   server/model/{brand_profile,product_asset,ecommerce_video}.go
--     struct rename + TableName() 新值
--   server/utils/testutil/testdb.go              CREATE TABLE 改名
--   server/initialize/gorm.go                    AutoMigrate struct 名更新
--   server/api/pro/tools/* + tests               model.X 全仓替换
--   相关表名约定与迁移说明同步
--
-- RENAME TABLE 是 atomic 元数据操作，零数据丢失、表别名 / FK 透明，
-- 与 20260504_refactor_project_tables_to_generic.sql 同款姿势。

RENAME TABLE
  `w_brand_profile`   TO `w_ad_brand_profile`,
  `w_brand_reference` TO `w_ad_brand_reference`,
  `w_product_asset`   TO `w_ecom_product_asset`,
  `w_product_image`   TO `w_ecom_product_image`,
  `w_ecommerce_video` TO `w_ecom_video`;
