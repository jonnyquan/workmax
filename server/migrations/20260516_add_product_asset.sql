-- Ecommerce foundation (step 7 in solutions-overview §6.4).
-- Adds w_product_asset + w_product_image.
--
-- ProductAsset is the SKU-level counterpart to Character/Location/
-- BrandProfile — the thing a video is ABOUT. Each project with
-- solution_type='ecommerce' points at many Products; each Product
-- becomes the subject of one or more ecommerce videos.
--
-- Price stored as integer cents (price_cents) not float — floating-
-- point price math is a classic source of off-by-1¢ bugs, and our
-- export renderer overlays prices verbatim so the raw cents value
-- is what ships.
--
-- specs_json / selling_points_json / tags_json are kept as flexible
-- JSON so the URL-import parser (Phase 2) can stuff platform-
-- specific fields (Taobao SKU variants, Amazon ASINs) without a
-- follow-up migration. Shape:
--   specs_json         — { "color": "blue", "size": "L", ... }
--   selling_points_json — [{category, label, detail, rank}]
--   tags_json          — ["bestseller", "new-arrival", ...]

CREATE TABLE `w_product_asset` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL DEFAULT '' COMMENT '对外UUID',
  `uid` int NOT NULL COMMENT '所有者',
  `project_id` bigint unsigned DEFAULT NULL COMMENT 'Ecommerce Campaign,NULL表示全局库',
  `title` varchar(300) NOT NULL DEFAULT '' COMMENT '商品名',
  `source` varchar(32) NOT NULL DEFAULT 'manual' COMMENT '来源 manual/url/csv/api',
  `source_url` varchar(2048) NOT NULL DEFAULT '' COMMENT '原始商品链接',
  `category` varchar(64) NOT NULL DEFAULT '' COMMENT '品类 3c/fashion/beauty/...',
  `description` text COMMENT '详细描述',
  `price_cents` bigint NOT NULL DEFAULT 0 COMMENT '价格(分),避免浮点误差',
  `currency` varchar(8) NOT NULL DEFAULT 'CNY' COMMENT 'ISO 4217',
  `discount_price_cents` bigint NOT NULL DEFAULT 0 COMMENT '折扣价(分),0=无折扣',
  `specs_json` json DEFAULT NULL COMMENT '规格属性 {color, size, ...}',
  `selling_points_json` json DEFAULT NULL COMMENT 'AI提炼的卖点 [{category,label,detail,rank}]',
  `tags_json` json DEFAULT NULL COMMENT '标签数组',
  `analysis_version` int NOT NULL DEFAULT 0 COMMENT 'AI卖点提炼次数',
  `analyzed_at` datetime DEFAULT NULL COMMENT '最后一次AI分析时间',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=草稿 2=已分析 3=归档',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_product_uuid` (`uuid`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_project` (`project_id`),
  KEY `idx_category` (`category`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='电商商品资产 — SKU级视频素材来源';

CREATE TABLE `w_product_image` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `product_id` bigint unsigned NOT NULL,
  `uid` int NOT NULL,
  `image_url` varchar(2048) NOT NULL DEFAULT '',
  `role` varchar(32) NOT NULL DEFAULT 'main' COMMENT 'main/gallery/detail/lifestyle/spec',
  `label` varchar(80) NOT NULL DEFAULT '',
  `sort_order` int NOT NULL DEFAULT 0,
  `width` int NOT NULL DEFAULT 0,
  `height` int NOT NULL DEFAULT 0,
  `metadata` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_product` (`product_id`),
  KEY `idx_role` (`role`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='商品图片 main/gallery/detail/lifestyle/spec';
