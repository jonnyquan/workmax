-- P0-044 - hold ordinary completed Turns until usage is measured.
--
-- A completed executor path that has not yet produced trusted metering must
-- not bypass the durable Settlement Review queue as an unexplained
-- Finalize(0). This migration adds one narrowly-bound Review source/reason
-- pair for that case. It deliberately keeps both historical release branches
-- unchanged: executor/reconcile releases still use usage_unknown and still
-- require durable Operation/Effect evidence.
--
-- executor_completion is allowed only for terminal_status=completed, must be
-- bound to the executor Attempt/Operation, and may carry an all-zero evidence
-- count tuple. Zero counts mean "ordinary completion awaits trusted meter";
-- they are not a commercial assertion of zero usage.

ALTER TABLE `w_agent_turn_settlement_review`
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_reason`,
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_source`,
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_counts`,
  DROP CONSTRAINT `chk_w_agent_turn_settlement_review_source_tuple`,
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_reason`
    CHECK (`reason` IN ('usage_unknown', 'completed_usage_unmeasured')),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_source`
    CHECK (`source` IN ('executor_release', 'reconcile_release', 'executor_completion')),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_counts`
    CHECK (
      `prior_operation_count` BETWEEN 0 AND 9223372036854775807
      AND `prior_effect_count` BETWEEN 0 AND 9223372036854775807
      AND `current_effect_count` BETWEEN 0 AND 64
      AND (
        `source` = 'executor_completion'
        OR `prior_operation_count` > 0
        OR `prior_effect_count` > 0
        OR `current_effect_count` > 0
      )
    ),
  ADD CONSTRAINT `chk_w_agent_turn_settlement_review_source_tuple`
    CHECK (
      (`source` = 'executor_release'
        AND `reason` = 'usage_unknown'
        AND `attempt_id` IS NOT NULL AND `operation_id` IS NOT NULL)
      OR
      (`source` = 'reconcile_release'
        AND `reason` = 'usage_unknown'
        AND `attempt_id` IS NULL AND `operation_id` IS NULL
        AND `current_effect_count` = 0)
      OR
      (`source` = 'executor_completion'
        AND `reason` = 'completed_usage_unmeasured'
        AND `terminal_status` = 'completed'
        AND `attempt_id` IS NOT NULL AND `operation_id` IS NOT NULL)
    );
