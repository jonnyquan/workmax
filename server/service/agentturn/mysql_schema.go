package agentturn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

const (
	SQLSettlementReviewUsageEvidenceSourceTable = "w_agent_turn_settlement_usage_evidence_source"
	SQLTurnReservationBindingTable              = "w_agent_turn_reservation_binding"
	SQLTurnSettlementOutcomeTable               = "w_agent_turn_settlement_outcome"

	mysqlCreditReservationTable           = "w_credit_reservation"
	mysqlCreditReservationAllocationTable = "w_credit_reservation_allocation"
	mysqlCreditsPackTable                 = "w_credits_pack"
	mysqlGlobalProjectTable               = "w_global_project"
	mysqlUserTable                        = "w_user"
	mysqlOrderTable                       = "w_order"
)

var mysqlRuntimeTables = []string{
	SQLTurnTable,
	SQLTurnEventTable,
	SQLTurnAttemptTable,
	SQLTurnOperationTable,
	SQLEffectOutboxTable,
	SQLUsageMeterReleaseTable,
	SQLProviderUsageJournalTable,
	SQLSettlementReviewTable,
	SQLSettlementReviewUsageEvidenceTable,
	SQLSettlementReviewUsageEvidenceSourceTable,
	SQLSettlementReviewResolutionTable,
	mysqlUserTable,
	mysqlOrderTable,
	mysqlCreditsPackTable,
	mysqlGlobalProjectTable,
	mysqlCreditReservationTable,
	mysqlCreditReservationAllocationTable,
	SQLTurnReservationBindingTable,
	SQLTurnSettlementOutcomeTable,
}

var (
	ErrMySQLRuntimeSchemaUnavailable = errors.New("MySQL runtime schema preflight is unavailable")
	ErrMySQLRuntimeSchemaSession     = errors.New("MySQL runtime schema session contract failed")
	ErrMySQLRuntimeSchemaMetadata    = errors.New("MySQL runtime schema metadata query failed")
	ErrMySQLRuntimeSchemaTables      = errors.New("MySQL runtime table contract failed")
	ErrMySQLRuntimeSchemaColumns     = errors.New("MySQL runtime column contract failed")
	ErrMySQLRuntimeSchemaIndexes     = errors.New("MySQL runtime index contract failed")
	ErrMySQLRuntimeSchemaForeignKeys = errors.New("MySQL runtime foreign-key contract failed")
	ErrMySQLRuntimeSchemaChecks      = errors.New("MySQL runtime check-constraint contract failed")
)

// MySQLSchemaQuerier is implemented by both *sql.DB and *sql.Conn. A caller
// that needs session guarantees should pass a pinned *sql.Conn.
type MySQLSchemaQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type mysqlRuntimeIndex struct {
	table     string
	name      string
	columns   []string
	hasPrefix bool
	invisible bool
}

type mysqlRuntimeColumn struct {
	table             string
	name              string
	dataType          string
	columnType        string
	nullable          bool
	characterSet      string
	collation         string
	datetimePrecision int64
}

type mysqlRuntimeColumnProperty struct {
	table         string
	name          string
	primaryKey    bool
	autoIncrement bool
	defaultPinned bool
	defaultIsNull bool
	defaultValue  string
}

type mysqlRuntimeRequiredColumn struct {
	table string
	name  string
}

// These columns carry identities used by exact replay/conflict decisions or
// canonical JSON bytes used as digest authority. Pinning only indexes and
// CHECK names is insufficient: a case-insensitive collation or MySQL's native
// JSON reserialization would change the runtime semantics while still passing
// the structural preflight.
var mysqlRuntimeColumns = []mysqlRuntimeColumn{
	{SQLUsageMeterReleaseTable, "release_id", "varchar", "varchar(64)", false, "ascii", "ascii_bin", -1},
	{SQLUsageMeterReleaseTable, "plugin_snapshot_digest", "varchar", "varchar(128)", false, "ascii", "ascii_bin", -1},
	{SQLUsageMeterReleaseTable, "pricing_snapshot_json", "mediumblob", "mediumblob", false, "", "", -1},
	{SQLUsageMeterReleaseTable, "source_registry_json", "mediumblob", "mediumblob", false, "", "", -1},
	{SQLUsageMeterReleaseTable, "created_at", "datetime", "datetime(6)", false, "", "", 6},
	{SQLProviderUsageJournalTable, "meter_release_id", "varchar", "varchar(64)", false, "ascii", "ascii_bin", -1},
	{SQLProviderUsageJournalTable, "provider_key", "varchar", "varchar(256)", false, "ascii", "ascii_bin", -1},
	{SQLProviderUsageJournalTable, "provider_account_digest", "varchar", "varchar(128)", false, "ascii", "ascii_bin", -1},
	{SQLProviderUsageJournalTable, "provider_event_digest", "varchar", "varchar(128)", false, "ascii", "ascii_bin", -1},
	{SQLProviderUsageJournalTable, "provider_usage_json", "mediumblob", "mediumblob", false, "", "", -1},
	{SQLProviderUsageJournalTable, "provider_reported_at", "datetime", "datetime(6)", false, "", "", 6},
	{SQLProviderUsageJournalTable, "created_at", "datetime", "datetime(6)", false, "", "", 6},
	{SQLSettlementReviewTable, "prior_provider_usage_count", "bigint", "bigint unsigned", false, "", "", -1},
	{SQLSettlementReviewUsageEvidenceTable, "meter_release_id", "varchar", "varchar(64)", false, "ascii", "ascii_bin", -1},
	{SQLSettlementReviewUsageEvidenceTable, "source_receipt_count", "smallint", "smallint unsigned", false, "", "", -1},
	{SQLSettlementReviewUsageEvidenceSourceTable, "receipt_id", "varchar", "varchar(64)", false, "ascii", "ascii_bin", -1},
	{SQLSettlementReviewUsageEvidenceSourceTable, "meter_release_id", "varchar", "varchar(64)", false, "ascii", "ascii_bin", -1},
	{SQLSettlementReviewUsageEvidenceSourceTable, "source_receipt_count", "smallint", "smallint unsigned", false, "", "", -1},
	{SQLSettlementReviewUsageEvidenceSourceTable, "created_at", "datetime", "datetime(6)", false, "", "", 6},
	{mysqlCreditsPackTable, "id", "bigint", "bigint unsigned", false, "", "", -1},
	{mysqlCreditsPackTable, "uid", "bigint", "bigint", false, "", "", -1},
	{mysqlCreditsPackTable, "source_type", "varchar", "varchar(50)", false, "utf8mb4", "utf8mb4_bin", -1},
	{mysqlCreditsPackTable, "source_id", "varchar", "varchar(64)", false, "utf8mb4", "utf8mb4_bin", -1},
	{mysqlCreditsPackTable, "credits_total", "bigint", "bigint", false, "", "", -1},
	{mysqlCreditsPackTable, "credits_used", "bigint", "bigint", false, "", "", -1},
	{mysqlGlobalProjectTable, "budget_credits_cap", "int", "int", true, "", "", -1},
	{mysqlGlobalProjectTable, "budget_credits_used", "int", "int", false, "", "", -1},
	{mysqlCreditReservationTable, "id", "bigint", "bigint unsigned", false, "", "", -1},
	{mysqlCreditReservationTable, "uid", "int", "int", false, "", "", -1},
	{mysqlCreditReservationTable, "idempotency_key", "varchar", "varchar(128)", false, "utf8mb4", "utf8mb4_general_ci", -1},
	{mysqlCreditReservationTable, "request_digest", "varchar", "varchar(64)", true, "ascii", "ascii_bin", -1},
	{mysqlCreditReservationTable, "tool", "varchar", "varchar(64)", false, "utf8mb4", "utf8mb4_bin", -1},
	{mysqlCreditReservationTable, "reserved", "int", "int", false, "", "", -1},
	{mysqlCreditReservationTable, "used", "int", "int", false, "", "", -1},
	{mysqlCreditReservationTable, "status", "varchar", "varchar(16)", false, "utf8mb4", "utf8mb4_general_ci", -1},
	{mysqlCreditReservationTable, "expires_at", "datetime", "datetime", false, "", "", 0},
	{mysqlCreditReservationTable, "finalized_at", "datetime", "datetime", true, "", "", 0},
	{mysqlCreditReservationTable, "released_at", "datetime", "datetime", true, "", "", 0},
	{mysqlCreditReservationTable, "hold_review_id", "varchar", "varchar(256)", true, "ascii", "ascii_bin", -1},
	{mysqlCreditReservationTable, "hold_settlement_key", "varchar", "varchar(256)", true, "ascii", "ascii_bin", -1},
	{mysqlCreditReservationTable, "hold_request_digest", "varchar", "varchar(128)", true, "ascii", "ascii_bin", -1},
	{mysqlCreditReservationTable, "review_held_at", "datetime", "datetime(6)", true, "", "", 6},
	{mysqlCreditReservationTable, "refund_target_status", "varchar", "varchar(16)", true, "ascii", "ascii_bin", -1},
	{mysqlCreditReservationTable, "refund_target_used", "int", "int unsigned", true, "", "", -1},
	{mysqlCreditReservationTable, "refund_due", "int", "int unsigned", false, "", "", -1},
	{mysqlCreditReservationTable, "refund_attempts", "bigint", "bigint unsigned", false, "", "", -1},
	{mysqlCreditReservationTable, "next_refund_at", "datetime", "datetime(6)", true, "", "", 6},
	{mysqlCreditReservationTable, "last_refund_error_code", "varchar", "varchar(64)", true, "ascii", "ascii_bin", -1},
	{mysqlCreditReservationTable, "state_changed_at", "datetime", "datetime(6)", true, "", "", 6},
	{mysqlCreditReservationTable, "state_version", "bigint", "bigint unsigned", false, "", "", -1},
	{mysqlCreditReservationTable, "project_id", "int", "int", false, "", "", -1},
	{mysqlCreditReservationTable, "created_at", "datetime", "datetime", false, "", "", 0},
	{mysqlCreditReservationTable, "updated_at", "datetime", "datetime", false, "", "", 0},
	{mysqlCreditReservationAllocationTable, "id", "bigint", "bigint unsigned", false, "", "", -1},
	{mysqlCreditReservationAllocationTable, "reservation_id", "bigint", "bigint unsigned", false, "", "", -1},
	{mysqlCreditReservationAllocationTable, "pack_id", "bigint", "bigint unsigned", false, "", "", -1},
	{mysqlCreditReservationAllocationTable, "credits", "int", "int", false, "", "", -1},
	{SQLTurnReservationBindingTable, "id", "bigint", "bigint unsigned", false, "", "", -1},
	{SQLTurnReservationBindingTable, "binding_id", "char", "char(64)", false, "ascii", "ascii_bin", -1},
	{SQLTurnReservationBindingTable, "turn_id", "varchar", "varchar(256)", false, "utf8mb4", "utf8mb4_bin", -1},
	{SQLTurnReservationBindingTable, "principal_id", "varchar", "varchar(128)", false, "utf8mb4", "utf8mb4_bin", -1},
	{SQLTurnReservationBindingTable, "turn_command_digest", "varchar", "varchar(128)", false, "ascii", "ascii_bin", -1},
	{SQLTurnReservationBindingTable, "reservation_id", "bigint", "bigint unsigned", false, "", "", -1},
	{SQLTurnReservationBindingTable, "reservation_uid", "int", "int", false, "", "", -1},
	{SQLTurnReservationBindingTable, "reservation_request_digest", "varchar", "varchar(64)", false, "ascii", "ascii_bin", -1},
	{SQLTurnReservationBindingTable, "reservation_tool", "varchar", "varchar(64)", false, "utf8mb4", "utf8mb4_bin", -1},
	{SQLTurnReservationBindingTable, "reserved_units", "int", "int", false, "", "", -1},
	{SQLTurnReservationBindingTable, "project_id", "int", "int", false, "", "", -1},
	{SQLTurnReservationBindingTable, "pricing_snapshot_digest", "char", "char(71)", false, "ascii", "ascii_bin", -1},
	{SQLTurnReservationBindingTable, "binding_digest", "char", "char(71)", false, "ascii", "ascii_bin", -1},
	{SQLTurnReservationBindingTable, "created_at", "datetime", "datetime(6)", false, "", "", 6},
	{SQLTurnSettlementOutcomeTable, "id", "bigint", "bigint unsigned", false, "", "", -1},
	{SQLTurnSettlementOutcomeTable, "outcome_id", "char", "char(64)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "binding_id", "char", "char(64)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "turn_id", "varchar", "varchar(256)", false, "utf8mb4", "utf8mb4_bin", -1},
	{SQLTurnSettlementOutcomeTable, "reservation_id", "bigint", "bigint unsigned", false, "", "", -1},
	{SQLTurnSettlementOutcomeTable, "binding_digest", "char", "char(71)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "settlement_key", "varchar", "varchar(256)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "ledger_request_digest", "char", "char(71)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "authorization_kind", "varchar", "varchar(16)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "attempt_id", "varchar", "varchar(64)", true, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "fencing_token", "bigint", "bigint unsigned", false, "", "", -1},
	{SQLTurnSettlementOutcomeTable, "operation_id", "varchar", "varchar(128)", true, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "terminal_status", "varchar", "varchar(16)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "requested_intent", "varchar", "varchar(16)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "used_units", "int", "int", true, "", "", -1},
	{SQLTurnSettlementOutcomeTable, "reserved_units", "int", "int", false, "", "", -1},
	{SQLTurnSettlementOutcomeTable, "status", "varchar", "varchar(16)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "refund_target", "varchar", "varchar(16)", true, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "refund_due", "int", "int", false, "", "", -1},
	{SQLTurnSettlementOutcomeTable, "reservation_state_version", "bigint", "bigint unsigned", false, "", "", -1},
	{SQLTurnSettlementOutcomeTable, "review_id", "varchar", "varchar(64)", true, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "review_request_digest", "varchar", "varchar(128)", true, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "resolution_id", "varchar", "varchar(64)", true, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "resolution_request_digest", "varchar", "varchar(128)", true, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "outcome_digest", "char", "char(71)", false, "ascii", "ascii_bin", -1},
	{SQLTurnSettlementOutcomeTable, "created_at", "datetime", "datetime(6)", false, "", "", 6},
	{SQLTurnSettlementOutcomeTable, "updated_at", "datetime", "datetime(6)", false, "", "", 6},
}

// These legacy owner tables predate the strict Agent migrations. Their exact
// historical width/default/collation differs across supported installations,
// but every listed field is read or written by positive-credit admission or
// refund. Presence is therefore fail-closed today; a future legacy-table
// hardening migration can promote individual entries to mysqlRuntimeColumns.
var mysqlRuntimeRequiredColumns = []mysqlRuntimeRequiredColumn{
	{mysqlCreditsPackTable, "expires_at"},
	{mysqlGlobalProjectTable, "id"},
	{mysqlGlobalProjectTable, "uid"},
	{mysqlGlobalProjectTable, "deleted_at"},
	{mysqlUserTable, "id"},
	{mysqlUserTable, "ban"},
	{mysqlUserTable, "ban_note"},
	{mysqlUserTable, "member"},
	{mysqlUserTable, "member_start_time"},
	{mysqlUserTable, "member_end_time"},
	{mysqlUserTable, "member_subscription"},
	{mysqlOrderTable, "id"},
	{mysqlOrderTable, "uid"},
	{mysqlOrderTable, "order_type"},
	{mysqlOrderTable, "status"},
	{mysqlOrderTable, "product_id"},
	{mysqlOrderTable, "pay_time"},
	{mysqlOrderTable, "subscription_id"},
	{mysqlOrderTable, "credits_amount"},
}

var mysqlRuntimeColumnProperties = []mysqlRuntimeColumnProperty{
	{table: mysqlCreditsPackTable, name: "id", primaryKey: true, autoIncrement: true},
	{table: mysqlCreditReservationTable, name: "id", primaryKey: true, autoIncrement: true},
	{table: mysqlCreditReservationTable, name: "reserved", defaultPinned: true, defaultValue: "0"},
	{table: mysqlCreditReservationTable, name: "used", defaultPinned: true, defaultValue: "0"},
	{table: mysqlCreditReservationTable, name: "status", defaultPinned: true, defaultValue: "reserved"},
	{table: mysqlCreditReservationTable, name: "project_id", defaultPinned: true, defaultValue: "0"},
	{table: mysqlCreditReservationTable, name: "refund_due", defaultPinned: true, defaultValue: "0"},
	{table: mysqlCreditReservationTable, name: "refund_attempts", defaultPinned: true, defaultValue: "0"},
	{table: mysqlCreditReservationTable, name: "state_version", defaultPinned: true, defaultValue: "0"},
	{table: mysqlCreditReservationAllocationTable, name: "id", primaryKey: true, autoIncrement: true},
	{table: mysqlCreditReservationAllocationTable, name: "credits", defaultPinned: true, defaultValue: "0"},
	{table: mysqlGlobalProjectTable, name: "id", primaryKey: true, autoIncrement: true},
	{table: mysqlGlobalProjectTable, name: "budget_credits_cap", defaultPinned: true, defaultIsNull: true},
	{table: mysqlGlobalProjectTable, name: "budget_credits_used", defaultPinned: true, defaultValue: "0"},
	{table: mysqlUserTable, name: "id", primaryKey: true, autoIncrement: true},
	{table: mysqlOrderTable, name: "id", primaryKey: true, autoIncrement: true},
}

// COLUMN_KEY='PRI' on information_schema.COLUMNS proves only that a column
// participates in a PRIMARY KEY. It does not prove the single-column owner
// identity used by First(id), FOR UPDATE and allocation/refund joins: a
// drifted PRIMARY KEY (id, tenant_id) would otherwise pass. Keep the exact
// ordered PRIMARY fingerprint separate from application UNIQUE indexes.
var mysqlRuntimePrimaryKeys = []mysqlRuntimeIndex{
	{table: mysqlCreditsPackTable, name: "PRIMARY", columns: []string{"id"}},
	{table: mysqlCreditReservationTable, name: "PRIMARY", columns: []string{"id"}},
	{table: mysqlCreditReservationAllocationTable, name: "PRIMARY", columns: []string{"id"}},
	{table: mysqlGlobalProjectTable, name: "PRIMARY", columns: []string{"id"}},
	{table: mysqlUserTable, name: "PRIMARY", columns: []string{"id"}},
	{table: mysqlOrderTable, name: "PRIMARY", columns: []string{"id"}},
}

type mysqlRuntimeForeignKey struct {
	table             string
	name              string
	referencedTable   string
	columns           []string
	referencedColumns []string
	updateRule        string
	deleteRule        string
}

type mysqlRuntimeCheck struct {
	table    string
	name     string
	clause   string
	enforced bool
}

var mysqlRuntimeUniqueIndexes = []mysqlRuntimeIndex{
	{table: SQLTurnTable, name: "uk_w_agent_turn_turn_id", columns: []string{"turn_id"}},
	{table: SQLTurnTable, name: "uk_w_agent_turn_admission", columns: []string{"principal_id", "thread_id", "idempotency_key"}},
	{table: SQLTurnTable, name: "uk_w_agent_turn_reservation_identity", columns: []string{"turn_id", "principal_id", "command_digest"}},
	{table: SQLTurnTable, name: "uk_w_agent_turn_settlement_fence", columns: []string{"turn_id", "fencing_token", "status"}},
	{table: SQLTurnEventTable, name: "uk_w_agent_turn_event_sequence", columns: []string{"turn_id", "sequence"}},
	{table: SQLTurnEventTable, name: "uk_w_agent_turn_event_id", columns: []string{"turn_id", "event_id"}},
	{table: SQLTurnAttemptTable, name: "uk_w_agent_turn_attempt_id", columns: []string{"attempt_id"}},
	{table: SQLTurnAttemptTable, name: "uk_w_agent_turn_attempt_fence", columns: []string{"turn_id", "fencing_token"}},
	{table: SQLTurnAttemptTable, name: "uk_w_agent_turn_attempt_binding", columns: []string{"turn_id", "attempt_id", "fencing_token"}},
	{table: SQLTurnOperationTable, name: "uk_w_agent_turn_operation_identity", columns: []string{"turn_id", "operation_id"}},
	{table: SQLTurnOperationTable, name: "uk_w_agent_turn_operation_binding", columns: []string{"turn_id", "operation_id", "attempt_id", "fencing_token"}},
	{table: SQLTurnOperationTable, name: "uk_w_agent_turn_operation_settlement_binding", columns: []string{"turn_id", "operation_id", "attempt_id", "fencing_token", "turn_status"}},
	{table: SQLEffectOutboxTable, name: "uk_w_agent_effect_outbox_id", columns: []string{"outbox_id"}},
	{table: SQLEffectOutboxTable, name: "uk_w_agent_effect_outbox_dedupe", columns: []string{"topic", "dedupe_key"}},
	{table: SQLEffectOutboxTable, name: "uk_w_agent_effect_outbox_operation_ordinal", columns: []string{"turn_id", "operation_id", "ordinal"}},
	{table: SQLSettlementReviewTable, name: "uk_w_agent_turn_settlement_review_review_id", columns: []string{"review_id"}},
	{table: SQLSettlementReviewTable, name: "uk_w_agent_turn_settlement_review_turn_id", columns: []string{"turn_id"}},
	{table: SQLSettlementReviewTable, name: "uk_w_agent_turn_settlement_review_settlement_key", columns: []string{"settlement_key"}},
	{table: SQLSettlementReviewTable, name: "uk_w_agent_turn_settlement_review_resolution_binding", columns: []string{"review_id", "turn_id", "settlement_key", "request_digest"}},
	{table: SQLSettlementReviewTable, name: "uk_w_agent_turn_settlement_review_outcome_binding", columns: []string{"review_id", "turn_id", "settlement_key", "request_digest", "terminal_status"}},
	{table: SQLSettlementReviewUsageEvidenceTable, name: "uk_w_agent_turn_settlement_usage_evidence_evidence_id", columns: []string{"evidence_id"}},
	{table: SQLSettlementReviewUsageEvidenceTable, name: "uk_w_agent_turn_settlement_usage_evidence_review_id", columns: []string{"review_id"}},
	{table: SQLSettlementReviewUsageEvidenceTable, name: "uk_w_agent_turn_settlement_usage_evidence_meter_source", columns: []string{"meter_key", "meter_version", "usage_source_digest"}},
	{table: SQLSettlementReviewUsageEvidenceTable, name: "uk_w_agent_turn_settlement_usage_evidence_resolution_binding", columns: []string{"review_id", "turn_id", "settlement_key", "review_request_digest", "evidence_id", "pricing_snapshot_digest", "evidence_digest", "used_units"}},
	{table: SQLSettlementReviewUsageEvidenceTable, name: "uk_w_agent_turn_settlement_usage_evidence_provenance", columns: []string{"evidence_id", "review_id", "turn_id", "settlement_key", "review_request_digest", "meter_release_id", "usage_source_digest", "evidence_digest", "source_receipt_count"}},
	{table: SQLSettlementReviewResolutionTable, name: "uk_w_agent_turn_settlement_review_resolution_resolution_id", columns: []string{"resolution_id"}},
	{table: SQLSettlementReviewResolutionTable, name: "uk_w_agent_turn_settlement_review_resolution_review_id", columns: []string{"review_id"}},
	{table: SQLUsageMeterReleaseTable, name: "uk_w_agent_usage_meter_release_release_id", columns: []string{"release_id"}},
	{table: SQLUsageMeterReleaseTable, name: "uk_w_agent_usage_meter_release_plugin_snapshot", columns: []string{"plugin_snapshot_digest"}},
	{table: SQLUsageMeterReleaseTable, name: "uk_w_agent_usage_meter_release_digest", columns: []string{"release_digest"}},
	{table: SQLProviderUsageJournalTable, name: "uk_w_agent_provider_usage_journal_receipt_id", columns: []string{"receipt_id"}},
	{table: SQLProviderUsageJournalTable, name: "uk_w_agent_provider_usage_journal_provider_event", columns: []string{"provider_key", "provider_account_digest", "provider_event_digest"}},
	{table: SQLProviderUsageJournalTable, name: "uk_w_agent_provider_usage_journal_source_binding", columns: []string{"receipt_id", "turn_id", "meter_release_id", "canonical_usage_digest", "provider_receipt_digest", "journal_record_digest"}},
	{table: SQLSettlementReviewUsageEvidenceSourceTable, name: "uk_w_agent_turn_settlement_usage_evidence_source_ordinal", columns: []string{"evidence_id", "ordinal"}},
	{table: SQLSettlementReviewUsageEvidenceSourceTable, name: "uk_w_agent_turn_settlement_usage_evidence_source_receipt", columns: []string{"receipt_id"}},
	{table: mysqlCreditsPackTable, name: "uk_w_credits_pack_source_identity", columns: []string{"uid", "source_type", "source_id"}},
	{table: mysqlCreditReservationTable, name: "idx_reservation_uid_key", columns: []string{"uid", "idempotency_key"}},
	{table: mysqlCreditReservationTable, name: "uk_w_credit_reservation_hold_settlement", columns: []string{"hold_settlement_key"}},
	{table: mysqlCreditReservationTable, name: "uk_w_credit_reservation_agent_binding", columns: []string{"id", "uid", "request_digest", "tool", "reserved", "project_id"}},
	{table: mysqlCreditReservationAllocationTable, name: "uk_w_credit_reservation_allocation_pair", columns: []string{"reservation_id", "pack_id"}},
	{table: SQLTurnReservationBindingTable, name: "uk_w_agent_turn_reservation_binding_binding_id", columns: []string{"binding_id"}},
	{table: SQLTurnReservationBindingTable, name: "uk_w_agent_turn_reservation_binding_turn_id", columns: []string{"turn_id"}},
	{table: SQLTurnReservationBindingTable, name: "uk_w_agent_turn_reservation_binding_reservation_id", columns: []string{"reservation_id"}},
	{table: SQLTurnReservationBindingTable, name: "uk_w_agent_turn_reservation_binding_exact", columns: []string{"binding_id", "turn_id", "reservation_id", "binding_digest", "reserved_units"}},
	{table: SQLTurnSettlementOutcomeTable, name: "uk_w_agent_turn_settlement_outcome_outcome_id", columns: []string{"outcome_id"}},
	{table: SQLTurnSettlementOutcomeTable, name: "uk_w_agent_turn_settlement_outcome_settlement_key", columns: []string{"settlement_key"}},
	{table: SQLTurnSettlementOutcomeTable, name: "uk_w_agent_turn_settlement_outcome_turn_id", columns: []string{"turn_id"}},
	{table: SQLTurnSettlementOutcomeTable, name: "uk_w_agent_turn_settlement_outcome_reservation_id", columns: []string{"reservation_id"}},
	{table: SQLTurnSettlementOutcomeTable, name: "uk_w_agent_turn_settlement_outcome_review_id", columns: []string{"review_id"}},
}

// Ordinary indexes below are part of correctness, not just tuning: they bound
// owner/range lock acquisition and keep sweeper/refund scans from locking an
// unrelated tenant-sized range under InnoDB.
var mysqlRuntimeOrdinaryIndexes = []mysqlRuntimeIndex{
	{table: mysqlCreditsPackTable, name: "idx_w_credits_pack_uid_id", columns: []string{"uid", "id"}},
	{table: mysqlOrderTable, name: "idx_w_order_uid", columns: []string{"uid"}},
	{table: mysqlOrderTable, name: "idx_w_order_membership_resolution", columns: []string{"uid", "order_type", "status", "pay_time", "id"}},
	{table: mysqlCreditReservationTable, name: "idx_w_credit_reservation_sweep", columns: []string{"status", "expires_at", "id"}},
	{table: mysqlCreditReservationTable, name: "idx_w_credit_reservation_refund", columns: []string{"status", "next_refund_at", "id"}},
	{table: mysqlCreditReservationAllocationTable, name: "idx_credit_reservation_allocation_reservation", columns: []string{"reservation_id"}},
	{table: mysqlCreditReservationAllocationTable, name: "idx_credit_reservation_allocation_pack", columns: []string{"pack_id"}},
}

var mysqlRuntimeForeignKeys = []mysqlRuntimeForeignKey{
	{SQLTurnEventTable, "fk_w_agent_turn_event_turn", SQLTurnTable, []string{"turn_id"}, []string{"turn_id"}, "RESTRICT", "RESTRICT"},
	{SQLTurnAttemptTable, "fk_w_agent_turn_attempt_turn", SQLTurnTable, []string{"turn_id"}, []string{"turn_id"}, "RESTRICT", "RESTRICT"},
	{SQLTurnTable, "fk_w_agent_turn_active_attempt", SQLTurnAttemptTable, []string{"turn_id", "active_attempt_id", "fencing_token"}, []string{"turn_id", "attempt_id", "fencing_token"}, "RESTRICT", "RESTRICT"},
	{SQLTurnOperationTable, "fk_w_agent_turn_operation_attempt", SQLTurnAttemptTable, []string{"turn_id", "attempt_id", "fencing_token"}, []string{"turn_id", "attempt_id", "fencing_token"}, "RESTRICT", "RESTRICT"},
	{SQLTurnOperationTable, "fk_w_agent_turn_operation_event", SQLTurnEventTable, []string{"turn_id", "event_sequence"}, []string{"turn_id", "sequence"}, "RESTRICT", "RESTRICT"},
	{SQLEffectOutboxTable, "fk_w_agent_effect_outbox_attempt", SQLTurnAttemptTable, []string{"turn_id", "attempt_id", "turn_fencing_token"}, []string{"turn_id", "attempt_id", "fencing_token"}, "RESTRICT", "RESTRICT"},
	{SQLEffectOutboxTable, "fk_w_agent_effect_outbox_operation", SQLTurnOperationTable, []string{"turn_id", "operation_id", "attempt_id", "turn_fencing_token"}, []string{"turn_id", "operation_id", "attempt_id", "fencing_token"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewTable, "fk_w_agent_turn_settlement_review_turn", SQLTurnTable, []string{"turn_id"}, []string{"turn_id"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewTable, "fk_w_agent_turn_settlement_review_operation", SQLTurnOperationTable, []string{"turn_id", "operation_id", "attempt_id", "fencing_token"}, []string{"turn_id", "operation_id", "attempt_id", "fencing_token"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewUsageEvidenceTable, "fk_w_agent_turn_settlement_usage_evidence_review", SQLSettlementReviewTable, []string{"review_id", "turn_id", "settlement_key", "review_request_digest"}, []string{"review_id", "turn_id", "settlement_key", "request_digest"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewResolutionTable, "fk_w_agent_turn_settlement_review_resolution_review", SQLSettlementReviewTable, []string{"review_id", "turn_id", "settlement_key", "review_request_digest"}, []string{"review_id", "turn_id", "settlement_key", "request_digest"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewResolutionTable, "fk_w_agent_turn_settlement_review_resolution_evidence", SQLSettlementReviewUsageEvidenceTable, []string{"review_id", "turn_id", "settlement_key", "review_request_digest", "evidence_id", "pricing_snapshot_digest", "evidence_digest", "used_units"}, []string{"review_id", "turn_id", "settlement_key", "review_request_digest", "evidence_id", "pricing_snapshot_digest", "evidence_digest", "used_units"}, "RESTRICT", "RESTRICT"},
	{SQLProviderUsageJournalTable, "fk_w_agent_provider_usage_journal_attempt", SQLTurnAttemptTable, []string{"turn_id", "attempt_id", "fencing_token"}, []string{"turn_id", "attempt_id", "fencing_token"}, "RESTRICT", "RESTRICT"},
	{SQLProviderUsageJournalTable, "fk_w_agent_provider_usage_journal_meter_release", SQLUsageMeterReleaseTable, []string{"meter_release_id"}, []string{"release_id"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewUsageEvidenceTable, "fk_w_agent_turn_settlement_usage_evidence_meter_release", SQLUsageMeterReleaseTable, []string{"meter_release_id"}, []string{"release_id"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewUsageEvidenceSourceTable, "fk_w_agent_turn_settlement_usage_evidence_source_evidence", SQLSettlementReviewUsageEvidenceTable, []string{"evidence_id", "review_id", "turn_id", "settlement_key", "review_request_digest", "meter_release_id", "usage_source_digest", "evidence_digest", "source_receipt_count"}, []string{"evidence_id", "review_id", "turn_id", "settlement_key", "review_request_digest", "meter_release_id", "usage_source_digest", "evidence_digest", "source_receipt_count"}, "RESTRICT", "RESTRICT"},
	{SQLSettlementReviewUsageEvidenceSourceTable, "fk_w_agent_turn_settlement_usage_evidence_source_journal", SQLProviderUsageJournalTable, []string{"receipt_id", "turn_id", "meter_release_id", "canonical_usage_digest", "provider_receipt_digest", "journal_record_digest"}, []string{"receipt_id", "turn_id", "meter_release_id", "canonical_usage_digest", "provider_receipt_digest", "journal_record_digest"}, "RESTRICT", "RESTRICT"},
	{SQLTurnReservationBindingTable, "fk_w_agent_turn_reservation_binding_turn", SQLTurnTable, []string{"turn_id", "principal_id", "turn_command_digest"}, []string{"turn_id", "principal_id", "command_digest"}, "RESTRICT", "RESTRICT"},
	{SQLTurnReservationBindingTable, "fk_w_agent_turn_reservation_binding_reservation", mysqlCreditReservationTable, []string{"reservation_id", "reservation_uid", "reservation_request_digest", "reservation_tool", "reserved_units", "project_id"}, []string{"id", "uid", "request_digest", "tool", "reserved", "project_id"}, "RESTRICT", "RESTRICT"},
	{mysqlCreditReservationAllocationTable, "fk_w_credit_reservation_allocation_reservation", mysqlCreditReservationTable, []string{"reservation_id"}, []string{"id"}, "RESTRICT", "RESTRICT"},
	{mysqlCreditReservationAllocationTable, "fk_w_credit_reservation_allocation_pack", mysqlCreditsPackTable, []string{"pack_id"}, []string{"id"}, "RESTRICT", "RESTRICT"},
	{SQLTurnSettlementOutcomeTable, "fk_w_agent_turn_settlement_outcome_binding", SQLTurnReservationBindingTable, []string{"binding_id", "turn_id", "reservation_id", "binding_digest", "reserved_units"}, []string{"binding_id", "turn_id", "reservation_id", "binding_digest", "reserved_units"}, "RESTRICT", "RESTRICT"},
	{SQLTurnSettlementOutcomeTable, "fk_w_agent_turn_settlement_outcome_turn_fence", SQLTurnTable, []string{"turn_id", "fencing_token", "terminal_status"}, []string{"turn_id", "fencing_token", "status"}, "RESTRICT", "RESTRICT"},
	{SQLTurnSettlementOutcomeTable, "fk_w_agent_turn_settlement_outcome_operation", SQLTurnOperationTable, []string{"turn_id", "operation_id", "attempt_id", "fencing_token", "terminal_status"}, []string{"turn_id", "operation_id", "attempt_id", "fencing_token", "turn_status"}, "RESTRICT", "RESTRICT"},
	{SQLTurnSettlementOutcomeTable, "fk_w_agent_turn_settlement_outcome_review", SQLSettlementReviewTable, []string{"review_id", "turn_id", "settlement_key", "review_request_digest", "terminal_status"}, []string{"review_id", "turn_id", "settlement_key", "request_digest", "terminal_status"}, "RESTRICT", "RESTRICT"},
}

// P0-044 depends on these predicates, not merely on the Review table and its
// indexes existing. A pre-P0-044 schema would accept an unexplained completed
// Finalize(0), while a loosely widened schema could weaken the historical
// release evidence rules. Keep the clauses exact and separately counted from
// the 19-table / 6-primary-key / 49-business-UNIQUE / 7-visible-ordinary-index /
// 25-RESTRICT-FK structural contract.
var mysqlRuntimeChecks = []mysqlRuntimeCheck{
	{
		table: mysqlGlobalProjectTable,
		name:  "chk_w_global_project_budget_credits",
		clause: `(budget_credits_cap IS NULL OR budget_credits_cap >= 0)
			AND budget_credits_used >= 0`,
		enforced: true,
	},
	{
		table:    SQLSettlementReviewTable,
		name:     "chk_w_agent_turn_settlement_review_reason",
		clause:   "reason IN ('usage_unknown', 'completed_usage_unmeasured', 'terminal_usage_unmeasured')",
		enforced: true,
	},
	{
		table:    SQLSettlementReviewTable,
		name:     "chk_w_agent_turn_settlement_review_source",
		clause:   "source IN ('executor_release', 'reconcile_release', 'executor_completion', 'executor_terminal', 'reconcile_terminal')",
		enforced: true,
	},
	{
		table: SQLSettlementReviewTable,
		name:  "chk_w_agent_turn_settlement_review_counts",
		clause: `prior_operation_count BETWEEN 0 AND 9223372036854775807
			AND prior_effect_count BETWEEN 0 AND 9223372036854775807
			AND prior_provider_usage_count BETWEEN 0 AND 9223372036854775807
			AND current_effect_count BETWEEN 0 AND 64
			AND (source IN ('executor_completion', 'executor_terminal', 'reconcile_terminal')
				OR (source IN ('executor_release', 'reconcile_release')
					AND prior_provider_usage_count = 0
					AND (prior_operation_count > 0
						OR prior_effect_count > 0
						OR current_effect_count > 0)))`,
		enforced: true,
	},
	{
		table: SQLSettlementReviewTable,
		name:  "chk_w_agent_turn_settlement_review_source_tuple",
		clause: `(source = 'executor_release'
				AND reason = 'usage_unknown'
				AND attempt_id IS NOT NULL AND operation_id IS NOT NULL)
			OR (source = 'reconcile_release'
				AND reason = 'usage_unknown'
				AND attempt_id IS NULL AND operation_id IS NULL
				AND current_effect_count = 0)
			OR (source = 'executor_completion'
				AND reason = 'completed_usage_unmeasured'
				AND terminal_status = 'completed'
				AND attempt_id IS NOT NULL AND operation_id IS NOT NULL)
			OR (source = 'executor_terminal'
				AND reason = 'terminal_usage_unmeasured'
				AND terminal_status IN ('stopped', 'failed', 'timeout')
				AND attempt_id IS NOT NULL AND operation_id IS NOT NULL)
			OR (source = 'reconcile_terminal'
				AND reason = 'terminal_usage_unmeasured'
				AND terminal_status IN ('stopped', 'failed', 'timeout')
				AND attempt_id IS NULL AND operation_id IS NULL
				AND current_effect_count = 0)`,
		enforced: true,
	},
	{
		table: SQLUsageMeterReleaseTable,
		name:  "chk_w_agent_usage_meter_release_payloads",
		clause: `OCTET_LENGTH(pricing_snapshot_json) BETWEEN 1 AND 65536
			AND JSON_VALID(CONVERT(pricing_snapshot_json USING utf8mb4))
			AND CONVERT(CONVERT(pricing_snapshot_json USING utf8mb4) USING binary) = pricing_snapshot_json
			AND OCTET_LENGTH(source_registry_json) BETWEEN 1 AND 65536
			AND JSON_VALID(CONVERT(source_registry_json USING utf8mb4))
			AND CONVERT(CONVERT(source_registry_json USING utf8mb4) USING binary) = source_registry_json`,
		enforced: true,
	},
	{
		table: SQLProviderUsageJournalTable,
		name:  "chk_w_agent_provider_usage_journal_payload",
		clause: `OCTET_LENGTH(provider_usage_json) BETWEEN 1 AND 65536
			AND JSON_VALID(CONVERT(provider_usage_json USING utf8mb4))
			AND CONVERT(CONVERT(provider_usage_json USING utf8mb4) USING binary) = provider_usage_json`,
		enforced: true,
	},
	{
		table: SQLSettlementReviewUsageEvidenceTable,
		name:  "chk_w_agent_turn_settlement_usage_evidence_provenance",
		clause: `OCTET_LENGTH(meter_release_id) BETWEEN 1 AND 64
			AND source_receipt_count BETWEEN 1 AND 64`,
		enforced: true,
	},
	{
		table: SQLSettlementReviewUsageEvidenceSourceTable,
		name:  "chk_w_agent_turn_settlement_usage_evidence_source_ordinal",
		clause: `source_receipt_count BETWEEN 1 AND 64
			AND ordinal < source_receipt_count`,
		enforced: true,
	},
	{
		table:    mysqlCreditsPackTable,
		name:     "chk_credits_used_bounds",
		clause:   "credits_used >= 0 AND credits_used <= credits_total",
		enforced: true,
	},
	{
		table: mysqlCreditsPackTable,
		name:  "chk_w_credits_pack_source_identity_canonical",
		clause: `NULLIF(TRIM(source_type), '') IS NOT NULL
			AND NULLIF(TRIM(source_id), '') IS NOT NULL
			AND BINARY source_type = BINARY TRIM(source_type)
			AND BINARY source_id = BINARY TRIM(source_id)`,
		enforced: true,
	},
	{
		table:    mysqlCreditReservationTable,
		name:     "chk_w_credit_reservation_status",
		clause:   "BINARY status IN ('reserved', 'review_hold', 'refund_pending', 'finalized', 'released', 'expired')",
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_amounts",
		clause: `reserved >= 0
			AND used >= 0
			AND used <= reserved
			AND (BINARY status = 'finalized' OR used = 0)
			AND refund_due BETWEEN 0 AND reserved`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_digests",
		clause: `(request_digest IS NULL OR (
				OCTET_LENGTH(request_digest) = 64
				AND BINARY request_digest NOT REGEXP '[^0-9a-f]'
			))
			AND (hold_request_digest IS NULL OR (
				OCTET_LENGTH(hold_request_digest) = 71
				AND BINARY LEFT(hold_request_digest, 7) = 'sha256:'
				AND BINARY SUBSTRING(hold_request_digest, 8) NOT REGEXP '[^0-9a-f]'
			))`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_bounded_codes",
		clause: `(hold_review_id IS NULL OR OCTET_LENGTH(hold_review_id) BETWEEN 1 AND 256)
			AND (hold_settlement_key IS NULL OR OCTET_LENGTH(hold_settlement_key) BETWEEN 1 AND 256)
			AND (last_refund_error_code IS NULL OR OCTET_LENGTH(last_refund_error_code) BETWEEN 1 AND 64)`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_hold_tuple",
		clause: `(hold_review_id IS NULL
				AND hold_settlement_key IS NULL
				AND hold_request_digest IS NULL
				AND review_held_at IS NULL)
			OR (hold_review_id IS NOT NULL
				AND hold_settlement_key IS NOT NULL
				AND hold_request_digest IS NOT NULL
				AND review_held_at IS NOT NULL)`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_review_state",
		clause: `(BINARY status <> 'reserved'
				OR (hold_review_id IS NULL
					AND hold_settlement_key IS NULL
					AND hold_request_digest IS NULL
					AND review_held_at IS NULL))
			AND (BINARY status <> 'review_hold'
				OR (hold_review_id IS NOT NULL
					AND hold_settlement_key IS NOT NULL
					AND hold_request_digest IS NOT NULL
					AND review_held_at IS NOT NULL))`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_refund_tuple",
		clause: `(BINARY status = 'refund_pending'
				AND refund_target_status IS NOT NULL
				AND BINARY refund_target_status IN ('finalized', 'released', 'expired')
				AND refund_target_used IS NOT NULL
				AND refund_target_used BETWEEN 0 AND reserved
				AND refund_due > 0
				AND refund_due = reserved - refund_target_used
				AND (BINARY refund_target_status = 'finalized' OR refund_target_used = 0)
				AND next_refund_at IS NOT NULL)
			OR (BINARY status <> 'refund_pending'
				AND refund_target_status IS NULL
				AND refund_target_used IS NULL
				AND refund_due = 0
				AND next_refund_at IS NULL)`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_status_time",
		clause: `(BINARY status IN ('reserved', 'review_hold', 'refund_pending')
				AND finalized_at IS NULL AND released_at IS NULL)
			OR (BINARY status = 'finalized'
				AND finalized_at IS NOT NULL AND released_at IS NULL)
			OR (BINARY status IN ('released', 'expired')
				AND finalized_at IS NULL AND released_at IS NOT NULL)`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_refund_error_code",
		clause: `last_refund_error_code IS NULL
			OR (BINARY status = 'refund_pending'
				AND BINARY last_refund_error_code IN (
					'project_invariant', 'allocation_invalid', 'allocation_incomplete',
					'pack_invariant', 'database_error'
				))`,
		enforced: true,
	},
	{
		table: mysqlCreditReservationTable,
		name:  "chk_w_credit_reservation_lifecycle_time",
		clause: `(review_held_at IS NULL OR review_held_at >= created_at)
			AND (state_changed_at IS NULL OR state_changed_at >= created_at)`,
		enforced: true,
	},
	{
		table:    mysqlCreditReservationAllocationTable,
		name:     "chk_w_credit_reservation_allocation_credits",
		clause:   "credits > 0",
		enforced: true,
	},
	{
		table: SQLTurnReservationBindingTable,
		name:  "chk_w_agent_turn_reservation_binding_identity",
		clause: `OCTET_LENGTH(binding_id) = 64
			AND BINARY binding_id NOT REGEXP '[^0-9a-f]'
			AND OCTET_LENGTH(turn_id) BETWEEN 1 AND 256
			AND OCTET_LENGTH(principal_id) BETWEEN 1 AND 128
			AND OCTET_LENGTH(turn_command_digest) BETWEEN 1 AND 128
			AND BINARY turn_command_digest NOT REGEXP '[^!-~]'
			AND reservation_uid > 0
			AND OCTET_LENGTH(reservation_request_digest) = 64
			AND BINARY reservation_request_digest NOT REGEXP '[^0-9a-f]'
			AND OCTET_LENGTH(reservation_tool) BETWEEN 1 AND 64
			AND reservation_tool = TRIM(reservation_tool)`,
		enforced: true,
	},
	{
		table:    SQLTurnReservationBindingTable,
		name:     "chk_w_agent_turn_reservation_binding_amounts",
		clause:   "reserved_units BETWEEN 0 AND 2147483647 AND project_id >= 0",
		enforced: true,
	},
	{
		table: SQLTurnReservationBindingTable,
		name:  "chk_w_agent_turn_reservation_binding_digests",
		clause: `OCTET_LENGTH(pricing_snapshot_digest) = 71
			AND BINARY LEFT(pricing_snapshot_digest, 7) = 'sha256:'
			AND BINARY SUBSTRING(pricing_snapshot_digest, 8) NOT REGEXP '[^0-9a-f]'
			AND OCTET_LENGTH(binding_digest) = 71
			AND BINARY LEFT(binding_digest, 7) = 'sha256:'
			AND BINARY SUBSTRING(binding_digest, 8) NOT REGEXP '[^0-9a-f]'`,
		enforced: true,
	},
	{
		table: SQLTurnSettlementOutcomeTable,
		name:  "chk_w_agent_turn_settlement_outcome_identity",
		clause: `OCTET_LENGTH(outcome_id) = 64
			AND BINARY outcome_id NOT REGEXP '[^0-9a-f]'
			AND OCTET_LENGTH(binding_id) = 64
			AND BINARY binding_id NOT REGEXP '[^0-9a-f]'
			AND OCTET_LENGTH(turn_id) BETWEEN 1 AND 256
			AND OCTET_LENGTH(settlement_key) = 86
			AND BINARY LEFT(settlement_key, 22) = 'wm:turn-settlement:v1:'
			AND BINARY SUBSTRING(settlement_key, 23) NOT REGEXP '[^0-9a-f]'
			AND fencing_token BETWEEN 1 AND 9223372036854775807`,
		enforced: true,
	},
	{
		table: SQLTurnSettlementOutcomeTable,
		name:  "chk_w_agent_turn_settlement_outcome_digests",
		clause: `OCTET_LENGTH(binding_digest) = 71
			AND BINARY LEFT(binding_digest, 7) = 'sha256:'
			AND BINARY SUBSTRING(binding_digest, 8) NOT REGEXP '[^0-9a-f]'
			AND OCTET_LENGTH(ledger_request_digest) = 71
			AND BINARY LEFT(ledger_request_digest, 7) = 'sha256:'
			AND BINARY SUBSTRING(ledger_request_digest, 8) NOT REGEXP '[^0-9a-f]'
			AND OCTET_LENGTH(outcome_digest) = 71
			AND BINARY LEFT(outcome_digest, 7) = 'sha256:'
			AND BINARY SUBSTRING(outcome_digest, 8) NOT REGEXP '[^0-9a-f]'
			AND (review_request_digest IS NULL OR (
				OCTET_LENGTH(review_request_digest) = 71
				AND BINARY LEFT(review_request_digest, 7) = 'sha256:'
				AND BINARY SUBSTRING(review_request_digest, 8) NOT REGEXP '[^0-9a-f]'
			))
			AND (resolution_request_digest IS NULL OR (
				OCTET_LENGTH(resolution_request_digest) = 71
				AND BINARY LEFT(resolution_request_digest, 7) = 'sha256:'
				AND BINARY SUBSTRING(resolution_request_digest, 8) NOT REGEXP '[^0-9a-f]'
			))`,
		enforced: true,
	},
	{
		table: SQLTurnSettlementOutcomeTable,
		name:  "chk_w_agent_turn_settlement_outcome_authorization",
		clause: `(authorization_kind = 'operation'
				AND attempt_id IS NOT NULL
				AND OCTET_LENGTH(attempt_id) BETWEEN 1 AND 64
				AND operation_id IS NOT NULL
				AND OCTET_LENGTH(operation_id) BETWEEN 1 AND 128)
			OR
			(authorization_kind = 'reconcile'
				AND attempt_id IS NULL AND operation_id IS NULL)`,
		enforced: true,
	},
	{
		table:    SQLTurnSettlementOutcomeTable,
		name:     "chk_w_agent_turn_settlement_outcome_terminal",
		clause:   "terminal_status IN ('completed', 'stopped', 'failed', 'timeout')",
		enforced: true,
	},
	{
		table: SQLTurnSettlementOutcomeTable,
		name:  "chk_w_agent_turn_settlement_outcome_amounts",
		clause: `reserved_units BETWEEN 0 AND 2147483647
			AND (used_units IS NULL OR used_units BETWEEN 0 AND reserved_units)
			AND refund_due BETWEEN 0 AND reserved_units
			AND reservation_state_version BETWEEN 1 AND 9223372036854775807`,
		enforced: true,
	},
	{
		table: SQLTurnSettlementOutcomeTable,
		name:  "chk_w_agent_turn_settlement_outcome_review_tuple",
		clause: `(requested_intent = 'review'
				AND review_id IS NOT NULL
				AND OCTET_LENGTH(review_id) BETWEEN 1 AND 64
				AND review_request_digest IS NOT NULL)
			OR
			(requested_intent IN ('finalize', 'release')
				AND review_id IS NULL AND review_request_digest IS NULL)`,
		enforced: true,
	},
	{
		table: SQLTurnSettlementOutcomeTable,
		name:  "chk_w_agent_turn_settlement_outcome_resolution_tuple",
		clause: `(resolution_id IS NULL AND resolution_request_digest IS NULL)
			OR
			(requested_intent = 'review'
				AND resolution_id IS NOT NULL
				AND OCTET_LENGTH(resolution_id) = 64
				AND BINARY resolution_id NOT REGEXP '[^0-9a-f]'
				AND resolution_request_digest IS NOT NULL
				AND status IN ('refund_pending', 'finalized'))`,
		enforced: true,
	},
	{
		table: SQLTurnSettlementOutcomeTable,
		name:  "chk_w_agent_turn_settlement_outcome_state_tuple",
		clause: `(status = 'review_held'
				AND requested_intent = 'review'
				AND used_units IS NULL
				AND refund_target IS NULL AND refund_due = 0
				AND resolution_id IS NULL AND resolution_request_digest IS NULL)
			OR
			(status = 'refund_pending'
				AND used_units IS NOT NULL
				AND refund_target IN ('finalized', 'released')
				AND refund_due > 0
				AND refund_due = reserved_units - used_units
				AND (
					(refund_target = 'finalized'
						AND requested_intent IN ('finalize', 'review')
						AND (requested_intent <> 'review' OR resolution_id IS NOT NULL))
					OR
					(refund_target = 'released'
						AND requested_intent = 'release' AND used_units = 0)
				))
			OR
			(status = 'finalized'
				AND requested_intent IN ('finalize', 'review')
				AND used_units IS NOT NULL
				AND refund_target IS NULL AND refund_due = 0
				AND (requested_intent <> 'review' OR resolution_id IS NOT NULL))
			OR
			(status = 'released'
				AND requested_intent = 'release' AND used_units = 0
				AND refund_target IS NULL AND refund_due = 0
				AND resolution_id IS NULL AND resolution_request_digest IS NULL)`,
		enforced: true,
	},
	{
		table:    SQLTurnSettlementOutcomeTable,
		name:     "chk_w_agent_turn_settlement_outcome_updated_time",
		clause:   "updated_at >= created_at",
		enforced: true,
	},
}

// ValidateMySQLRuntimeSchema performs the read-only compatibility preflight
// required by the durable Worker. It deliberately does not migrate or repair
// anything: missing/incompatible tables, required indexes, RESTRICT foreign
// keys or required CHECK predicates make startup fail closed.
func ValidateMySQLRuntimeSchema(ctx context.Context, query MySQLSchemaQuerier) error {
	if ctx == nil || query == nil {
		return ErrMySQLRuntimeSchemaUnavailable
	}
	var foreignKeyChecks, uniqueChecks, checkConstraintChecks, utcOffsetSeconds int
	var sessionTimeZone, transactionIsolation, version, versionComment string
	if err := query.QueryRowContext(ctx, `
		SELECT @@SESSION.foreign_key_checks,
		       @@SESSION.unique_checks,
		       @@SESSION.check_constraint_checks,
		       @@SESSION.time_zone,
		       @@SESSION.transaction_isolation,
		       TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(6), CURRENT_TIMESTAMP(6)),
		       @@version,
		       @@version_comment`).Scan(
		&foreignKeyChecks, &uniqueChecks, &checkConstraintChecks,
		&sessionTimeZone, &transactionIsolation, &utcOffsetSeconds, &version, &versionComment,
	); err != nil {
		return fmt.Errorf("%w: read enforcement state", ErrMySQLRuntimeSchemaSession)
	}
	if foreignKeyChecks != 1 || uniqueChecks != 1 || checkConstraintChecks != 1 {
		return fmt.Errorf("%w: enforcement is disabled", ErrMySQLRuntimeSchemaSession)
	}
	if sessionTimeZone != "+00:00" || utcOffsetSeconds != 0 {
		return fmt.Errorf("%w: session time zone must be UTC", ErrMySQLRuntimeSchemaSession)
	}
	if !supportedMySQLTransactionIsolation(transactionIsolation) {
		return fmt.Errorf("%w: unsupported transaction isolation", ErrMySQLRuntimeSchemaSession)
	}
	if !supportedMySQLRuntimeVersion(version, versionComment) {
		return fmt.Errorf("%w: unsupported database runtime", ErrMySQLRuntimeSchemaSession)
	}

	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT TABLE_NAME, ENGINE
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN (%s)`, mysqlRuntimeTablePlaceholders()), mysqlRuntimeTableArguments()...)
	if err != nil {
		return fmt.Errorf("%w: read table metadata", ErrMySQLRuntimeSchemaMetadata)
	}
	engines := make(map[string]string, len(mysqlRuntimeTables))
	for rows.Next() {
		var table, engine string
		if err := rows.Scan(&table, &engine); err != nil {
			_ = rows.Close()
			return fmt.Errorf("%w: decode table metadata", ErrMySQLRuntimeSchemaMetadata)
		}
		engines[table] = engine
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("%w: close table metadata", ErrMySQLRuntimeSchemaMetadata)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: iterate table metadata", ErrMySQLRuntimeSchemaMetadata)
	}
	for _, table := range mysqlRuntimeTables {
		engine, exists := engines[table]
		if !exists {
			return fmt.Errorf("%w: required table %s is missing", ErrMySQLRuntimeSchemaTables, table)
		}
		if !strings.EqualFold(engine, "InnoDB") {
			return fmt.Errorf("%w: required table %s must use InnoDB", ErrMySQLRuntimeSchemaTables, table)
		}
	}

	columns, err := readMySQLRuntimeColumns(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMySQLRuntimeSchemaMetadata, err)
	}
	for _, expected := range mysqlRuntimeColumns {
		actual, exists := columns[expected.table+"\x00"+expected.name]
		if !exists || !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("%w: required column %s.%s is missing or incompatible",
				ErrMySQLRuntimeSchemaColumns, expected.table, expected.name)
		}
	}
	for _, expected := range mysqlRuntimeRequiredColumns {
		if _, exists := columns[expected.table+"\x00"+expected.name]; !exists {
			return fmt.Errorf("%w: required column %s.%s is missing or incompatible",
				ErrMySQLRuntimeSchemaColumns, expected.table, expected.name)
		}
	}

	properties, err := readMySQLRuntimeColumnProperties(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMySQLRuntimeSchemaMetadata, err)
	}
	for _, expected := range mysqlRuntimeColumnProperties {
		actual, exists := properties[expected.table+"\x00"+expected.name]
		if !exists || !compatibleMySQLRuntimeColumnProperty(actual, expected) {
			return fmt.Errorf("%w: required column property %s.%s is missing or incompatible",
				ErrMySQLRuntimeSchemaColumns, expected.table, expected.name)
		}
	}

	indexes, err := readMySQLRuntimeIndexes(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMySQLRuntimeSchemaMetadata, err)
	}
	for _, expected := range mysqlRuntimeUniqueIndexes {
		actual, exists := indexes[expected.table+"\x00"+expected.name]
		if !exists || actual.hasPrefix || actual.invisible || !reflect.DeepEqual(actual.columns, expected.columns) {
			return fmt.Errorf("%w: required unique index %s.%s is missing or incompatible",
				ErrMySQLRuntimeSchemaIndexes, expected.table, expected.name)
		}
	}
	for _, expected := range mysqlRuntimePrimaryKeys {
		actual, exists := indexes[expected.table+"\x00"+expected.name]
		if !exists || actual.hasPrefix || actual.invisible || !reflect.DeepEqual(actual.columns, expected.columns) {
			return fmt.Errorf("%w: required primary key %s.%s is missing or incompatible",
				ErrMySQLRuntimeSchemaIndexes, expected.table, expected.name)
		}
	}
	ordinaryIndexes, err := readMySQLRuntimeOrdinaryIndexes(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMySQLRuntimeSchemaMetadata, err)
	}
	for _, expected := range mysqlRuntimeOrdinaryIndexes {
		actual, exists := ordinaryIndexes[expected.table+"\x00"+expected.name]
		if !exists || actual.hasPrefix || actual.invisible || !reflect.DeepEqual(actual.columns, expected.columns) {
			return fmt.Errorf("%w: required ordinary index %s.%s is missing or incompatible",
				ErrMySQLRuntimeSchemaIndexes, expected.table, expected.name)
		}
	}

	foreignKeys, err := readMySQLRuntimeForeignKeys(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMySQLRuntimeSchemaMetadata, err)
	}
	for _, expected := range mysqlRuntimeForeignKeys {
		actual, exists := foreignKeys[expected.name]
		if !exists || actual.table != expected.table || actual.referencedTable != expected.referencedTable ||
			!reflect.DeepEqual(actual.columns, expected.columns) || !reflect.DeepEqual(actual.referencedColumns, expected.referencedColumns) ||
			!strings.EqualFold(actual.updateRule, expected.updateRule) || !strings.EqualFold(actual.deleteRule, expected.deleteRule) {
			return fmt.Errorf("%w: required foreign key %s is missing or incompatible",
				ErrMySQLRuntimeSchemaForeignKeys, expected.name)
		}
	}

	checks, err := readMySQLRuntimeChecks(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMySQLRuntimeSchemaMetadata, err)
	}
	for _, expected := range mysqlRuntimeChecks {
		actual, exists := checks[expected.table+"\x00"+expected.name]
		if !exists || !actual.enforced ||
			canonicalMySQLCheckClause(actual.clause) != canonicalMySQLCheckClause(expected.clause) {
			return fmt.Errorf("%w: required check %s.%s is missing or incompatible",
				ErrMySQLRuntimeSchemaChecks, expected.table, expected.name)
		}
	}
	return nil
}

func supportedMySQLTransactionIsolation(value string) bool {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	normalized = strings.Join(strings.Fields(normalized), "-")
	switch normalized {
	case "READ-COMMITTED", "REPEATABLE-READ":
		return true
	default:
		return false
	}
}

func supportedMySQLRuntimeVersion(version, comment string) bool {
	combined := strings.ToLower(version + " " + comment)
	if strings.Contains(combined, "mariadb") {
		return false
	}
	numeric := strings.SplitN(strings.TrimSpace(version), "-", 2)[0]
	parts := strings.Split(numeric, ".")
	if len(parts) < 3 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil || patchErr != nil || major < 0 || minor < 0 || patch < 0 {
		return false
	}
	return major > 8 || (major == 8 && (minor > 0 || (minor == 0 && patch >= 19)))
}

func readMySQLRuntimeColumns(ctx context.Context, query MySQLSchemaQuerier) (map[string]mysqlRuntimeColumn, error) {
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE,
		       CHARACTER_SET_NAME, COLLATION_NAME, DATETIME_PRECISION
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN (%s)
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, mysqlRuntimeTablePlaceholders()), mysqlRuntimeTableArguments()...)
	if err != nil {
		return nil, errors.New("read column metadata failed")
	}
	defer rows.Close()
	columns := make(map[string]mysqlRuntimeColumn, len(mysqlRuntimeColumns))
	for rows.Next() {
		var column mysqlRuntimeColumn
		var nullable string
		var characterSet, collation sql.NullString
		var precision sql.NullInt64
		if err := rows.Scan(&column.table, &column.name, &column.dataType, &column.columnType,
			&nullable, &characterSet, &collation, &precision); err != nil {
			return nil, errors.New("decode column metadata failed")
		}
		column.dataType = strings.ToLower(column.dataType)
		column.columnType = strings.ToLower(column.columnType)
		column.nullable = strings.EqualFold(nullable, "YES")
		if characterSet.Valid {
			column.characterSet = strings.ToLower(characterSet.String)
		}
		if collation.Valid {
			column.collation = strings.ToLower(collation.String)
		}
		column.datetimePrecision = -1
		if precision.Valid {
			column.datetimePrecision = precision.Int64
		}
		columns[column.table+"\x00"+column.name] = column
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate column metadata failed")
	}
	return columns, nil
}

func readMySQLRuntimeColumnProperties(
	ctx context.Context,
	query MySQLSchemaQuerier,
) (map[string]mysqlRuntimeColumnProperty, error) {
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT TABLE_NAME, COLUMN_NAME, COLUMN_KEY, EXTRA, COLUMN_DEFAULT
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN (%s)
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, mysqlRuntimeTablePlaceholders()), mysqlRuntimeTableArguments()...)
	if err != nil {
		return nil, errors.New("read column property metadata failed")
	}
	defer rows.Close()
	properties := make(map[string]mysqlRuntimeColumnProperty, len(mysqlRuntimeColumnProperties))
	for rows.Next() {
		var property mysqlRuntimeColumnProperty
		var columnKey, extra string
		var defaultValue sql.NullString
		if err := rows.Scan(&property.table, &property.name, &columnKey, &extra, &defaultValue); err != nil {
			return nil, errors.New("decode column property metadata failed")
		}
		property.primaryKey = strings.EqualFold(strings.TrimSpace(columnKey), "PRI")
		property.autoIncrement = strings.Contains(strings.ToLower(extra), "auto_increment")
		property.defaultPinned = true
		property.defaultIsNull = !defaultValue.Valid
		if defaultValue.Valid {
			property.defaultValue = defaultValue.String
		}
		properties[property.table+"\x00"+property.name] = property
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate column property metadata failed")
	}
	return properties, nil
}

func compatibleMySQLRuntimeColumnProperty(actual, expected mysqlRuntimeColumnProperty) bool {
	if expected.primaryKey && !actual.primaryKey {
		return false
	}
	if expected.autoIncrement && !actual.autoIncrement {
		return false
	}
	if !expected.defaultPinned {
		return true
	}
	if actual.defaultIsNull != expected.defaultIsNull {
		return false
	}
	return expected.defaultIsNull || actual.defaultValue == expected.defaultValue
}

func readMySQLRuntimeIndexes(ctx context.Context, query MySQLSchemaQuerier) (map[string]mysqlRuntimeIndex, error) {
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, SUB_PART, IS_VISIBLE
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN (%s)
		  AND NON_UNIQUE = 0
		ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, mysqlRuntimeTablePlaceholders()), mysqlRuntimeTableArguments()...)
	if err != nil {
		return nil, errors.New("read unique-index metadata failed")
	}
	defer rows.Close()
	indexes := make(map[string]mysqlRuntimeIndex)
	for rows.Next() {
		var table, name, column, visible string
		var sequence int
		var subPart sql.NullInt64
		if err := rows.Scan(&table, &name, &sequence, &column, &subPart, &visible); err != nil {
			return nil, errors.New("decode unique-index metadata failed")
		}
		key := table + "\x00" + name
		index := indexes[key]
		index.table = table
		index.name = name
		index.columns = append(index.columns, column)
		index.hasPrefix = index.hasPrefix || subPart.Valid
		index.invisible = index.invisible || !strings.EqualFold(visible, "YES")
		indexes[key] = index
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate unique-index metadata failed")
	}
	return indexes, nil
}

func readMySQLRuntimeOrdinaryIndexes(ctx context.Context, query MySQLSchemaQuerier) (map[string]mysqlRuntimeIndex, error) {
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, SUB_PART, IS_VISIBLE
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN (%s)
		  AND NON_UNIQUE = 1
		ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, mysqlRuntimeTablePlaceholders()), mysqlRuntimeTableArguments()...)
	if err != nil {
		return nil, errors.New("read ordinary-index metadata failed")
	}
	defer rows.Close()
	indexes := make(map[string]mysqlRuntimeIndex)
	for rows.Next() {
		var table, name, column, visible string
		var sequence int
		var subPart sql.NullInt64
		if err := rows.Scan(&table, &name, &sequence, &column, &subPart, &visible); err != nil {
			return nil, errors.New("decode ordinary-index metadata failed")
		}
		key := table + "\x00" + name
		index := indexes[key]
		index.table = table
		index.name = name
		index.columns = append(index.columns, column)
		index.hasPrefix = index.hasPrefix || subPart.Valid
		index.invisible = index.invisible || !strings.EqualFold(visible, "YES")
		indexes[key] = index
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate ordinary-index metadata failed")
	}
	return indexes, nil
}

func readMySQLRuntimeForeignKeys(ctx context.Context, query MySQLSchemaQuerier) (map[string]mysqlRuntimeForeignKey, error) {
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME,
		       k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
		       k.ORDINAL_POSITION, r.UPDATE_RULE, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE AS k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS AS r
		  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
		 AND r.TABLE_NAME = k.TABLE_NAME
		 AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
		WHERE k.CONSTRAINT_SCHEMA = DATABASE()
		  AND k.TABLE_NAME IN (%s)
		  AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, mysqlRuntimeTablePlaceholders()), mysqlRuntimeTableArguments()...)
	if err != nil {
		return nil, errors.New("read foreign-key metadata failed")
	}
	defer rows.Close()
	foreignKeys := make(map[string]mysqlRuntimeForeignKey)
	for rows.Next() {
		var table, name, column, referencedTable, referencedColumn, updateRule, deleteRule string
		var ordinal int
		if err := rows.Scan(&table, &name, &column, &referencedTable, &referencedColumn, &ordinal, &updateRule, &deleteRule); err != nil {
			return nil, errors.New("decode foreign-key metadata failed")
		}
		foreignKey := foreignKeys[name]
		foreignKey.table = table
		foreignKey.name = name
		foreignKey.referencedTable = referencedTable
		foreignKey.updateRule = updateRule
		foreignKey.deleteRule = deleteRule
		foreignKey.columns = append(foreignKey.columns, column)
		foreignKey.referencedColumns = append(foreignKey.referencedColumns, referencedColumn)
		foreignKeys[name] = foreignKey
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate foreign-key metadata failed")
	}
	return foreignKeys, nil
}

func readMySQLRuntimeChecks(ctx context.Context, query MySQLSchemaQuerier) (map[string]mysqlRuntimeCheck, error) {
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`
		SELECT tc.TABLE_NAME, tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE, tc.ENFORCED
		FROM information_schema.TABLE_CONSTRAINTS AS tc
		JOIN information_schema.CHECK_CONSTRAINTS AS cc
		  ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
		 AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
		  AND tc.TABLE_NAME IN (%s)
		  AND tc.CONSTRAINT_TYPE = 'CHECK'
		ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME`, mysqlRuntimeTablePlaceholders()), mysqlRuntimeTableArguments()...)
	if err != nil {
		return nil, errors.New("read check-constraint metadata failed")
	}
	defer rows.Close()
	checks := make(map[string]mysqlRuntimeCheck, len(mysqlRuntimeChecks))
	for rows.Next() {
		var check mysqlRuntimeCheck
		var enforced string
		if err := rows.Scan(&check.table, &check.name, &check.clause, &enforced); err != nil {
			return nil, errors.New("decode check-constraint metadata failed")
		}
		check.enforced = strings.EqualFold(enforced, "YES")
		checks[check.table+"\x00"+check.name] = check
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate check-constraint metadata failed")
	}
	return checks, nil
}

func mysqlRuntimeTablePlaceholders() string {
	return strings.TrimSuffix(strings.Repeat("?, ", len(mysqlRuntimeTables)), ", ")
}

func mysqlRuntimeTableArguments() []any {
	arguments := make([]any, len(mysqlRuntimeTables))
	for index, table := range mysqlRuntimeTables {
		arguments[index] = table
	}
	return arguments
}

func canonicalMySQLCheckClause(clause string) string {
	var canonical strings.Builder
	canonical.Grow(len(clause))
	for index := 0; index < len(clause); {
		if clause[index] == '\'' {
			// SQL keywords and unquoted identifiers are case-insensitive, but a
			// quoted literal can feed a BINARY comparison. Preserve its bytes
			// exactly so 'finalized' cannot be confused with 'FINALIZED'.
			canonical.WriteByte(clause[index])
			index++
			for index < len(clause) {
				character := clause[index]
				canonical.WriteByte(character)
				index++
				if character == '\\' && index < len(clause) {
					canonical.WriteByte(clause[index])
					index++
					continue
				}
				if character != '\'' {
					continue
				}
				if index < len(clause) && clause[index] == '\'' {
					canonical.WriteByte(clause[index])
					index++
					continue
				}
				break
			}
			continue
		}
		if introducerLength := mysqlCheckCharsetIntroducerLength(clause[index:]); introducerLength > 0 {
			index += introducerLength
			continue
		}
		switch clause[index] {
		case '`', ' ', '\t', '\n', '\r', '\f', '\v':
			index++
			continue
		}
		character := clause[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		canonical.WriteByte(character)
		index++
	}
	clause = canonical.String()
	for mysqlCheckHasRedundantOuterParentheses(clause) {
		clause = clause[1 : len(clause)-1]
	}
	return clause
}

func mysqlCheckCharsetIntroducerLength(clause string) int {
	for _, introducer := range []string{
		"_utf8mb4", "_utf8mb3", "_utf8", "_ascii", "_latin1", "_binary",
	} {
		if len(clause) > len(introducer) && clause[len(introducer)] == '\'' &&
			strings.EqualFold(clause[:len(introducer)], introducer) {
			return len(introducer)
		}
	}
	return 0
}

func mysqlCheckHasRedundantOuterParentheses(clause string) bool {
	if len(clause) < 2 || clause[0] != '(' || clause[len(clause)-1] != ')' {
		return false
	}
	depth := 0
	quoted := false
	for index := 0; index < len(clause); index++ {
		switch clause[index] {
		case '\\':
			if quoted && index+1 < len(clause) {
				index++
			}
		case '\'':
			if quoted && index+1 < len(clause) && clause[index+1] == '\'' {
				index++
				continue
			}
			quoted = !quoted
		case '(':
			if !quoted {
				depth++
			}
		case ')':
			if !quoted {
				depth--
				if depth == 0 && index != len(clause)-1 {
					return false
				}
			}
		}
	}
	return !quoted && depth == 0
}
