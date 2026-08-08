package agentturn

import (
	"context"
	"crypto/rand"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/spf13/viper"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"server/config"
	agentv1 "server/contracts/agent/v1"
)

const (
	mysqlContractEnabledEnv          = "WORKMAX_AGENTTURN_MYSQL_CONTRACT"
	mysqlContractConfigEnv           = "WORKMAX_AGENTTURN_MYSQL_CONFIG"
	mysqlContractDSNEnv              = "WORKMAX_AGENTTURN_MYSQL_DSN"
	mysqlContractAllowDirectDSNEnv   = "WORKMAX_AGENTTURN_MYSQL_ALLOW_DIRECT_DSN"
	mysqlContractAllowPlaintextEnv   = "WORKMAX_AGENTTURN_MYSQL_ALLOW_PLAINTEXT"
	mysqlContractAllowInsecureTLSEnv = "WORKMAX_AGENTTURN_MYSQL_ALLOW_INSECURE_TLS"
	mysqlContractIOTimeout           = 10 * time.Second
)

var mysqlContractTables = []string{
	SQLTurnTable,
	SQLTurnEventTable,
	SQLTurnAttemptTable,
	SQLTurnOperationTable,
	SQLEffectOutboxTable,
	SQLProviderUsageJournalTable,
	SQLSettlementReviewTable,
	SQLSettlementReviewUsageEvidenceTable,
	SQLSettlementReviewUsageEvidenceSourceTable,
	SQLSettlementReviewResolutionTable,
	SQLTurnReservationBindingTable,
	SQLTurnSettlementOutcomeTable,
}

type mysqlContractSettings struct {
	dsn string
}

func loadMySQLContractSettings() (mysqlContractSettings, bool, error) {
	enabled := strings.TrimSpace(os.Getenv(mysqlContractEnabledEnv))
	explicitDSN := strings.TrimSpace(os.Getenv(mysqlContractDSNEnv))
	configPath := strings.TrimSpace(os.Getenv(mysqlContractConfigEnv))

	// Merely having a local config file or a CI DSN cannot enable writes. Every
	// real-database run requires the common enable bit; a direct DSN additionally
	// requires a CI-only escape-hatch bit below.
	if enabled == "" && explicitDSN == "" && configPath == "" {
		return mysqlContractSettings{}, false, nil
	}
	if enabled != "1" {
		return mysqlContractSettings{}, false, errors.New("MySQL contract enable flag must be 1")
	}
	if explicitDSN != "" && configPath != "" {
		return mysqlContractSettings{}, false, errors.New("MySQL contract DSN and config path are mutually exclusive")
	}
	if enabled == "1" && explicitDSN == "" && configPath == "" {
		return mysqlContractSettings{}, false, errors.New("MySQL contract opt-in requires a config path or explicit CI DSN")
	}
	if explicitDSN != "" && strings.TrimSpace(os.Getenv(mysqlContractAllowDirectDSNEnv)) != "1" {
		return mysqlContractSettings{}, false, errors.New("direct MySQL contract DSN requires the isolated-CI opt-in")
	}

	dsn := explicitDSN
	if dsn == "" {
		if !filepath.IsAbs(configPath) {
			return mysqlContractSettings{}, false, errors.New("MySQL contract config path must be absolute")
		}
		info, err := os.Stat(configPath)
		if err != nil || !info.Mode().IsRegular() {
			return mysqlContractSettings{}, false, errors.New("MySQL contract config file is not a readable regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return mysqlContractSettings{}, false, errors.New("MySQL contract config file must not be group/world accessible")
		}

		reader := viper.New()
		reader.SetConfigFile(configPath)
		if err := reader.ReadInConfig(); err != nil {
			return mysqlContractSettings{}, false, errors.New("read MySQL contract config failed")
		}
		var mysqlConfig config.GormMysql
		if err := reader.UnmarshalKey("mysql_system", &mysqlConfig); err != nil {
			return mysqlContractSettings{}, false, errors.New("decode mysql_system contract config failed")
		}
		if strings.TrimSpace(mysqlConfig.Path) == "" || strings.TrimSpace(mysqlConfig.Port) == "" ||
			strings.TrimSpace(mysqlConfig.Dbname) == "" || strings.TrimSpace(mysqlConfig.Username) == "" {
			return mysqlContractSettings{}, false, errors.New("mysql_system contract config is incomplete")
		}
		if err := validateMySQLContractRollout(reader); err != nil {
			return mysqlContractSettings{}, false, err
		}
		dsn = mysqlConfig.Dsn()
	}

	parsed, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return mysqlContractSettings{}, false, errors.New("parse MySQL contract connection settings failed")
	}
	if strings.TrimSpace(parsed.DBName) == "" {
		return mysqlContractSettings{}, false, errors.New("MySQL contract database name is empty")
	}
	if explicitDSN != "" && !mysqlContractDatabaseLooksIsolated(parsed.DBName) {
		return mysqlContractSettings{}, false, errors.New("direct MySQL contract DSN must name an isolated test, contract or CI database")
	}
	for name, value := range parsed.Params {
		if strings.EqualFold(strings.TrimSpace(name), "foreign_key_checks") && strings.TrimSpace(value) != "1" {
			return mysqlContractSettings{}, false, errors.New("MySQL contract DSN must not disable foreign-key checks")
		}
	}
	if mysqlContractIsRemote(parsed) && (parsed.TLS == nil || parsed.AllowFallbackToPlaintext) {
		if strings.TrimSpace(os.Getenv(mysqlContractAllowPlaintextEnv)) != "1" {
			return mysqlContractSettings{}, false, errors.New("remote MySQL contract requires TLS or explicit plaintext opt-in")
		}
	}
	if mysqlContractIsRemote(parsed) && parsed.TLS != nil && parsed.TLS.InsecureSkipVerify &&
		strings.TrimSpace(os.Getenv(mysqlContractAllowInsecureTLSEnv)) != "1" {
		return mysqlContractSettings{}, false, errors.New("remote MySQL contract rejects insecure TLS without a separate opt-in")
	}
	// The long-running Server configuration may intentionally omit driver
	// deadlines. An explicit contract run must remain bounded even when the
	// selected development database or network is unavailable.
	if parsed.Timeout == 0 {
		parsed.Timeout = mysqlContractIOTimeout
	}
	if parsed.ReadTimeout == 0 {
		parsed.ReadTimeout = mysqlContractIOTimeout
	}
	if parsed.WriteTimeout == 0 {
		parsed.WriteTimeout = mysqlContractIOTimeout
	}
	return mysqlContractSettings{dsn: parsed.FormatDSN()}, true, nil
}

func mysqlContractDatabaseLooksIsolated(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, marker := range []string{"test", "contract", "ci"} {
		if normalized == marker || strings.HasPrefix(normalized, marker+"_") ||
			strings.HasSuffix(normalized, "_"+marker) || strings.Contains(normalized, "_"+marker+"_") {
			return true
		}
	}
	return false
}

func validateMySQLContractRollout(reader *viper.Viper) error {
	if reader == nil {
		return errors.New("MySQL contract rollout reader is nil")
	}
	var raw *config.AgentPlatformRollout
	if reader.IsSet("agent_platform_rollout") {
		configured := &config.AgentPlatformRollout{}
		if err := reader.UnmarshalKey("agent_platform_rollout", configured); err != nil {
			return errors.New("decode agent_platform_rollout contract config failed")
		}
		if err := configured.Validate(); err != nil {
			return errors.New("agent_platform_rollout contract config is invalid")
		}
		raw = configured
	}
	effective := config.EffectiveAgentPlatformRollout(raw)
	if effective.Durable.PublicAPI != config.DurablePublicAPIOff ||
		effective.Durable.Worker != config.DurableWorkerOff ||
		effective.Durable.AllowNewStarts ||
		effective.Desktop.AgentTransport != config.DesktopAgentTransportLegacy {
		return errors.New("agent_platform_rollout must disable durable traffic for MySQL contract tests")
	}
	return nil
}

func mysqlContractIsRemote(connection *drivermysql.Config) bool {
	if connection == nil || connection.Net == "unix" {
		return false
	}
	host, _, err := net.SplitHostPort(connection.Addr)
	if err != nil {
		return true
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func mysqlContractSettingsForTest(t *testing.T) mysqlContractSettings {
	t.Helper()
	settings, enabled, err := loadMySQLContractSettings()
	if err != nil {
		t.Fatalf("unsafe MySQL contract settings: %v", err)
	}
	if !enabled {
		t.Skip("MySQL contract is disabled; use WORKMAX_AGENTTURN_MYSQL_CONTRACT=1 with an absolute config path (direct DSN additionally requires the isolated-CI opt-in)")
	}
	return settings
}

func openMySQLContractDatabase(t *testing.T, settings mysqlContractSettings) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(gormmysql.Open(settings.dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal("open MySQL contract database failed")
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal("obtain MySQL contract connection pool failed")
	}
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(2 * time.Minute)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error("close MySQL contract connection pool failed")
		}
	})
	return database
}

func mysqlContractPreflight(t *testing.T, database *gorm.DB) {
	t.Helper()
	pool, err := database.DB()
	if err != nil {
		t.Fatal("obtain MySQL contract preflight connection failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ValidateMySQLRuntimeSchema(ctx, pool); err != nil {
		t.Fatalf("MySQL contract schema preflight failed: %v", err)
	}
}

func mysqlContractSuffix(t *testing.T, label string) string {
	t.Helper()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal("generate MySQL contract namespace failed")
	}
	return label + "_" + hex.EncodeToString(random)
}

func mysqlContractAssertNamespaceEmpty(t *testing.T, database *gorm.DB, turn Turn) {
	t.Helper()
	for _, table := range mysqlContractTables {
		if count := mysqlContractCount(t, database, table, "turn_id = ?", turn.ID); count != 0 {
			t.Fatalf("MySQL contract namespace collision in %s", table)
		}
	}
	if count := mysqlContractCount(t, database, SQLTurnTable, "principal_id = ?", turn.PrincipalID); count != 0 {
		t.Fatal("MySQL contract principal marker already exists")
	}
}

func mysqlContractOwnedCleanup(t *testing.T, database *gorm.DB, turn Turn) func() {
	t.Helper()
	var mu sync.Mutex
	complete := false
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if complete {
			return
		}
		if err := database.Transaction(func(tx *gorm.DB) error {
			var ownerCount int64
			if err := tx.Table(SQLTurnTable).
				Where("turn_id = ? AND principal_id = ?", turn.ID, turn.PrincipalID).
				Count(&ownerCount).Error; err != nil || ownerCount != 1 {
				return errors.New("contract ownership marker is missing")
			}
			if err := tx.Table(SQLTurnTable).
				Where("turn_id = ? AND principal_id = ?", turn.ID, turn.PrincipalID).
				UpdateColumn("active_attempt_id", nil).Error; err != nil {
				return errors.New("clear owned contract attempt binding failed")
			}
			for _, statement := range []struct {
				table string
				row   any
			}{
				{SQLTurnSettlementOutcomeTable, &struct{}{}},
				{SQLSettlementReviewUsageEvidenceSourceTable, &struct{}{}},
				{SQLSettlementReviewResolutionTable, &sqlSettlementReviewResolutionRow{}},
				{SQLSettlementReviewUsageEvidenceTable, &sqlSettlementReviewUsageEvidenceRow{}},
				{SQLProviderUsageJournalTable, &sqlProviderUsageJournalRow{}},
				{SQLSettlementReviewTable, &sqlSettlementReviewRow{}},
				{SQLEffectOutboxTable, &sqlEffectOutboxRow{}},
				{SQLTurnOperationTable, &sqlTurnOperationRow{}},
				{SQLTurnEventTable, &sqlTurnEventRow{}},
				{SQLTurnAttemptTable, &sqlTurnAttemptRow{}},
				{SQLTurnReservationBindingTable, &struct{}{}},
			} {
				if result := tx.Table(statement.table).Where("turn_id = ?", turn.ID).Delete(statement.row); result.Error != nil {
					return errors.New("delete owned contract child rows failed")
				}
			}
			deleted := tx.Table(SQLTurnTable).
				Where("turn_id = ? AND principal_id = ?", turn.ID, turn.PrincipalID).
				Delete(&sqlTurnRow{})
			if deleted.Error != nil || deleted.RowsAffected != 1 {
				return errors.New("delete owned contract turn failed")
			}
			for _, table := range mysqlContractTables {
				var count int64
				if err := tx.Table(table).Where("turn_id = ?", turn.ID).Count(&count).Error; err != nil {
					return errors.New("verify owned contract cleanup failed")
				}
				if count != 0 {
					return fmt.Errorf("contract cleanup left rows in %s", table)
				}
			}
			var principalCount int64
			if err := tx.Table(SQLTurnTable).Where("principal_id = ?", turn.PrincipalID).Count(&principalCount).Error; err != nil {
				return errors.New("verify contract principal cleanup failed")
			}
			if principalCount != 0 {
				return errors.New("contract cleanup left the principal marker")
			}
			return nil
		}); err != nil {
			t.Errorf("FK-safe MySQL contract cleanup failed: %v", err)
			return
		}
		complete = true
	}
}

func mysqlContractCount(t *testing.T, database *gorm.DB, table, predicate string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := database.Table(table).Where(predicate, args...).Count(&count).Error; err != nil {
		t.Fatalf("count MySQL contract rows in %s failed", table)
	}
	return count
}

func TestMySQLRuntimeSchemaContractIsPinned(t *testing.T) {
	if got := len(mysqlRuntimeTables); got != 19 {
		t.Fatalf("MySQL runtime table contract = %d tables, want 19", got)
	}
	if got := len(mysqlRuntimeUniqueIndexes); got != 49 {
		t.Fatalf("MySQL runtime unique-index contract = %d indexes, want 49", got)
	}
	if got := len(mysqlRuntimeOrdinaryIndexes); got != 7 {
		t.Fatalf("MySQL runtime ordinary-index contract = %d indexes, want 7", got)
	}
	if got := len(mysqlRuntimeForeignKeys); got != 25 {
		t.Fatalf("MySQL runtime foreign-key contract = %d keys, want 25", got)
	}
	if got := len(mysqlRuntimeChecks); got != 34 {
		t.Fatalf("MySQL runtime CHECK contract = %d checks, want 34", got)
	}
	if got := len(mysqlRuntimeColumns); got != 98 {
		t.Fatalf("MySQL runtime exact-column contract = %d columns, want 98", got)
	}
	if got := len(mysqlRuntimeRequiredColumns); got != 19 {
		t.Fatalf("MySQL runtime presence-column contract = %d columns, want 19", got)
	}
	if got := len(mysqlRuntimeColumnProperties); got != 16 {
		t.Fatalf("MySQL runtime column-property contract = %d properties, want 16", got)
	}
	if got := len(mysqlRuntimePrimaryKeys); got != 6 {
		t.Fatalf("MySQL runtime primary-key contract = %d keys, want 6", got)
	}

	tables := make(map[string]struct{}, len(mysqlRuntimeTables))
	for _, table := range mysqlRuntimeTables {
		if table == "" {
			t.Fatal("MySQL runtime table contract contains an empty table name")
		}
		if _, duplicate := tables[table]; duplicate {
			t.Fatalf("MySQL runtime table contract repeats %s", table)
		}
		tables[table] = struct{}{}
	}
	columnNames := make(map[string]struct{}, len(mysqlRuntimeColumns))
	for _, column := range mysqlRuntimeColumns {
		key := column.table + "\x00" + column.name
		if _, knownTable := tables[column.table]; !knownTable || column.name == "" ||
			column.dataType == "" || column.columnType == "" {
			t.Fatalf("incompatible MySQL runtime column declaration: %+v", column)
		}
		if _, duplicate := columnNames[key]; duplicate {
			t.Fatalf("MySQL runtime column contract repeats %s.%s", column.table, column.name)
		}
		columnNames[key] = struct{}{}
	}
	for _, column := range mysqlRuntimeRequiredColumns {
		key := column.table + "\x00" + column.name
		if _, knownTable := tables[column.table]; !knownTable || column.name == "" {
			t.Fatalf("incompatible MySQL runtime required-column declaration: %+v", column)
		}
		if _, duplicate := columnNames[key]; duplicate {
			t.Fatalf("MySQL runtime column contract repeats %s.%s", column.table, column.name)
		}
		columnNames[key] = struct{}{}
	}
	propertyNames := make(map[string]struct{}, len(mysqlRuntimeColumnProperties))
	for _, property := range mysqlRuntimeColumnProperties {
		key := property.table + "\x00" + property.name
		if _, knownColumn := columnNames[key]; !knownColumn ||
			(!property.primaryKey && !property.autoIncrement && !property.defaultPinned) ||
			(property.autoIncrement && !property.primaryKey) ||
			(property.defaultIsNull && property.defaultValue != "") {
			t.Fatalf("incompatible MySQL runtime column-property declaration: %+v", property)
		}
		if _, duplicate := propertyNames[key]; duplicate {
			t.Fatalf("MySQL runtime column-property contract repeats %s.%s", property.table, property.name)
		}
		propertyNames[key] = struct{}{}
	}
	indexNames := make(map[string]struct{}, len(mysqlRuntimeUniqueIndexes))
	for _, index := range mysqlRuntimePrimaryKeys {
		key := index.table + "\x00" + index.name
		if _, knownTable := tables[index.table]; !knownTable || index.name != "PRIMARY" ||
			!reflect.DeepEqual(index.columns, []string{"id"}) || index.hasPrefix || index.invisible {
			t.Fatalf("incompatible MySQL runtime primary-key declaration: %+v", index)
		}
		if _, duplicate := indexNames[key]; duplicate {
			t.Fatalf("MySQL runtime primary-key contract repeats %s.%s", index.table, index.name)
		}
		indexNames[key] = struct{}{}
	}
	for _, index := range mysqlRuntimeUniqueIndexes {
		key := index.table + "\x00" + index.name
		if _, knownTable := tables[index.table]; !knownTable || index.name == "" || len(index.name) > 64 ||
			len(index.columns) == 0 || index.hasPrefix || index.invisible {
			t.Fatalf("incompatible MySQL runtime unique-index declaration: %+v", index)
		}
		if _, duplicate := indexNames[key]; duplicate {
			t.Fatalf("MySQL runtime unique-index contract repeats %s.%s", index.table, index.name)
		}
		indexNames[key] = struct{}{}
	}
	for _, index := range mysqlRuntimeOrdinaryIndexes {
		key := index.table + "\x00" + index.name
		if _, knownTable := tables[index.table]; !knownTable || index.name == "" || len(index.name) > 64 ||
			len(index.columns) == 0 || index.hasPrefix || index.invisible {
			t.Fatalf("incompatible MySQL runtime ordinary-index declaration: %+v", index)
		}
		if _, duplicate := indexNames[key]; duplicate {
			t.Fatalf("MySQL runtime index contract repeats %s.%s", index.table, index.name)
		}
		indexNames[key] = struct{}{}
	}
	foreignKeyNames := make(map[string]struct{}, len(mysqlRuntimeForeignKeys))
	for _, foreignKey := range mysqlRuntimeForeignKeys {
		_, knownTable := tables[foreignKey.table]
		_, knownParent := tables[foreignKey.referencedTable]
		if !knownTable || !knownParent || foreignKey.name == "" || len(foreignKey.name) > 64 || len(foreignKey.columns) == 0 ||
			len(foreignKey.columns) != len(foreignKey.referencedColumns) ||
			foreignKey.updateRule != "RESTRICT" || foreignKey.deleteRule != "RESTRICT" {
			t.Fatalf("incompatible MySQL runtime foreign-key declaration: %+v", foreignKey)
		}
		if _, duplicate := foreignKeyNames[foreignKey.name]; duplicate {
			t.Fatalf("MySQL runtime foreign-key contract repeats %s", foreignKey.name)
		}
		foreignKeyNames[foreignKey.name] = struct{}{}
	}
	checkNames := make(map[string]struct{}, len(mysqlRuntimeChecks))
	for _, check := range mysqlRuntimeChecks {
		key := check.table + "\x00" + check.name
		if _, knownTable := tables[check.table]; !knownTable || check.name == "" || len(check.name) > 64 ||
			canonicalMySQLCheckClause(check.clause) == "" || !check.enforced {
			t.Fatalf("incompatible MySQL runtime CHECK declaration: %+v", check)
		}
		if _, duplicate := checkNames[key]; duplicate {
			t.Fatalf("MySQL runtime CHECK contract repeats %s.%s", check.table, check.name)
		}
		checkNames[key] = struct{}{}
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockAcceptsRequiredChecks(t *testing.T) {
	database := newMySQLRuntimeSchemaSQLMock(t, nil)
	if err := ValidateMySQLRuntimeSchema(context.Background(), database); err != nil {
		t.Fatalf("complete MySQL runtime schema mock: %v", err)
	}
}

func TestCanonicalMySQLCheckClausePreservesQuotedLiteralBytes(t *testing.T) {
	expected := canonicalMySQLCheckClause(" ( BINARY `status` = _utf8mb4'finalized' ) ")
	equivalent := canonicalMySQLCheckClause("binary status=_ascii'finalized'")
	if expected != equivalent {
		t.Fatalf("keyword/collation-printer normalization mismatch: %q != %q", expected, equivalent)
	}
	caseDrift := canonicalMySQLCheckClause("binary status=_utf8mb4'FINALIZED'")
	if expected == caseDrift {
		t.Fatalf("binary literal case drift collapsed to %q", expected)
	}
	spaced := canonicalMySQLCheckClause("reason = 'usage unknown'")
	collapsed := canonicalMySQLCheckClause("reason = 'usageunknown'")
	if spaced == collapsed {
		t.Fatalf("literal whitespace drift collapsed to %q", spaced)
	}
	if got := canonicalMySQLCheckClause("value = '_UTF8MB4 A''B'"); !strings.Contains(got, "'_UTF8MB4 A''B'") {
		t.Fatalf("literal bytes were rewritten: %q", got)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsUnsafeSession(t *testing.T) {
	tests := map[string]func(*mysqlRuntimeSchemaSQLMockMetadata){
		"foreign keys disabled": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.foreignKeyChecks = 0
		},
		"unique checks disabled": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.uniqueChecks = 0
		},
		"checks disabled": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.checkConstraintChecks = 0
		},
		"non UTC offset": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.utcOffsetSeconds = 8 * 60 * 60
		},
		"named zone with current UTC offset": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.sessionTimeZone = "Europe/London"
		},
		"serializable isolation": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.transactionIsolation = "SERIALIZABLE"
		},
		"read uncommitted isolation": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.transactionIsolation = "READ-UNCOMMITTED"
		},
		"old MySQL": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.version = "8.0.18"
		},
		"MariaDB": func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
			metadata.version = "10.11.8-MariaDB"
			metadata.versionComment = "MariaDB Server"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
				mutate(&metadata)
				return metadata
			})
			if err := ValidateMySQLRuntimeSchema(context.Background(), database); !errors.Is(err, ErrMySQLRuntimeSchemaSession) {
				t.Fatalf("unsafe session error = %v", err)
			}
		})
	}
}

func TestSupportedMySQLTransactionIsolation(t *testing.T) {
	for value, want := range map[string]bool{
		"READ-COMMITTED": true, "REPEATABLE-READ": true, "read committed": true,
		"read_committed": true, "SERIALIZABLE": false, "READ-UNCOMMITTED": false, "": false,
	} {
		if got := supportedMySQLTransactionIsolation(value); got != want {
			t.Fatalf("supportedMySQLTransactionIsolation(%q) = %t, want %t", value, got, want)
		}
	}
}

func TestSupportedMySQLRuntimeVersion(t *testing.T) {
	for version, want := range map[string]bool{
		"8.0.18": false, "8.0.19": true, "8.4.1": true, "9.0.0": true,
		"10.11.8-MariaDB": false, "8.0": false, "invalid": false,
	} {
		if got := supportedMySQLRuntimeVersion(version, "MySQL Community Server - GPL"); got != want {
			t.Fatalf("supportedMySQLRuntimeVersion(%q) = %t, want %t", version, got, want)
		}
	}
	if supportedMySQLRuntimeVersion("8.0.36", "MariaDB compatibility") {
		t.Fatal("MariaDB version comment was accepted")
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsMissingProviderUsageTable(t *testing.T) {
	missing := SQLSettlementReviewUsageEvidenceSourceTable
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index, table := range metadata.tables {
			if table == missing {
				metadata.tables = append(metadata.tables[:index], metadata.tables[index+1:]...)
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaTables) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing Provider schema table error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaTables, missing)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsMissingSettlementOutcomeTable(t *testing.T) {
	missing := SQLTurnSettlementOutcomeTable
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index, table := range metadata.tables {
			if table == missing {
				metadata.tables = append(metadata.tables[:index], metadata.tables[index+1:]...)
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaTables) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing Settlement Outcome table error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaTables, missing)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsMissingCreditAllocationTable(t *testing.T) {
	missing := mysqlCreditReservationAllocationTable
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index, table := range metadata.tables {
			if table == missing {
				metadata.tables = append(metadata.tables[:index], metadata.tables[index+1:]...)
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaTables) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing Credit allocation table error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsMissingCreditsOwnerTable(t *testing.T) {
	missing := mysqlUserTable
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index, table := range metadata.tables {
			if table == missing {
				metadata.tables = append(metadata.tables[:index], metadata.tables[index+1:]...)
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaTables) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing Credits owner table error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsMissingLegacyOwnerColumn(t *testing.T) {
	missing := mysqlRuntimeRequiredColumn{table: mysqlUserTable, name: "member_subscription"}
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index, column := range metadata.requiredColumns {
			if column == missing {
				metadata.requiredColumns = append(metadata.requiredColumns[:index], metadata.requiredColumns[index+1:]...)
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), missing.name) {
		t.Fatalf("missing legacy owner column error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsProjectBudgetColumnDrift(t *testing.T) {
	tests := map[string]struct {
		column string
		mutate func(*mysqlRuntimeColumn)
	}{
		"cap type": {
			column: "budget_credits_cap",
			mutate: func(column *mysqlRuntimeColumn) {
				column.dataType = "bigint"
				column.columnType = "bigint"
			},
		},
		"used nullable": {
			column: "budget_credits_used",
			mutate: func(column *mysqlRuntimeColumn) {
				column.nullable = true
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
				for index := range metadata.columns {
					column := &metadata.columns[index]
					if column.table == mysqlGlobalProjectTable && column.name == test.column {
						test.mutate(column)
						break
					}
				}
				return metadata
			})
			err := ValidateMySQLRuntimeSchema(context.Background(), database)
			if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), test.column) {
				t.Fatalf("project budget column drift error = %v", err)
			}
		})
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedColumnProperties(t *testing.T) {
	tests := map[string]struct {
		index  int
		mutate func(*mysqlRuntimeColumnProperty)
	}{
		"primary key": {index: 0, mutate: func(property *mysqlRuntimeColumnProperty) {
			property.primaryKey = false
		}},
		"auto increment": {index: 0, mutate: func(property *mysqlRuntimeColumnProperty) {
			property.autoIncrement = false
		}},
		"financial default": {index: 2, mutate: func(property *mysqlRuntimeColumnProperty) {
			property.defaultValue = "1"
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
				test.mutate(&metadata.columnProperties[test.index])
				return metadata
			})
			err := ValidateMySQLRuntimeSchema(context.Background(), database)
			if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) {
				t.Fatalf("tampered column property error = %v", err)
			}
		})
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsCompositeOwnerPrimaryKey(t *testing.T) {
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		metadata.primaryKeys[0].columns = []string{"id", "uid"}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaIndexes) || !strings.Contains(err.Error(), "PRIMARY") {
		t.Fatalf("composite owner primary-key error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedOrdinaryOwnerIndex(t *testing.T) {
	want := mysqlRuntimeOrdinaryIndexes[0]
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		metadata.ordinaryIndexes[0].columns = []string{"id", "uid"}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaIndexes) || !strings.Contains(err.Error(), want.name) {
		t.Fatalf("tampered ordinary owner index error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsInvisibleOwnerIndex(t *testing.T) {
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		metadata.ordinaryIndexes[0].invisible = true
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaIndexes) {
		t.Fatalf("invisible owner index error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRequiresLegacyFinancialOwnerHardening(t *testing.T) {
	tests := []struct {
		name   string
		kind   error
		remove func(*mysqlRuntimeSchemaSQLMockMetadata)
	}{
		{
			name: "allocation pack owner foreign key", kind: ErrMySQLRuntimeSchemaForeignKeys,
			remove: func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
				for index, foreignKey := range metadata.foreignKeys {
					if foreignKey.name == "fk_w_credit_reservation_allocation_pack" {
						metadata.foreignKeys = append(metadata.foreignKeys[:index], metadata.foreignKeys[index+1:]...)
						return
					}
				}
			},
		},
		{
			name: "project budget invariant", kind: ErrMySQLRuntimeSchemaChecks,
			remove: func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
				for index, check := range metadata.checks {
					if check.name == "chk_w_global_project_budget_credits" {
						metadata.checks = append(metadata.checks[:index], metadata.checks[index+1:]...)
						return
					}
				}
			},
		},
		{
			name: "membership resolution index", kind: ErrMySQLRuntimeSchemaIndexes,
			remove: func(metadata *mysqlRuntimeSchemaSQLMockMetadata) {
				for index, candidate := range metadata.ordinaryIndexes {
					if candidate.name == "idx_w_order_membership_resolution" {
						metadata.ordinaryIndexes = append(metadata.ordinaryIndexes[:index], metadata.ordinaryIndexes[index+1:]...)
						return
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
				test.remove(&metadata)
				return metadata
			})
			if err := ValidateMySQLRuntimeSchema(context.Background(), database); !errors.Is(err, test.kind) {
				t.Fatalf("missing legacy financial owner hardening error = %v, want %v", err, test.kind)
			}
		})
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsCaseInsensitiveSettlementKey(t *testing.T) {
	want := "settlement_key"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.columns {
			if metadata.columns[index].table == SQLTurnSettlementOutcomeTable && metadata.columns[index].name == want {
				metadata.columns[index].collation = "ascii_general_ci"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), want) {
		t.Fatalf("case-insensitive SettlementKey column error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaColumns, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsCaseInsensitiveReservationTool(t *testing.T) {
	want := "tool"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.columns {
			if metadata.columns[index].table == mysqlCreditReservationTable && metadata.columns[index].name == want {
				metadata.columns[index].collation = "utf8mb4_general_ci"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), want) {
		t.Fatalf("case-insensitive Reservation tool column error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaColumns, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsNullableBindingDigest(t *testing.T) {
	want := "binding_digest"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.columns {
			if metadata.columns[index].table == SQLTurnReservationBindingTable && metadata.columns[index].name == want {
				metadata.columns[index].nullable = true
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), want) {
		t.Fatalf("nullable Turn Reservation binding digest error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaColumns, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedBindingExactIndex(t *testing.T) {
	want := "uk_w_agent_turn_reservation_binding_exact"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.indexes {
			if metadata.indexes[index].name == want {
				metadata.indexes[index].columns[len(metadata.indexes[index].columns)-1] = "project_id"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaIndexes) || !strings.Contains(err.Error(), want) {
		t.Fatalf("tampered Turn Reservation binding index error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaIndexes, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedReservationParentIndex(t *testing.T) {
	want := "uk_w_credit_reservation_agent_binding"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.indexes {
			if metadata.indexes[index].name == want {
				metadata.indexes[index].columns[len(metadata.indexes[index].columns)-1] = "state_version"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaIndexes) || !strings.Contains(err.Error(), want) {
		t.Fatalf("tampered Reservation parent index error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaIndexes, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedReservationIdempotencyIndex(t *testing.T) {
	want := "idx_reservation_uid_key"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.indexes {
			if metadata.indexes[index].name == want {
				metadata.indexes[index].columns[1] = "request_digest"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaIndexes) || !strings.Contains(err.Error(), want) {
		t.Fatalf("tampered Reservation idempotency index error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsWeakenedReservationDigestCheck(t *testing.T) {
	want := "chk_w_credit_reservation_digests"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.checks {
			if metadata.checks[index].name == want {
				metadata.checks[index].clause += " OR 1 = 1"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaChecks) || !strings.Contains(err.Error(), want) {
		t.Fatalf("weakened Reservation digest CHECK error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaChecks, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsBinaryCheckLiteralCaseDrift(t *testing.T) {
	want := "chk_w_credit_reservation_status"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.checks {
			if metadata.checks[index].name == want {
				original := metadata.checks[index].clause
				metadata.checks[index].clause = strings.Replace(original, "'reserved'", "'RESERVED'", 1)
				if metadata.checks[index].clause == original {
					t.Fatalf("status CHECK fixture does not contain the expected binary literal")
				}
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaChecks) || !strings.Contains(err.Error(), want) {
		t.Fatalf("binary literal case drift error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaChecks, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsWeakenedReservationRefundCheck(t *testing.T) {
	want := "chk_w_credit_reservation_refund_tuple"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.checks {
			if metadata.checks[index].name == want {
				metadata.checks[index].clause += " OR 1 = 1"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaChecks) || !strings.Contains(err.Error(), want) {
		t.Fatalf("weakened Reservation refund CHECK error = %v", err)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedOutcomeBindingForeignKey(t *testing.T) {
	want := "fk_w_agent_turn_settlement_outcome_binding"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.foreignKeys {
			if metadata.foreignKeys[index].name == want {
				metadata.foreignKeys[index].referencedColumns[len(metadata.foreignKeys[index].referencedColumns)-1] = "project_id"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaForeignKeys) || !strings.Contains(err.Error(), want) {
		t.Fatalf("tampered Settlement Outcome binding FK error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaForeignKeys, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsWeakenedOutcomeStateCheck(t *testing.T) {
	want := "chk_w_agent_turn_settlement_outcome_state_tuple"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.checks {
			if metadata.checks[index].name == want {
				metadata.checks[index].clause += " OR 1 = 1"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaChecks) || !strings.Contains(err.Error(), want) {
		t.Fatalf("weakened Settlement Outcome state CHECK error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaChecks, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedProviderSourceIndex(t *testing.T) {
	want := "uk_w_agent_provider_usage_journal_source_binding"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.indexes {
			if metadata.indexes[index].name == want {
				metadata.indexes[index].columns[len(metadata.indexes[index].columns)-1] = "attestation_digest"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaIndexes) || !strings.Contains(err.Error(), want) {
		t.Fatalf("tampered Provider source index error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaIndexes, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsMissingCanonicalPayloadColumn(t *testing.T) {
	want := "provider_usage_json"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index, column := range metadata.columns {
			if column.table == SQLProviderUsageJournalTable && column.name == want {
				metadata.columns = append(metadata.columns[:index], metadata.columns[index+1:]...)
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), want) {
		t.Fatalf("missing canonical payload column error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaColumns, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsNativeJSONPayload(t *testing.T) {
	want := "pricing_snapshot_json"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.columns {
			if metadata.columns[index].table == SQLUsageMeterReleaseTable && metadata.columns[index].name == want {
				metadata.columns[index].dataType = "json"
				metadata.columns[index].columnType = "json"
				metadata.columns[index].characterSet = "utf8mb4"
				metadata.columns[index].collation = "utf8mb4_bin"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), want) {
		t.Fatalf("native JSON payload column error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaColumns, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsCaseInsensitiveProviderIdentity(t *testing.T) {
	want := "provider_event_digest"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.columns {
			if metadata.columns[index].table == SQLProviderUsageJournalTable && metadata.columns[index].name == want {
				metadata.columns[index].collation = "ascii_general_ci"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), want) {
		t.Fatalf("case-insensitive Provider identity column error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaColumns, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsNullableEvidenceProvenance(t *testing.T) {
	want := "source_receipt_count"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.columns {
			if metadata.columns[index].table == SQLSettlementReviewUsageEvidenceTable && metadata.columns[index].name == want {
				metadata.columns[index].nullable = true
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaColumns) || !strings.Contains(err.Error(), want) {
		t.Fatalf("nullable Evidence provenance column error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaColumns, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsTamperedEvidenceSourceForeignKey(t *testing.T) {
	want := "fk_w_agent_turn_settlement_usage_evidence_source_journal"
	database := newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		for index := range metadata.foreignKeys {
			if metadata.foreignKeys[index].name == want {
				metadata.foreignKeys[index].deleteRule = "CASCADE"
				break
			}
		}
		return metadata
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaForeignKeys) || !strings.Contains(err.Error(), want) {
		t.Fatalf("tampered Evidence Source FK error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaForeignKeys, want)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsMissingRequiredCheck(t *testing.T) {
	missing := mysqlRuntimeChecks[0]
	database := newMySQLRuntimeSchemaSQLMock(t, func(checks []mysqlRuntimeCheck) []mysqlRuntimeCheck {
		return append([]mysqlRuntimeCheck(nil), checks[1:]...)
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaChecks) || !strings.Contains(err.Error(), missing.name) {
		t.Fatalf("missing MySQL CHECK error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaChecks, missing.name)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsIncompatibleRequiredCheck(t *testing.T) {
	incompatible := mysqlRuntimeChecks[len(mysqlRuntimeChecks)-1]
	database := newMySQLRuntimeSchemaSQLMock(t, func(checks []mysqlRuntimeCheck) []mysqlRuntimeCheck {
		checks[len(checks)-1].clause += " OR 1 = 1"
		return checks
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaChecks) || !strings.Contains(err.Error(), incompatible.name) {
		t.Fatalf("incompatible MySQL CHECK error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaChecks, incompatible.name)
	}
}

func TestValidateMySQLRuntimeSchemaSQLMockRejectsUnenforcedRequiredCheck(t *testing.T) {
	unenforced := mysqlRuntimeChecks[1]
	database := newMySQLRuntimeSchemaSQLMock(t, func(checks []mysqlRuntimeCheck) []mysqlRuntimeCheck {
		checks[1].enforced = false
		return checks
	})
	err := ValidateMySQLRuntimeSchema(context.Background(), database)
	if !errors.Is(err, ErrMySQLRuntimeSchemaChecks) || !strings.Contains(err.Error(), unenforced.name) {
		t.Fatalf("unenforced MySQL CHECK error = %v, want sanitized %s for %s", err, ErrMySQLRuntimeSchemaChecks, unenforced.name)
	}
}

var mysqlRuntimeSchemaSQLMockSequence atomic.Uint64

type mysqlRuntimeSchemaSQLMockDriver struct {
	metadata mysqlRuntimeSchemaSQLMockMetadata
}

func (driver mysqlRuntimeSchemaSQLMockDriver) Open(string) (sqldriver.Conn, error) {
	return &mysqlRuntimeSchemaSQLMockConn{metadata: cloneMySQLRuntimeSchemaSQLMockMetadata(driver.metadata)}, nil
}

type mysqlRuntimeSchemaSQLMockConn struct {
	metadata mysqlRuntimeSchemaSQLMockMetadata
}

type mysqlRuntimeSchemaSQLMockMetadata struct {
	tables                []string
	primaryKeys           []mysqlRuntimeIndex
	indexes               []mysqlRuntimeIndex
	ordinaryIndexes       []mysqlRuntimeIndex
	foreignKeys           []mysqlRuntimeForeignKey
	checks                []mysqlRuntimeCheck
	columns               []mysqlRuntimeColumn
	requiredColumns       []mysqlRuntimeRequiredColumn
	columnProperties      []mysqlRuntimeColumnProperty
	foreignKeyChecks      int64
	uniqueChecks          int64
	checkConstraintChecks int64
	utcOffsetSeconds      int64
	sessionTimeZone       string
	transactionIsolation  string
	version               string
	versionComment        string
}

func (*mysqlRuntimeSchemaSQLMockConn) Prepare(string) (sqldriver.Stmt, error) {
	return nil, errors.New("MySQL runtime schema SQL mock does not prepare statements")
}

func (*mysqlRuntimeSchemaSQLMockConn) Close() error { return nil }

func (*mysqlRuntimeSchemaSQLMockConn) Begin() (sqldriver.Tx, error) {
	return nil, errors.New("MySQL runtime schema SQL mock does not begin transactions")
}

func (connection *mysqlRuntimeSchemaSQLMockConn) QueryContext(
	_ context.Context,
	query string,
	_ []sqldriver.NamedValue,
) (sqldriver.Rows, error) {
	normalized := strings.ToLower(query)
	switch {
	case strings.Contains(normalized, "@@session.foreign_key_checks"):
		return newMySQLRuntimeSchemaSQLMockRows(
			[]string{
				"foreign_key_checks", "unique_checks", "check_constraint_checks", "time_zone",
				"transaction_isolation", "utc_offset_seconds", "version", "version_comment",
			},
			[][]sqldriver.Value{{
				connection.metadata.foreignKeyChecks, connection.metadata.uniqueChecks,
				connection.metadata.checkConstraintChecks, connection.metadata.sessionTimeZone,
				connection.metadata.transactionIsolation, connection.metadata.utcOffsetSeconds,
				connection.metadata.version, connection.metadata.versionComment,
			}},
		), nil
	case strings.Contains(normalized, "information_schema.tables"):
		values := make([][]sqldriver.Value, 0, len(connection.metadata.tables))
		for _, table := range connection.metadata.tables {
			values = append(values, []sqldriver.Value{table, "InnoDB"})
		}
		return newMySQLRuntimeSchemaSQLMockRows([]string{"TABLE_NAME", "ENGINE"}, values), nil
	case strings.Contains(normalized, "information_schema.columns") && strings.Contains(normalized, "column_key"):
		values := make([][]sqldriver.Value, 0, len(connection.metadata.columnProperties))
		for _, property := range connection.metadata.columnProperties {
			columnKey := ""
			if property.primaryKey {
				columnKey = "PRI"
			}
			extra := ""
			if property.autoIncrement {
				extra = "auto_increment"
			}
			var defaultValue sqldriver.Value
			if property.defaultPinned && !property.defaultIsNull {
				defaultValue = property.defaultValue
			}
			values = append(values, []sqldriver.Value{
				property.table, property.name, columnKey, extra, defaultValue,
			})
		}
		return newMySQLRuntimeSchemaSQLMockRows([]string{
			"TABLE_NAME", "COLUMN_NAME", "COLUMN_KEY", "EXTRA", "COLUMN_DEFAULT",
		}, values), nil
	case strings.Contains(normalized, "information_schema.columns"):
		values := make([][]sqldriver.Value, 0, len(connection.metadata.columns)+len(connection.metadata.requiredColumns))
		for _, column := range connection.metadata.columns {
			nullable := "NO"
			if column.nullable {
				nullable = "YES"
			}
			var characterSet, collation, precision sqldriver.Value
			if column.characterSet != "" {
				characterSet = column.characterSet
			}
			if column.collation != "" {
				collation = column.collation
			}
			if column.datetimePrecision >= 0 {
				precision = column.datetimePrecision
			}
			values = append(values, []sqldriver.Value{
				column.table, column.name, column.dataType, column.columnType, nullable,
				characterSet, collation, precision,
			})
		}
		for _, column := range connection.metadata.requiredColumns {
			values = append(values, []sqldriver.Value{
				column.table, column.name, "varchar", "varchar(1)", "YES", "utf8mb4", "utf8mb4_bin", nil,
			})
		}
		return newMySQLRuntimeSchemaSQLMockRows([]string{
			"TABLE_NAME", "COLUMN_NAME", "DATA_TYPE", "COLUMN_TYPE", "IS_NULLABLE",
			"CHARACTER_SET_NAME", "COLLATION_NAME", "DATETIME_PRECISION",
		}, values), nil
	case strings.Contains(normalized, "information_schema.statistics"):
		indexes := connection.metadata.indexes
		if strings.Contains(normalized, "non_unique = 1") {
			indexes = connection.metadata.ordinaryIndexes
		} else {
			indexes = append(append([]mysqlRuntimeIndex(nil), connection.metadata.primaryKeys...), indexes...)
		}
		values := make([][]sqldriver.Value, 0, len(indexes))
		for _, index := range indexes {
			for sequence, column := range index.columns {
				visible := "YES"
				if index.invisible {
					visible = "NO"
				}
				values = append(values, []sqldriver.Value{
					index.table, index.name, int64(sequence + 1), column, nil, visible,
				})
			}
		}
		return newMySQLRuntimeSchemaSQLMockRows(
			[]string{"TABLE_NAME", "INDEX_NAME", "SEQ_IN_INDEX", "COLUMN_NAME", "SUB_PART", "IS_VISIBLE"}, values), nil
	case strings.Contains(normalized, "information_schema.key_column_usage"):
		values := make([][]sqldriver.Value, 0, len(connection.metadata.foreignKeys))
		for _, foreignKey := range connection.metadata.foreignKeys {
			for ordinal, column := range foreignKey.columns {
				values = append(values, []sqldriver.Value{
					foreignKey.table, foreignKey.name, column,
					foreignKey.referencedTable, foreignKey.referencedColumns[ordinal],
					int64(ordinal + 1), foreignKey.updateRule, foreignKey.deleteRule,
				})
			}
		}
		return newMySQLRuntimeSchemaSQLMockRows([]string{
			"TABLE_NAME", "CONSTRAINT_NAME", "COLUMN_NAME", "REFERENCED_TABLE_NAME",
			"REFERENCED_COLUMN_NAME", "ORDINAL_POSITION", "UPDATE_RULE", "DELETE_RULE",
		}, values), nil
	case strings.Contains(normalized, "information_schema.check_constraints"):
		values := make([][]sqldriver.Value, 0, len(connection.metadata.checks))
		for _, check := range connection.metadata.checks {
			enforced := "NO"
			if check.enforced {
				enforced = "YES"
			}
			values = append(values, []sqldriver.Value{check.table, check.name, "(" + check.clause + ")", enforced})
		}
		return newMySQLRuntimeSchemaSQLMockRows(
			[]string{"TABLE_NAME", "CONSTRAINT_NAME", "CHECK_CLAUSE", "ENFORCED"}, values), nil
	default:
		return nil, errors.New("unexpected MySQL runtime schema SQL mock query")
	}
}

type mysqlRuntimeSchemaSQLMockRows struct {
	columns []string
	values  [][]sqldriver.Value
	next    int
}

func newMySQLRuntimeSchemaSQLMockRows(
	columns []string,
	values [][]sqldriver.Value,
) *mysqlRuntimeSchemaSQLMockRows {
	return &mysqlRuntimeSchemaSQLMockRows{
		columns: append([]string(nil), columns...), values: values,
	}
}

func (rows *mysqlRuntimeSchemaSQLMockRows) Columns() []string {
	return append([]string(nil), rows.columns...)
}

func (*mysqlRuntimeSchemaSQLMockRows) Close() error { return nil }

func (rows *mysqlRuntimeSchemaSQLMockRows) Next(destination []sqldriver.Value) error {
	if rows.next >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.next])
	rows.next++
	return nil
}

func newMySQLRuntimeSchemaSQLMock(
	t *testing.T,
	mutate func([]mysqlRuntimeCheck) []mysqlRuntimeCheck,
) *sql.DB {
	t.Helper()
	return newMySQLRuntimeSchemaMetadataSQLMock(t, func(metadata mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata {
		if mutate != nil {
			metadata.checks = mutate(metadata.checks)
		}
		return metadata
	})
}

func newMySQLRuntimeSchemaMetadataSQLMock(
	t *testing.T,
	mutate func(mysqlRuntimeSchemaSQLMockMetadata) mysqlRuntimeSchemaSQLMockMetadata,
) *sql.DB {
	t.Helper()
	metadata := cloneMySQLRuntimeSchemaSQLMockMetadata(mysqlRuntimeSchemaSQLMockMetadata{
		tables: mysqlRuntimeTables, primaryKeys: mysqlRuntimePrimaryKeys,
		indexes: mysqlRuntimeUniqueIndexes, ordinaryIndexes: mysqlRuntimeOrdinaryIndexes,
		foreignKeys: mysqlRuntimeForeignKeys, checks: mysqlRuntimeChecks,
		columns: mysqlRuntimeColumns, requiredColumns: mysqlRuntimeRequiredColumns,
		columnProperties: mysqlRuntimeColumnProperties,
		foreignKeyChecks: 1, uniqueChecks: 1, checkConstraintChecks: 1,
		sessionTimeZone: "+00:00", transactionIsolation: "READ-COMMITTED",
		version: "8.0.36", versionComment: "MySQL Community Server - GPL",
	})
	for index := range metadata.checks {
		metadata.checks[index].enforced = true
	}
	if mutate != nil {
		metadata = mutate(metadata)
	}
	driverName := fmt.Sprintf("agentturn_mysql_schema_sqlmock_%d", mysqlRuntimeSchemaSQLMockSequence.Add(1))
	sql.Register(driverName, mysqlRuntimeSchemaSQLMockDriver{metadata: metadata})
	database, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open MySQL runtime schema SQL mock: %v", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close MySQL runtime schema SQL mock: %v", err)
		}
	})
	return database
}

func cloneMySQLRuntimeSchemaSQLMockMetadata(
	metadata mysqlRuntimeSchemaSQLMockMetadata,
) mysqlRuntimeSchemaSQLMockMetadata {
	clone := mysqlRuntimeSchemaSQLMockMetadata{
		tables: append([]string(nil), metadata.tables...), checks: append([]mysqlRuntimeCheck(nil), metadata.checks...),
		columns:          append([]mysqlRuntimeColumn(nil), metadata.columns...),
		requiredColumns:  append([]mysqlRuntimeRequiredColumn(nil), metadata.requiredColumns...),
		columnProperties: append([]mysqlRuntimeColumnProperty(nil), metadata.columnProperties...),
		foreignKeyChecks: metadata.foreignKeyChecks, uniqueChecks: metadata.uniqueChecks,
		checkConstraintChecks: metadata.checkConstraintChecks, utcOffsetSeconds: metadata.utcOffsetSeconds,
		sessionTimeZone: metadata.sessionTimeZone, transactionIsolation: metadata.transactionIsolation,
		version: metadata.version, versionComment: metadata.versionComment,
	}
	clone.primaryKeys = make([]mysqlRuntimeIndex, len(metadata.primaryKeys))
	for index, candidate := range metadata.primaryKeys {
		clone.primaryKeys[index] = candidate
		clone.primaryKeys[index].columns = append([]string(nil), candidate.columns...)
	}
	clone.indexes = make([]mysqlRuntimeIndex, len(metadata.indexes))
	for index, candidate := range metadata.indexes {
		clone.indexes[index] = candidate
		clone.indexes[index].columns = append([]string(nil), candidate.columns...)
	}
	clone.ordinaryIndexes = make([]mysqlRuntimeIndex, len(metadata.ordinaryIndexes))
	for index, candidate := range metadata.ordinaryIndexes {
		clone.ordinaryIndexes[index] = candidate
		clone.ordinaryIndexes[index].columns = append([]string(nil), candidate.columns...)
	}
	clone.foreignKeys = make([]mysqlRuntimeForeignKey, len(metadata.foreignKeys))
	for index, candidate := range metadata.foreignKeys {
		clone.foreignKeys[index] = candidate
		clone.foreignKeys[index].columns = append([]string(nil), candidate.columns...)
		clone.foreignKeys[index].referencedColumns = append([]string(nil), candidate.referencedColumns...)
	}
	return clone
}

func TestLoadMySQLContractSettingsDefaultsDisabled(t *testing.T) {
	clearMySQLContractEnvironment(t)
	_, enabled, err := loadMySQLContractSettings()
	if err != nil || enabled {
		t.Fatalf("default MySQL contract settings = enabled:%v err:%v, want disabled", enabled, err)
	}
}

func TestLoadMySQLContractSettingsAcceptsExplicitLoopbackDSN(t *testing.T) {
	clearMySQLContractEnvironment(t)
	enableDirectMySQLContractForTest(t)
	t.Setenv(mysqlContractDSNEnv, "contract_user:contract_password@tcp(127.0.0.1:3306)/contract_db?parseTime=true")
	settings, enabled, err := loadMySQLContractSettings()
	if err != nil || !enabled || settings.dsn == "" {
		t.Fatalf("explicit loopback settings = enabled:%v err:%v", enabled, err)
	}
	parsed, err := drivermysql.ParseDSN(settings.dsn)
	if err != nil {
		t.Fatal("parse normalized loopback contract settings failed")
	}
	if parsed.Timeout != mysqlContractIOTimeout || parsed.ReadTimeout != mysqlContractIOTimeout ||
		parsed.WriteTimeout != mysqlContractIOTimeout {
		t.Fatalf("normalized contract timeouts = dial:%s read:%s write:%s", parsed.Timeout, parsed.ReadTimeout, parsed.WriteTimeout)
	}
}

func TestLoadMySQLContractSettingsRejectsRemotePlaintextByDefault(t *testing.T) {
	clearMySQLContractEnvironment(t)
	enableDirectMySQLContractForTest(t)
	t.Setenv(mysqlContractDSNEnv, "contract_user:secret_marker@tcp(db.example.invalid:3306)/contract_db?parseTime=true")
	_, _, err := loadMySQLContractSettings()
	if err == nil || strings.Contains(err.Error(), "secret_marker") {
		t.Fatalf("remote plaintext error = %v, want sanitized rejection", err)
	}
	t.Setenv(mysqlContractAllowPlaintextEnv, "1")
	if _, enabled, err := loadMySQLContractSettings(); err != nil || !enabled {
		t.Fatalf("explicit plaintext opt-in = enabled:%v err:%v", enabled, err)
	}
}

func TestLoadMySQLContractSettingsAcceptsRemoteTLSWithoutConnecting(t *testing.T) {
	clearMySQLContractEnvironment(t)
	enableDirectMySQLContractForTest(t)
	t.Setenv(mysqlContractDSNEnv, "contract_user:secret_marker@tcp(db.example.invalid:3306)/contract_db?parseTime=true&tls=true")
	settings, enabled, err := loadMySQLContractSettings()
	if err != nil || !enabled || settings.dsn == "" {
		t.Fatalf("remote TLS settings = enabled:%v err:%v", enabled, err)
	}
}

func TestLoadMySQLContractSettingsRejectsRemoteTLSFallback(t *testing.T) {
	clearMySQLContractEnvironment(t)
	enableDirectMySQLContractForTest(t)
	t.Setenv(mysqlContractDSNEnv, "contract_user:secret_marker@tcp(db.example.invalid:3306)/contract_db?parseTime=true&tls=preferred")
	_, _, err := loadMySQLContractSettings()
	if err == nil || strings.Contains(err.Error(), "secret_marker") {
		t.Fatalf("remote TLS fallback error = %v, want sanitized rejection", err)
	}
}

func TestLoadMySQLContractSettingsRejectsDirectDSNWithoutIsolatedCIOptIn(t *testing.T) {
	clearMySQLContractEnvironment(t)
	t.Setenv(mysqlContractEnabledEnv, "1")
	t.Setenv(mysqlContractDSNEnv, "contract_user:contract_password@tcp(127.0.0.1:3306)/contract_db?parseTime=true")
	if _, _, err := loadMySQLContractSettings(); err == nil || !strings.Contains(err.Error(), "isolated-CI") {
		t.Fatalf("direct DSN error = %v, want isolated-CI rejection", err)
	}
}

func TestLoadMySQLContractSettingsRejectsDirectDSNForUnmarkedDatabase(t *testing.T) {
	for _, databaseName := range []string{"production", "contest_prod"} {
		t.Run(databaseName, func(t *testing.T) {
			clearMySQLContractEnvironment(t)
			enableDirectMySQLContractForTest(t)
			t.Setenv(mysqlContractDSNEnv, "contract_user:contract_password@tcp(127.0.0.1:3306)/"+databaseName+"?parseTime=true")
			if _, _, err := loadMySQLContractSettings(); err == nil || !strings.Contains(err.Error(), "isolated test, contract or CI") {
				t.Fatalf("unmarked database error = %v, want isolated-database rejection", err)
			}
		})
	}
}

func TestLoadMySQLContractSettingsRejectsDisabledForeignKeyChecks(t *testing.T) {
	clearMySQLContractEnvironment(t)
	enableDirectMySQLContractForTest(t)
	t.Setenv(mysqlContractDSNEnv, "contract_user:contract_password@tcp(127.0.0.1:3306)/contract_db?parseTime=true&foreign_key_checks=0")
	if _, _, err := loadMySQLContractSettings(); err == nil || !strings.Contains(err.Error(), "foreign-key checks") {
		t.Fatalf("disabled foreign-key checks error = %v, want rejection", err)
	}
}

func TestLoadMySQLContractSettingsRejectsRemoteInsecureTLSByDefault(t *testing.T) {
	clearMySQLContractEnvironment(t)
	enableDirectMySQLContractForTest(t)
	t.Setenv(mysqlContractDSNEnv, "contract_user:secret_marker@tcp(db.example.invalid:3306)/contract_db?parseTime=true&tls=skip-verify")
	_, _, err := loadMySQLContractSettings()
	if err == nil || strings.Contains(err.Error(), "secret_marker") || !strings.Contains(err.Error(), "insecure TLS") {
		t.Fatalf("insecure TLS error = %v, want sanitized rejection", err)
	}
	t.Setenv(mysqlContractAllowInsecureTLSEnv, "1")
	if _, enabled, err := loadMySQLContractSettings(); err != nil || !enabled {
		t.Fatalf("explicit insecure-TLS opt-in = enabled:%v err:%v", enabled, err)
	}
}

func TestLoadMySQLContractSettingsUsesPrivateConfigWithoutGlobals(t *testing.T) {
	clearMySQLContractEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`mysql_system:
  path: 127.0.0.1
  port: "3306"
  db-name: contract_db
  username: contract_user
  password: contract_password
  Config: parseTime=true
unrelated:
  unsafe: ignored
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mysqlContractEnabledEnv, "1")
	t.Setenv(mysqlContractConfigEnv, path)
	settings, enabled, err := loadMySQLContractSettings()
	if err != nil || !enabled || settings.dsn == "" {
		t.Fatalf("private config settings = enabled:%v err:%v", enabled, err)
	}
}

func TestLoadMySQLContractSettingsRejectsAccessibleConfig(t *testing.T) {
	clearMySQLContractEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mysql_system: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mysqlContractEnabledEnv, "1")
	t.Setenv(mysqlContractConfigEnv, path)
	if _, _, err := loadMySQLContractSettings(); err == nil || !strings.Contains(err.Error(), "group/world") {
		t.Fatalf("accessible config error = %v", err)
	}
}

func TestLoadMySQLContractSettingsRejectsEnabledDurableRollout(t *testing.T) {
	clearMySQLContractEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`mysql_system:
  path: 127.0.0.1
  port: "3306"
  db-name: contract_db
  username: contract_user
  password: contract_password
  Config: parseTime=true
agent_platform_rollout:
  credential:
    desktop_resource: enforce
    agent_resource: enforce
  durable_turn:
    legacy_shadow: validate
    public_api: on
    canary_percent: 100
    worker: on
    allow_new_starts: true
  desktop:
    agent_transport: durable
  readiness:
    token_rollover_complete: true
    active_device_sessions: true
    sql_store: true
    atomic_live_event_stream: true
    worker_lease_fencing: true
    transactional_outbox: true
    exactly_once_settlement: true
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mysqlContractEnabledEnv, "1")
	t.Setenv(mysqlContractConfigEnv, path)
	if _, _, err := loadMySQLContractSettings(); err == nil || !strings.Contains(err.Error(), "disable durable traffic") {
		t.Fatalf("enabled durable rollout error = %v", err)
	}
}

func clearMySQLContractEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		mysqlContractEnabledEnv,
		mysqlContractConfigEnv,
		mysqlContractDSNEnv,
		mysqlContractAllowDirectDSNEnv,
		mysqlContractAllowPlaintextEnv,
		mysqlContractAllowInsecureTLSEnv,
	} {
		t.Setenv(name, "")
	}
}

func enableDirectMySQLContractForTest(t *testing.T) {
	t.Helper()
	t.Setenv(mysqlContractEnabledEnv, "1")
	t.Setenv(mysqlContractAllowDirectDSNEnv, "1")
}

func mysqlContractAssertCreated(t *testing.T, admission AdmissionRecord, err error) {
	t.Helper()
	if err != nil {
		t.Fatal("MySQL contract Admit failed")
	}
	if !admission.Created {
		t.Fatal("MySQL contract Admit did not create its random namespace")
	}
}

func mysqlContractAssertNoRows(t *testing.T, database *gorm.DB, turnID agentv1.TurnID) {
	t.Helper()
	for _, table := range mysqlContractTables {
		if count := mysqlContractCount(t, database, table, "turn_id = ?", turnID); count != 0 {
			t.Fatalf("MySQL contract cleanup left rows in %s", table)
		}
	}
}
