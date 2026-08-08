package agentturn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

func TestPluginScopedExecutionStoreClaimsOnlyExactReleaseSnapshots(t *testing.T) {
	db, base, _, _ := newSQLClaimNextFixture(t)
	supported := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "2.0.0", ReleaseDigest: "sha256:WriterRelease",
	}
	candidates := []struct {
		suffix string
		plugin agentv1.EventPluginRef
	}{
		{suffix: "scope_wrong_id", plugin: agentv1.EventPluginRef{ID: "workmax.workbook", Version: supported.Version, ReleaseDigest: supported.ReleaseDigest}},
		{suffix: "scope_wrong_version", plugin: agentv1.EventPluginRef{ID: supported.ID, Version: "1.9.9", ReleaseDigest: supported.ReleaseDigest}},
		{suffix: "scope_wrong_digest", plugin: agentv1.EventPluginRef{ID: supported.ID, Version: supported.Version, ReleaseDigest: "sha256:other"}},
		{suffix: "scope_digest_case", plugin: agentv1.EventPluginRef{ID: supported.ID, Version: supported.Version, ReleaseDigest: "sha256:writerrelease"}},
		{suffix: "scope_supported", plugin: supported},
	}
	for _, candidate := range candidates {
		turn := sqlStoreTestTurn(candidate.suffix)
		turn.Plugin = candidate.plugin
		if _, err := base.Admit(context.Background(), turn,
			sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
			t.Fatalf("Admit(%s): %v", candidate.suffix, err)
		}
	}

	scoped, err := NewPluginScopedExecutionStore(base, []agentv1.EventPluginRef{supported})
	if err != nil {
		t.Fatal(err)
	}
	result, err := scoped.ClaimNext(context.Background(), claimNextCommand("attempt_exact_scope"))
	if err != nil {
		t.Fatalf("ClaimNext(): %v", err)
	}
	if result.Turn.ID != "turn_scope_supported" || result.Turn.Plugin != supported {
		t.Fatalf("ClaimNext() = %+v, want exact supported release", result.Turn)
	}
	for _, candidate := range candidates[:len(candidates)-1] {
		if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", "turn_"+candidate.suffix); got != 0 {
			t.Fatalf("out-of-scope Turn %q persisted %d attempts", candidate.suffix, got)
		}
	}
}

func TestPluginScopedExecutionStorePreservesFIFOAcrossSeveralReleases(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "multi_first", "multi_second", "multi_third")
	second := agentv1.EventPluginRef{ID: "workmax.workbook", Version: "3.0.0", ReleaseDigest: "sha256:workbook"}
	if err := base.db.Table(SQLTurnTable).Where("turn_id = ?", turns[1].ID).
		UpdateColumn("plugin_snapshot_json", mustPluginSnapshotJSON(t, second)).Error; err != nil {
		t.Fatal(err)
	}
	scoped, err := NewPluginScopedExecutionStore(base, []agentv1.EventPluginRef{second, turns[0].Plugin})
	if err != nil {
		t.Fatal(err)
	}

	for index, want := range turns {
		result, claimErr := scoped.ClaimNext(context.Background(), claimNextCommand(fmt.Sprintf("attempt_multi_%d", index)))
		if claimErr != nil {
			t.Fatalf("ClaimNext(%d): %v", index, claimErr)
		}
		if result.Turn.ID != want.ID {
			t.Fatalf("ClaimNext(%d) = %q, want FIFO %q", index, result.Turn.ID, want.ID)
		}
	}
}

func TestClaimAttemptRejectsOutOfScopeBeforeWriteAndOnReplay(t *testing.T) {
	db, store, turn, _ := newSQLExecutionFixture(t, "scope_authority")
	foreign := []agentv1.EventPluginRef{{
		ID: turn.Plugin.ID, Version: turn.Plugin.Version, ReleaseDigest: "sha256:foreign",
	}}
	command := executionClaimCommand(turn.ID, "attempt_scope_authority")
	command.PluginScope = foreign
	if _, err := store.ClaimAttempt(context.Background(), command); !errors.Is(err, ErrPluginScopeMismatch) {
		t.Fatalf("out-of-scope ClaimAttempt() error = %v, want ErrPluginScopeMismatch", err)
	}
	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("out-of-scope claim persisted %d attempts", got)
	}

	claim := executionClaimCommand(turn.ID, "attempt_scope_replay")
	claim.PluginScope = []agentv1.EventPluginRef{turn.Plugin}
	if _, err := store.ClaimAttempt(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	replay := ClaimNextCommand{
		AttemptID: claim.AttemptID, WorkerID: claim.WorkerID,
		WorkerBuildDigest: claim.WorkerBuildDigest, PluginScope: foreign,
	}
	if _, err := store.ClaimNext(context.Background(), replay); !errors.Is(err, ErrPluginScopeMismatch) {
		t.Fatalf("out-of-scope ClaimNext replay error = %v, want ErrPluginScopeMismatch", err)
	}
	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("out-of-scope replay changed attempt count to %d", got)
	}
}

func TestPluginScopedExecutionStoreOwnsCanonicalScope(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "scope_copy")
	writer := turns[0].Plugin
	workbook := agentv1.EventPluginRef{ID: "workmax.workbook", Version: "1.0.0", ReleaseDigest: "sha256:workbook"}
	input := []agentv1.EventPluginRef{writer, workbook}
	scoped, err := NewPluginScopedExecutionStore(base, input)
	if err != nil {
		t.Fatal(err)
	}
	input[0].ID = "mutated"
	input = input[:0]
	if !scoped.Matches(base, []agentv1.EventPluginRef{workbook, writer}) {
		t.Fatal("caller mutation changed the sealed scope or order affected matching")
	}
	if scoped.Matches(&SQLStore{}, []agentv1.EventPluginRef{writer, workbook}) ||
		scoped.Matches(base, []agentv1.EventPluginRef{writer}) {
		t.Fatal("scoped store matched the wrong base or an incomplete scope")
	}
}

func TestPluginScopeValidationRejectsEmptyConcreteDuplicateAndOversizedScopes(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "scope_validation")
	plugin := turns[0].Plugin
	if _, err := NewPluginScopedExecutionStore(base, nil); !errors.Is(err, ErrPluginScopeMismatch) {
		t.Fatalf("empty scope error = %v, want ErrPluginScopeMismatch", err)
	}
	if _, err := NewPluginScopedExecutionStore(nil, []agentv1.EventPluginRef{plugin}); !errors.Is(err, ErrPluginScopeMismatch) {
		t.Fatalf("nil base error = %v, want ErrPluginScopeMismatch", err)
	}
	if _, err := NewPluginScopedExecutionStore(base, []agentv1.EventPluginRef{plugin, plugin}); err == nil {
		t.Fatal("duplicate scope was accepted")
	}
	tooMany := make([]agentv1.EventPluginRef, MaxClaimPluginScopes+1)
	for index := range tooMany {
		tooMany[index] = agentv1.EventPluginRef{
			ID: fmt.Sprintf("workmax.plugin.%02d", index), Version: "1.0.0", ReleaseDigest: "sha256:release",
		}
	}
	if _, err := NewPluginScopedExecutionStore(base, tooMany); err == nil {
		t.Fatal("oversized scope was accepted")
	}
}

func TestClaimPluginScopePredicateUsesExactDialectComparisons(t *testing.T) {
	plugin := agentv1.EventPluginRef{ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: "sha256:AbCd"}
	for _, dialect := range []string{"sqlite", "mysql"} {
		predicate, args, err := claimPluginScopePredicate(dialect, []agentv1.EventPluginRef{plugin})
		if err != nil {
			t.Fatalf("%s predicate: %v", dialect, err)
		}
		if len(args) != 3 || args[0] != plugin.ID || args[1] != plugin.Version || args[2] != plugin.ReleaseDigest {
			t.Fatalf("%s args = %#v", dialect, args)
		}
		if dialect == "mysql" && strings.Count(predicate, "AS BINARY") != 6 {
			t.Fatalf("MySQL predicate is not binary exact: %s", predicate)
		}
		if dialect == "sqlite" && strings.Count(predicate, "COLLATE BINARY") != 3 {
			t.Fatalf("SQLite predicate is not binary exact: %s", predicate)
		}
	}
	if _, _, err := claimPluginScopePredicate("postgres", []agentv1.EventPluginRef{plugin}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("unknown dialect error = %v, want ErrStoreUnavailable", err)
	}
}

// TestPluginScopedExecutionStoreMySQLContract is opt-in through the shared,
// fail-closed MySQL contract harness. It executes the real JSON_EXTRACT /
// binary-cast predicate against an isolated schema, including several release
// OR branches and a digest that differs only by case. The ordinary test suite
// skips this test before opening a connection.
func TestPluginScopedExecutionStoreMySQLContract(t *testing.T) {
	settings := mysqlContractSettingsForTest(t)
	database := openMySQLContractDatabase(t, settings)
	mysqlContractPreflight(t, database)
	store := mustSQLStore(t, database)
	namespace := mysqlContractSuffix(t, "mxscope")

	writer := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "4.0.0", ReleaseDigest: "sha256:ScopeReleaseCase",
	}
	workbook := agentv1.EventPluginRef{
		ID: "workmax.workbook", Version: "5.0.0", ReleaseDigest: "sha256:WorkbookRelease",
	}
	turns := []Turn{
		sqlStoreTestTurn(namespace + "_foreign"),
		sqlStoreTestTurn(namespace + "_writer"),
		sqlStoreTestTurn(namespace + "_workbook"),
	}
	turns[0].Plugin = agentv1.EventPluginRef{
		ID: writer.ID, Version: writer.Version, ReleaseDigest: "sha256:scopereleasecase",
	}
	turns[1].Plugin = writer
	turns[2].Plugin = workbook
	for index := range turns {
		turns[index].CreatedAt = turns[index].CreatedAt.Add(time.Duration(index) * time.Microsecond)
		turns[index].UpdatedAt = turns[index].CreatedAt
		mysqlContractAssertNamespaceEmpty(t, database, turns[index])
		admission, err := store.Admit(context.Background(), turns[index],
			sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`))
		mysqlContractAssertCreated(t, admission, err)
		cleanup := mysqlContractOwnedCleanup(t, database, turns[index])
		t.Cleanup(cleanup)
	}

	scoped, err := NewPluginScopedExecutionStore(store, []agentv1.EventPluginRef{workbook, writer})
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range turns[1:] {
		claimed, claimErr := scoped.ClaimNext(context.Background(), ClaimNextCommand{
			AttemptID:         fmt.Sprintf("attempt_mysql_scope_%d_%s", index, namespace),
			WorkerID:          "worker_mysql_scope",
			WorkerBuildDigest: "sha256:mysql-scope-build",
		})
		if claimErr != nil {
			t.Fatalf("MySQL scoped ClaimNext(%d): %v", index, claimErr)
		}
		if claimed.Turn.ID != want.ID || claimed.Turn.Plugin != want.Plugin {
			t.Fatalf("MySQL scoped ClaimNext(%d) = %+v, want %+v", index, claimed.Turn, want)
		}
	}
	if got := mysqlContractCount(t, database, SQLTurnAttemptTable, "turn_id = ?", turns[0].ID); got != 0 {
		t.Fatalf("case-mismatched MySQL release persisted %d attempts", got)
	}
}

func mustPluginSnapshotJSON(t *testing.T, plugin agentv1.EventPluginRef) string {
	t.Helper()
	return fmt.Sprintf(`{"id":%q,"version":%q,"releaseDigest":%q}`,
		plugin.ID, plugin.Version, plugin.ReleaseDigest)
}
