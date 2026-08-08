-- Adds w_generation_record.origin, the product-surface discriminator
-- used by Canvas × video-generator service 合流. Legacy rows stay
-- NULL — new writes always stamp 'canvas' / 'video-gen' / 'image-gen'.

ALTER TABLE `w_generation_record`
    ADD COLUMN `origin` varchar(32) DEFAULT NULL COMMENT '产品来源: canvas / video-gen / image-gen' AFTER `batch_id`,
    ADD INDEX `idx_origin` (`origin`);
