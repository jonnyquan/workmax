package workagent

import (
	"encoding/json"
	"server/globals"
	workagentModel "server/model/workagent"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Tests for AgentAccountPool itself — lifecycle, normalization,
// shutdown semantics. CircuitBreaker tests live in
// circuit_breaker_test.go.

func TestNormalizeRequestedTier(t *testing.T) {
	cases := map[string]string{
		"":             "work-pro",
		"work-pro":     "work-pro",
		"WORK-PRO":     "work-pro",
		"  work-pro  ": "work-pro",
		"work-plus":    "work-plus",
		"WORK-PLUS":    "work-plus",
		"unknown":      "work-pro", // fallback
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := normalizeRequestedTier(input); got != want {
				t.Errorf("normalizeRequestedTier(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestAccountTokenBudgetCanFit(t *testing.T) {
	month := "2026-05"
	cases := []struct {
		name   string
		acc    workagentModel.AgentAccount
		need   int
		expect bool
	}{
		{
			name:   "unlimited_cap",
			acc:    workagentModel.AgentAccount{MonthlyTokenBudgetCredits: 0, MonthlyTokenCreditsUsed: 999, MonthlyTokenBudgetMonth: month},
			need:   50,
			expect: true,
		},
		{
			name:   "fits_current_month",
			acc:    workagentModel.AgentAccount{MonthlyTokenBudgetCredits: 100, MonthlyTokenCreditsUsed: 70, MonthlyTokenBudgetMonth: month},
			need:   30,
			expect: true,
		},
		{
			name:   "exceeds_current_month",
			acc:    workagentModel.AgentAccount{MonthlyTokenBudgetCredits: 100, MonthlyTokenCreditsUsed: 80, MonthlyTokenBudgetMonth: month},
			need:   30,
			expect: false,
		},
		{
			name:   "stale_month_resets_for_routing",
			acc:    workagentModel.AgentAccount{MonthlyTokenBudgetCredits: 100, MonthlyTokenCreditsUsed: 100, MonthlyTokenBudgetMonth: "2026-04"},
			need:   30,
			expect: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := accountTokenBudgetCanFit(&tc.acc, tc.need, month); got != tc.expect {
				t.Fatalf("accountTokenBudgetCanFit = %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestGetAccountForModelTier_SkipsExhaustedTokenBudget(t *testing.T) {
	db := installAccountPoolTestDB(t)
	nowMonth := accountTokenBudgetMonth(time.Now())
	accounts := []workagentModel.AgentAccount{
		{
			ID:                        1,
			Name:                      "active-exhausted",
			Provider:                  "anthropic",
			BaseURL:                   "https://api.anthropic.com/v1",
			APIKey:                    "sk-active",
			Priority:                  10,
			Status:                    1,
			IsActive:                  true,
			MonthlyTokenBudgetCredits: 100,
			MonthlyTokenCreditsUsed:   95,
			MonthlyTokenBudgetMonth:   nowMonth,
		},
		{
			ID:                        2,
			Name:                      "fallback-fits",
			Provider:                  "anthropic",
			BaseURL:                   "https://api.anthropic.com/v1",
			APIKey:                    "sk-fallback",
			Priority:                  9,
			Status:                    1,
			MonthlyTokenBudgetCredits: 100,
			MonthlyTokenCreditsUsed:   60,
			MonthlyTokenBudgetMonth:   nowMonth,
		},
	}
	if err := db.Create(&accounts).Error; err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	p := &AgentAccountPool{
		circuitBreakers: map[uint64]*CircuitBreaker{
			1: NewCircuitBreaker(1),
			2: NewCircuitBreaker(2),
		},
	}

	got, err := p.GetAccountForModelTier("work-pro", 10)
	if err != nil {
		t.Fatalf("GetAccountForModelTier: %v", err)
	}
	if got.ID != 2 {
		t.Fatalf("selected account ID = %d, want 2", got.ID)
	}
}

func TestRecordTokenCreditUsage_ResetsStaleMonth(t *testing.T) {
	db := installAccountPoolTestDB(t)
	oldMonth := "2026-04"
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	acc := workagentModel.AgentAccount{
		ID:                        7,
		Name:                      "token-budget",
		Provider:                  "anthropic",
		BaseURL:                   "https://api.anthropic.com/v1",
		APIKey:                    "sk-token",
		Priority:                  5,
		Status:                    1,
		MonthlyTokenBudgetCredits: 100,
		MonthlyTokenCreditsUsed:   90,
		MonthlyTokenBudgetMonth:   oldMonth,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatalf("seed account: %v", err)
	}

	p := &AgentAccountPool{}
	p.RecordTokenCreditUsage(7, 12, now)

	var fresh workagentModel.AgentAccount
	if err := db.First(&fresh, 7).Error; err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if fresh.MonthlyTokenCreditsUsed != 12 {
		t.Fatalf("monthly used = %d, want 12", fresh.MonthlyTokenCreditsUsed)
	}
	if fresh.MonthlyTokenBudgetMonth != "2026-05" {
		t.Fatalf("budget month = %q, want 2026-05", fresh.MonthlyTokenBudgetMonth)
	}
}

func TestAgentAccountPool_ShutdownIsIdempotent(t *testing.T) {
	// Shutdown protected by sync.Once so a double-call doesn't panic
	// on a closed channel. Tests the contract directly because the
	// production wire-up is "called from SIGTERM handler that may run
	// twice" territory.
	p := &AgentAccountPool{
		statUpdates: make(chan func(), 4),
		flushStop:   make(chan struct{}),
	}
	p.Shutdown()
	p.Shutdown() // must not panic
}

func installAccountPoolTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_account (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		provider TEXT NOT NULL,
		base_url TEXT NOT NULL,
		api_key TEXT NOT NULL,
		is_premium INTEGER NOT NULL DEFAULT 0,
		priority INTEGER NOT NULL DEFAULT 5,
		status INTEGER NOT NULL DEFAULT 1,
		is_active INTEGER NOT NULL DEFAULT 0,
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		total_requests INTEGER NOT NULL DEFAULT 0,
		failed_requests INTEGER NOT NULL DEFAULT 0,
		last_used_at DATETIME,
		last_error TEXT,
		last_error_at DATETIME,
		monthly_token_budget_credits INTEGER NOT NULL DEFAULT 0,
		monthly_token_credits_used INTEGER NOT NULL DEFAULT 0,
		monthly_token_budget_month TEXT NOT NULL DEFAULT '',
		remark TEXT,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create account table: %v", err)
	}
	if globals.GraDBs == nil {
		globals.GraDBs = make(map[string]*gorm.DB)
	}
	prev := globals.GraDBs["system"]
	globals.GraDBs["system"] = db
	t.Cleanup(func() {
		if prev != nil {
			globals.GraDBs["system"] = prev
		} else {
			delete(globals.GraDBs, "system")
		}
	})
	return db
}

func TestAgentAccountPool_SubmitAfterShutdownDoesNotPanic(t *testing.T) {
	// "send on closed channel" would panic the request handler. We
	// recover and downgrade to a drop-with-warn so a Shutdown racing
	// with a request finishes cleanly.
	p := &AgentAccountPool{
		statUpdates: make(chan func(), 4),
		flushStop:   make(chan struct{}),
	}
	p.Shutdown()
	p.submitStatUpdate("post_shutdown", func() {})
}

// runStatUpdate is the per-task recover wrapper. A panic inside fn()
// must NOT escape — without this guard, a single bad update kills the
// worker goroutine and the pool degrades over time as panics
// accumulate. Tests that the panic is absorbed and the call returns
// normally.
func TestRunStatUpdate_AbsorbsPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("runStatUpdate leaked a panic: %v", r)
		}
	}()
	runStatUpdate(func() { panic("simulated DB driver explosion") })
}

// Happy path: clean fn() runs and returns normally — pin so a
// future "harden everything" pass can't silently swallow legitimate
// completion.
func TestRunStatUpdate_RunsCleanFn(t *testing.T) {
	called := false
	runStatUpdate(func() { called = true })
	if !called {
		t.Error("runStatUpdate did not invoke its fn")
	}
}

// snapshotBreakers must hand back the live *CircuitBreaker pointers
// (not copies) so admin-side reads see the current state. A future
// refactor that accidentally cloned the breakers would freeze admin
// dashboards on stale state during an outage.
func TestAgentAccountPool_SnapshotBreakersSharesLiveState(t *testing.T) {
	p := &AgentAccountPool{
		circuitBreakers: map[uint64]*CircuitBreaker{
			11: NewCircuitBreaker(11),
			22: NewCircuitBreaker(22),
		},
	}

	snap := p.snapshotBreakers()
	if len(snap) != 2 {
		t.Fatalf("snapshot size = %d, want 2", len(snap))
	}

	// Mutate via the snapshot — original map MUST observe the change
	// because the snapshot stores live pointers.
	for i := 0; i < 5; i++ {
		snap[11].RecordFailure()
	}
	if got := p.circuitBreakers[11].ConsecutiveFailures(); got != 5 {
		t.Errorf("source breaker failures via snapshot = %d, want 5", got)
	}
	if got := p.circuitBreakers[11].GetState(); got != "open" {
		t.Errorf("source breaker state via snapshot = %q, want open", got)
	}

	// Snapshot is a map clone (read-cheap), so adding to the snapshot
	// must NOT touch the source map.
	snap[33] = NewCircuitBreaker(33)
	if _, exists := p.circuitBreakers[33]; exists {
		t.Error("snapshot mutation leaked into source breaker map")
	}
}

// TestRecord_AccumulatorCoalescesPerAccount verifies the D5 design:
// N RecordFailure calls for the same account fold into a single
// accumulator entry instead of N queue submissions, so a 429 storm
// can't saturate the bounded statUpdates channel and lose
// last_error fidelity. Reads the accumulator state directly rather
// than waiting for a flusher tick (which would need a real DB).
func TestRecord_AccumulatorCoalescesPerAccount(t *testing.T) {
	p := &AgentAccountPool{
		circuitBreakers: map[uint64]*CircuitBreaker{
			77: NewCircuitBreaker(77),
		},
		accountStats: make(map[uint64]*accountStatAccum),
		statUpdates:  make(chan func(), 4),
		flushStop:    make(chan struct{}),
	}
	t.Cleanup(func() { p.Shutdown() })

	// 200 failures + 50 successes against one account. With the prior
	// design this would have submitted 250 closures to a 4-slot queue
	// and dropped most. With the accumulator, all 250 fold into one
	// entry.
	for i := 0; i < 200; i++ {
		p.RecordFailure(77, errBoom("upstream timeout"))
	}
	for i := 0; i < 50; i++ {
		p.RecordSuccess(77)
	}

	if got := len(p.accountStats); got != 1 {
		t.Fatalf("accountStats size = %d, want 1", got)
	}
	acc := p.accountStats[77]
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if !acc.dirty {
		t.Error("accumulator should be dirty")
	}
	if acc.totalReqsDelta != 250 {
		t.Errorf("totalReqsDelta = %d, want 250", acc.totalReqsDelta)
	}
	if acc.failedReqsDelta != 200 {
		t.Errorf("failedReqsDelta = %d, want 200", acc.failedReqsDelta)
	}
	if acc.lastError != "upstream timeout" {
		t.Errorf("lastError = %q, want upstream timeout", acc.lastError)
	}
}

// errBoom is a tiny error type so RecordFailure has something with an
// .Error() method. Avoids dragging in errors.New in a hot loop.
type errBoom string

func (e errBoom) Error() string { return string(e) }

// AccountHealth's JSON tags drive the admin dashboard's API contract.
// A renamed field (e.g. breaker_state → breakerState) would silently
// break the frontend without compiler help; this test pins the wire
// shape.
func TestAccountHealth_JSONTags(t *testing.T) {
	h := AccountHealth{
		ID:                        42,
		Name:                      "test",
		Provider:                  "anthropic",
		Status:                    1,
		IsActive:                  true,
		IsPremium:                 true,
		Priority:                  5,
		BreakerState:              "half-open",
		ConsecutiveFailures:       3,
		TotalRequests:             100,
		FailedRequests:            10,
		SuccessRate:               90.0,
		LastError:                 "boom",
		MonthlyTokenBudgetCredits: 100,
		MonthlyTokenCreditsUsed:   25,
		MonthlyTokenBudgetMonth:   "2026-05",
	}
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)

	required := []string{
		`"id":42`,
		`"name":"test"`,
		`"provider":"anthropic"`,
		`"is_active":true`,
		`"is_premium":true`,
		`"breaker_state":"half-open"`,
		`"consecutive_failures":3`,
		`"total_requests":100`,
		`"failed_requests":10`,
		`"success_rate":90`,
		`"last_error":"boom"`,
		`"monthly_token_budget_credits":100`,
		`"monthly_token_credits_used":25`,
		`"monthly_token_budget_month":"2026-05"`,
	}
	for _, want := range required {
		if !contains(got, want) {
			t.Errorf("AccountHealth JSON missing %q\nfull payload: %s", want, got)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Sanity: snapshotBreakers takes an RLock; running it in parallel
// with other readers must not deadlock or race. Pins the boundary so
// a future refactor that promotes the read to a write-lock surfaces
// here under -race instead of in production.
func TestAgentAccountPool_SnapshotBreakersConcurrentReads(t *testing.T) {
	p := &AgentAccountPool{
		circuitBreakers: map[uint64]*CircuitBreaker{
			1: NewCircuitBreaker(1),
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap := p.snapshotBreakers()
			if len(snap) != 1 {
				t.Errorf("concurrent snapshot size = %d, want 1", len(snap))
			}
		}()
	}
	wg.Wait()
}
