-- Adds batch job engine — step 6 in solutions-overview §6.4.
-- Serves ecommerce (100 SKUs per workflow), video-ad (variant
-- matrix), and future social-short (multi-platform publication).
-- Pattern borrowed from waoowaoo's BullMQ-backed ledger; stripped
-- down to what our sync workers need today.
--
-- Two tables:
--
--   w_batch_job  — the named batch operation. One row per
--                  "submit" action. Holds total/completed/failed
--                  counters so the frontend renders a progress bar
--                  without scanning every item row.
--
--   w_batch_item — one sub-task inside a job. Each item eventually
--                  dispatches to an existing generation_task; the
--                  item row holds the local lifecycle so multiple
--                  workers can claim without stepping on each other.
--
-- Idempotency: (uid, idempotency_key) uniq so the same submit
-- retried by a flaky client returns the original job instead of
-- creating a duplicate N-row matrix. Empty key = no dedupe.
--
-- Per-user concurrency gate — read by the orchestration service
-- (not the schema): a user's active-job count compared against
-- max_concurrency decides admission of new submissions. Keeps one
-- team's batch-of-500 from starving another team's batch-of-10.

CREATE TABLE `w_batch_job` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uid` int NOT NULL COMMENT '提交者',
  `project_id` bigint unsigned DEFAULT NULL COMMENT '所属项目,NULL表示跨项目批量',
  `kind` varchar(48) NOT NULL DEFAULT '' COMMENT '批量种类 ad_variant_matrix/ecommerce_sku/...',
  `title` varchar(200) NOT NULL DEFAULT '' COMMENT '用户可见名',
  `total_count` int NOT NULL DEFAULT 0 COMMENT '子任务总数',
  `completed_count` int NOT NULL DEFAULT 0 COMMENT '已完成',
  `failed_count` int NOT NULL DEFAULT 0 COMMENT '失败',
  `cancelled_count` int NOT NULL DEFAULT 0 COMMENT '已取消',
  `status` tinyint NOT NULL DEFAULT 0 COMMENT '0=pending 1=processing 2=completed 3=failed 4=cancelled 5=partial_success',
  `parameters_json` json DEFAULT NULL COMMENT 'kind-specific输入(axis/sku/platform等)',
  `result_summary_json` json DEFAULT NULL COMMENT '汇总结果(成功率/样本url)',
  `idempotency_key` varchar(128) NOT NULL DEFAULT '' COMMENT '重复提交去重,按(uid,key)唯一',
  `max_concurrency` int NOT NULL DEFAULT 5 COMMENT '单job内最大并发子任务数',
  `started_at` datetime DEFAULT NULL,
  `completed_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_project_status` (`project_id`, `status`),
  -- Idempotency gate: same uid + same key returns the original job
  -- rather than a duplicate matrix. Empty key excluded from uniq
  -- via a conditional index when we move to Postgres; MySQL
  -- enforces uniqueness on all rows, and the submit handler sends
  -- a generated UUID when the caller doesn't supply one.
  UNIQUE KEY `uk_uid_idempotency` (`uid`, `idempotency_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='批量任务 — ecommerce/video-ad variant matrix / social multi-platform';

CREATE TABLE `w_batch_item` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `batch_job_id` bigint unsigned NOT NULL,
  `uid` int NOT NULL COMMENT '冗余,便于按用户筛选',
  `seq` int NOT NULL DEFAULT 0 COMMENT '在job内顺序',
  `payload_json` json DEFAULT NULL COMMENT 'item-specific输入',
  `status` tinyint NOT NULL DEFAULT 0 COMMENT '0=pending 1=processing 2=completed 3=failed 4=skipped',
  `generation_task_id` bigint unsigned DEFAULT NULL COMMENT '派发后的w_generation_task关联',
  `result_json` json DEFAULT NULL,
  `error_msg` text,
  `attempt_count` int NOT NULL DEFAULT 0 COMMENT '已尝试次数',
  `attempted_at` datetime DEFAULT NULL,
  `completed_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_batch_status` (`batch_job_id`, `status`),
  KEY `idx_uid_status` (`uid`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='批量子任务 — 一个job含N item';
