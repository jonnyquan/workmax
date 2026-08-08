package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"server/utils/testutil"
)

const agentTurnCompletedSettlementMeteringMigrationFile = "20260670_require_completed_settlement_metering.sql"

func TestAgentTurnCompletedSettlementMeteringMigrationPinsNarrowReviewTuple(t *testing.T) {
	sql := readAgentTurnCompletedSettlementMeteringMigration(t)
	normalized := normalizeSQL(sql)

	for _, want := range []string{
		"alter table `w_agent_turn_settlement_review` drop constraint `chk_w_agent_turn_settlement_review_reason`, drop constraint `chk_w_agent_turn_settlement_review_source`, drop constraint `chk_w_agent_turn_settlement_review_counts`, drop constraint `chk_w_agent_turn_settlement_review_source_tuple`",
		"check (`reason` in ('usage_unknown', 'completed_usage_unmeasured'))",
		"check (`source` in ('executor_release', 'reconcile_release', 'executor_completion'))",
		"`source` = 'executor_completion' or `prior_operation_count` > 0 or `prior_effect_count` > 0 or `current_effect_count` > 0",
		"`source` = 'executor_release' and `reason` = 'usage_unknown' and `attempt_id` is not null and `operation_id` is not null",
		"`source` = 'reconcile_release' and `reason` = 'usage_unknown' and `attempt_id` is null and `operation_id` is null and `current_effect_count` = 0",
		"`source` = 'executor_completion' and `reason` = 'completed_usage_unmeasured' and `terminal_status` = 'completed' and `attempt_id` is not null and `operation_id` is not null",
	} {
		assertSQLContains(t, normalized, want)
	}
	if strings.Contains(normalized, "alter table `w_agent_effect_outbox`") ||
		strings.Contains(normalized, "chk_w_agent_turn_settlement_review_status") ||
		strings.Contains(normalized, "update `w_agent_turn_settlement_review`") {
		t.Fatal("P0-044 must only widen the four named Review CHECK constraints")
	}

	namePattern := regexp.MustCompile("(?i)constraint `([^`]+)`")
	for _, match := range namePattern.FindAllStringSubmatch(sql, -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL constraint identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestAgentTurnCompletedSettlementMeteringSQLiteMirrorAllowsZeroCountCompletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	insertSettlementReviewTurn(t, db, "turn_completion_review", "idem_completion_review")
	insertSettlementReviewExecutorBinding(t, db, "turn_completion_review", "attempt_completion", "operation_completion")

	review := settlementReviewFixture{
		reviewID: "review_completion", turnID: "turn_completion_review",
		settlementKey: "settlement_completion", reason: "completed_usage_unmeasured",
		source: "executor_completion", terminalStatus: "completed",
		attemptID: "attempt_completion", fencingToken: 1, operationID: "operation_completion",
	}
	if err := insertSettlementReview(db, review); err != nil {
		t.Fatalf("insert zero-count completed Settlement Review: %v", err)
	}
}

func TestAgentTurnCompletedSettlementMeteringSQLiteMirrorKeepsReleaseRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*settlementReviewFixture)
	}{
		{name: "completion requires completed terminal", mutate: func(row *settlementReviewFixture) { row.terminalStatus = "failed" }},
		{name: "completion requires its reason", mutate: func(row *settlementReviewFixture) { row.reason = "usage_unknown" }},
		{name: "completion requires attempt", mutate: func(row *settlementReviewFixture) { row.attemptID = nil }},
		{name: "completion requires operation", mutate: func(row *settlementReviewFixture) { row.operationID = nil }},
		{name: "executor release rejects completion reason", mutate: func(row *settlementReviewFixture) {
			row.source, row.reason, row.terminalStatus = "executor_release", "completed_usage_unmeasured", "completed"
		}},
		{name: "reconcile release rejects completion reason", mutate: func(row *settlementReviewFixture) {
			row.source, row.reason = "reconcile_release", "completed_usage_unmeasured"
			row.attemptID, row.operationID = nil, nil
		}},
		{name: "executor release still requires evidence", mutate: func(row *settlementReviewFixture) {
			row.source, row.reason, row.terminalStatus = "executor_release", "usage_unknown", "failed"
		}},
		{name: "reconcile release still requires evidence", mutate: func(row *settlementReviewFixture) {
			row.source, row.reason, row.terminalStatus = "reconcile_release", "usage_unknown", "timeout"
			row.attemptID, row.operationID = nil, nil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			insertSettlementReviewTurn(t, db, "turn_completion_invalid", "idem_completion_invalid")
			insertSettlementReviewExecutorBinding(t, db, "turn_completion_invalid", "attempt_completion_invalid", "operation_completion_invalid")
			row := settlementReviewFixture{
				reviewID: "review_completion_invalid", turnID: "turn_completion_invalid",
				settlementKey: "settlement_completion_invalid", reason: "completed_usage_unmeasured",
				source: "executor_completion", terminalStatus: "completed",
				attemptID: "attempt_completion_invalid", fencingToken: 1, operationID: "operation_completion_invalid",
			}
			test.mutate(&row)
			if err := insertSettlementReview(db, row); err == nil {
				t.Fatal("invalid completed/release Settlement Review tuple must fail")
			}
		})
	}
}

func readAgentTurnCompletedSettlementMeteringMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(agentTurnCompletedSettlementMeteringMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnCompletedSettlementMeteringMigrationFile, err)
	}
	return string(body)
}
