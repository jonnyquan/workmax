package workagent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

// ============================================================================
// Agent Account Pool (账号池管理器)
// ============================================================================

// AgentAccountPool 账号池管理器（无内存缓存，直接查库）
type AgentAccountPool struct {
	mu              sync.RWMutex
	circuitBreakers map[uint64]*CircuitBreaker // 熔断器状态保留在内存

	// statUpdates is a bounded work queue for fire-and-forget DB stat
	// updates from the LOW-FREQUENCY admin paths (TestAccount runs).
	// Hot-path RecordSuccess / RecordFailure calls bypass this queue
	// entirely and instead update the per-account accumulator below;
	// see accountStats / flushStatsLoop. The queue path used to handle
	// every Record* call which meant a 429 storm at 500 connections
	// saturated the 128-buffer / 4-worker pool and dropped failure
	// stats — last_error went stale right when ops needed it.
	statUpdates chan func()

	// accountStats holds per-account dirty counters that the flusher
	// goroutine drains every flushStatsInterval into ONE UPDATE per
	// dirty account. Coalescing means N failures for the same account
	// in one window fold into one row write, so DB write traffic stays
	// bounded by active-account count rather than by request rate.
	accountStatsMu sync.Mutex
	accountStats   map[uint64]*accountStatAccum

	// autoSwitchInProgress deduplicates concurrent AutoSwitchAccount
	// launches. Without this, many requests crossing the 5-consecutive-
	// failure threshold within the same window would each fire a
	// goroutine contending on the same DB transaction.
	autoSwitchInProgress atomic.Bool

	// shutdownOnce guards the close of statUpdates so Shutdown is
	// idempotent. Closing a closed channel panics.
	shutdownOnce sync.Once

	// flushStop signals the flusher goroutine to drain pending state
	// and exit. Closed by Shutdown; the flusher does one final flush
	// before returning so we don't lose stats accumulated since the
	// last tick.
	flushStop chan struct{}
}

// accountStatAccum holds pending stat deltas for one account between
// flush ticks. All fields guarded by mu. The flusher snapshots and
// resets the accumulator under mu, so producer-side updates and
// consumer-side reads can't race on partial state.
type accountStatAccum struct {
	mu              sync.Mutex
	totalReqsDelta  int64
	failedReqsDelta int64
	lastError       string
	lastErrorAt     time.Time
	lastUsedAt      time.Time
	dirty           bool
}

const (
	statUpdateWorkers    = 4
	statUpdateBufferSize = 128

	// flushStatsInterval is how often the flusher coalesces pending
	// per-account stat deltas into UPDATE statements. 2s gives ops
	// near-real-time visibility into last_error for an outage in
	// progress without hammering the DB during a healthy storm —
	// peak write traffic is now bounded by active-account count
	// (typically <50) per 2s rather than scaling with request rate.
	flushStatsInterval = 2 * time.Second
)

var (
	globalAccountPool     *AgentAccountPool
	accountPoolOnce       sync.Once
	ErrNoAccountAvailable = errors.New("no account available")
)

// GetAgentAccountPool 获取全局账号池单例
func GetAgentAccountPool() *AgentAccountPool {
	accountPoolOnce.Do(func() {
		globalAccountPool = &AgentAccountPool{
			circuitBreakers: make(map[uint64]*CircuitBreaker),
			statUpdates:     make(chan func(), statUpdateBufferSize),
			accountStats:    make(map[uint64]*accountStatAccum),
			flushStop:       make(chan struct{}),
		}
		for i := 0; i < statUpdateWorkers; i++ {
			go globalAccountPool.statUpdateWorker()
		}
		go globalAccountPool.flushStatsLoop()
	})
	return globalAccountPool
}

// statUpdateWorker drains the stat update queue until the channel is
// closed by Shutdown. Multiple workers run concurrently — see
// statUpdateWorkers.
//
// A panic inside fn() is recovered per-iteration so the worker keeps
// draining. Without this guard, one bad update (gorm nil-deref,
// driver-level surprise) would kill the worker permanently and the
// pool would degrade towards 0 workers as unrelated panics
// accumulated over the process lifetime.
func (p *AgentAccountPool) statUpdateWorker() {
	for fn := range p.statUpdates {
		runStatUpdate(fn)
	}
}

func runStatUpdate(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("[AgentAccountPool] Stat update panic recovered: %v", r))
		}
	}()
	fn()
}

// Shutdown signals the worker pool and flusher to drain and exit.
// Safe to call multiple times — protected by sync.Once. Buffered
// updates that have already been enqueued still get executed; the
// flusher does one final pass over the per-account accumulators
// before returning so stats accrued since the last tick aren't lost.
// New submitStatUpdate calls after Shutdown silently drop with a warn
// log (channel is closed).
func (p *AgentAccountPool) Shutdown() {
	p.shutdownOnce.Do(func() {
		close(p.flushStop)
		close(p.statUpdates)
	})
}

// submitStatUpdate enqueues a DB stat update for asynchronous execution.
// Drops the update (with a warn log) if the queue is saturated or the
// pool has been shut down. The recover guard catches "send on closed
// channel" so callers running after Shutdown don't panic the request
// handler — they just get the same drop-with-warn semantics as a full
// queue.
func (p *AgentAccountPool) submitStatUpdate(label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			globals.Warn(fmt.Sprintf("[AgentAccountPool] Stat update post-shutdown drop: %s", label))
		}
	}()
	select {
	case p.statUpdates <- fn:
	default:
		globals.Warn(fmt.Sprintf("[AgentAccountPool] Stat update queue full, dropping %s", label))
	}
}

// getOrCreateStatAccum returns the per-account accumulator, creating
// one on first call. Bounded by active-account count which is small
// (typically <50) for the lifetime of the process.
func (p *AgentAccountPool) getOrCreateStatAccum(accountID uint64) *accountStatAccum {
	p.accountStatsMu.Lock()
	defer p.accountStatsMu.Unlock()
	if acc, ok := p.accountStats[accountID]; ok {
		return acc
	}
	acc := &accountStatAccum{}
	p.accountStats[accountID] = acc
	return acc
}

// flushStatsLoop wakes every flushStatsInterval and emits one UPDATE
// per dirty account. Exits on flushStop, doing one final flush so we
// don't lose stats accumulated since the last tick.
//
// Per-iteration recover so a single bad UPDATE (gorm panic, driver
// surprise, malformed last_error bytes) doesn't kill the whole
// flusher. Without this, one bad payload would silently stop all
// future stats from landing in the DB.
func (p *AgentAccountPool) flushStatsLoop() {
	ticker := time.NewTicker(flushStatsInterval)
	defer ticker.Stop()

	flush := func() {
		defer func() {
			if r := recover(); r != nil {
				globals.Error(fmt.Sprintf("[AgentAccountPool] flushStats panic recovered: %v", r))
			}
		}()
		p.flushDirtyStats()
	}

	for {
		select {
		case <-p.flushStop:
			flush() // final flush before exit
			return
		case <-ticker.C:
			flush()
		}
	}
}

// flushDirtyStats walks the accumulator map, snapshots each dirty
// entry under its own mu (resetting the deltas in the same critical
// section), and emits one parameterized UPDATE per account. Reads
// the live consecutive_failures from the in-memory CircuitBreaker so
// the persisted column matches the routing decision made in
// CanAttempt — there's no DB read in the hot path.
func (p *AgentAccountPool) flushDirtyStats() {
	// Snapshot the accumulator pointer slice under the map lock so
	// the actual per-account flushes can run without holding it.
	// Each accumulator has its own mu for the snapshot+reset.
	p.accountStatsMu.Lock()
	pending := make([]uint64, 0, len(p.accountStats))
	for id := range p.accountStats {
		pending = append(pending, id)
	}
	p.accountStatsMu.Unlock()

	if len(pending) == 0 {
		return
	}

	db := globals.GraDBs["system"]
	for _, accID := range pending {
		p.accountStatsMu.Lock()
		acc, ok := p.accountStats[accID]
		p.accountStatsMu.Unlock()
		if !ok {
			continue
		}

		acc.mu.Lock()
		if !acc.dirty {
			acc.mu.Unlock()
			continue
		}
		totalDelta := acc.totalReqsDelta
		failedDelta := acc.failedReqsDelta
		lastErr := acc.lastError
		lastErrAt := acc.lastErrorAt
		lastUsedAt := acc.lastUsedAt
		// Reset under the same lock so any updates between snapshot
		// and reset can't be lost.
		acc.totalReqsDelta = 0
		acc.failedReqsDelta = 0
		acc.lastError = ""
		acc.lastErrorAt = time.Time{}
		acc.dirty = false
		acc.mu.Unlock()

		// Read live consecutive_failures from the in-memory CB so the
		// persisted value matches the routing decision. cb may be nil
		// during early init / late shutdown — fall back to "leave the
		// column as-is" (-1 sentinel handled by the SQL below).
		consecutive := -1
		p.mu.RLock()
		if cb := p.circuitBreakers[accID]; cb != nil {
			consecutive = cb.ConsecutiveFailures()
		}
		p.mu.RUnlock()

		// One UPDATE per account, parameterised. The conditional
		// last_error update preserves the prior value when no new
		// error landed in this window — important for ops forensics.
		var execErr error
		if lastErr != "" && consecutive >= 0 {
			execErr = db.Exec(
				"UPDATE w_workagent_account SET total_requests = total_requests + ?, failed_requests = failed_requests + ?, consecutive_failures = ?, last_error = ?, last_error_at = ?, last_used_at = ? WHERE id = ?",
				totalDelta, failedDelta, consecutive, lastErr, lastErrAt, lastUsedAt, accID,
			).Error
		} else if consecutive >= 0 {
			execErr = db.Exec(
				"UPDATE w_workagent_account SET total_requests = total_requests + ?, failed_requests = failed_requests + ?, consecutive_failures = ?, last_used_at = ? WHERE id = ?",
				totalDelta, failedDelta, consecutive, lastUsedAt, accID,
			).Error
		} else if lastErr != "" {
			execErr = db.Exec(
				"UPDATE w_workagent_account SET total_requests = total_requests + ?, failed_requests = failed_requests + ?, last_error = ?, last_error_at = ?, last_used_at = ? WHERE id = ?",
				totalDelta, failedDelta, lastErr, lastErrAt, lastUsedAt, accID,
			).Error
		} else {
			execErr = db.Exec(
				"UPDATE w_workagent_account SET total_requests = total_requests + ?, failed_requests = failed_requests + ?, last_used_at = ? WHERE id = ?",
				totalDelta, failedDelta, lastUsedAt, accID,
			).Error
		}
		if execErr != nil {
			globals.Error(fmt.Sprintf("[AgentAccountPool] Flush stats failed for account %d: %v", accID, execErr))
		}
	}
}

// Initialize 初始化账号池（初始化熔断器）
func (p *AgentAccountPool) Initialize() error {
	// 获取所有账号ID，初始化熔断器
	var accountIDs []uint64
	if err := globals.GraDBs["system"].Model(&workagentModel.AgentAccount{}).
		Where("deleted_at IS NULL").
		Pluck("id", &accountIDs).Error; err != nil {
		return fmt.Errorf("failed to load account IDs: %w", err)
	}

	p.mu.Lock()
	for _, id := range accountIDs {
		if _, exists := p.circuitBreakers[id]; !exists {
			p.circuitBreakers[id] = NewCircuitBreaker(id)
		}
	}
	p.mu.Unlock()

	// 检查是否有活跃账号
	activeAccount, err := p.GetActiveAccount()
	if err != nil {
		return fmt.Errorf("failed to get active account: %w", err)
	}

	globals.Info(fmt.Sprintf("[AgentAccountPool] Initialized with %d accounts, active: %d (%s)",
		len(accountIDs), activeAccount.ID, activeAccount.Name))

	return nil
}

// GetActiveAccount 获取当前活跃账号（直接查库）
func (p *AgentAccountPool) GetActiveAccount() (*workagentModel.AgentAccount, error) {
	var account workagentModel.AgentAccount

	// 直接从数据库查询活跃账号
	err := globals.GraDBs["system"].Where("is_active = ? AND status = ? AND deleted_at IS NULL", true, 1).
		First(&account).Error

	if err != nil {
		return nil, fmt.Errorf("no active account found: %w", err)
	}

	// 检查熔断器
	p.mu.RLock()
	cb := p.circuitBreakers[account.ID]
	p.mu.RUnlock()

	if cb != nil && !cb.CanAttempt() {
		return nil, fmt.Errorf("active account %d circuit breaker is open", account.ID)
	}

	// 计算成功率
	account.PrepareForDisplay()

	return &account, nil
}

// GetAccountForModelTier returns the best available account for the requested model tier.
// Selection rules:
// - work-plus: query only premium accounts (is_premium = 1)
// - work-pro: query all enabled accounts
//
// expectedCredits is optional for legacy call sites. When supplied,
// accounts whose current-month token-spend budget cannot fit the
// predicted turn cost are skipped before routing. We deliberately use
// settled credits here because the SDK reports token spend as USD, not
// raw tokens; pricing.AgentCostFromUSD converts that spend to credits.
func (p *AgentAccountPool) GetAccountForModelTier(modelTier string, expectedCredits ...int) (*workagentModel.AgentAccount, error) {
	requestTier := normalizeRequestedTier(modelTier)
	neededCredits := 0
	if len(expectedCredits) > 0 && expectedCredits[0] > 0 {
		neededCredits = expectedCredits[0]
	}
	budgetMonth := accountTokenBudgetMonth(time.Now())

	baseQuery := func() *gorm.DB {
		q := globals.GraDBs["system"].Where("status = ? AND deleted_at IS NULL", 1)
		if requestTier == "work-plus" {
			q = q.Where("is_premium = ?", 1)
		}
		return q
	}

	// Snapshot breakers under RLock, then run DB queries lock-free. Holding
	// the pool's RLock across the DB calls would block every concurrent
	// RecordSuccess/RecordFailure (Lock-takers) for the full DB latency —
	// exactly what we want to avoid during a slow-DB / model-outage storm.
	breakers := p.snapshotBreakers()

	// Prefer the currently active account for the requested tier.
	var active workagentModel.AgentAccount
	if err := baseQuery().
		Where("is_active = ?", true).
		First(&active).Error; err == nil {
		if (breakers[active.ID] == nil || breakers[active.ID].CanAttempt()) && accountTokenBudgetCanFit(&active, neededCredits, budgetMonth) {
			active.PrepareForDisplay()
			return &active, nil
		}
	}

	// Fallback: choose best available by priority.
	var accounts []workagentModel.AgentAccount
	err := baseQuery().Order("priority DESC, id ASC").Find(&accounts).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load accounts: %w", err)
	}

	for i := range accounts {
		account := &accounts[i]
		cb := breakers[account.ID]
		if cb != nil && !cb.CanAttempt() {
			continue
		}
		if !accountTokenBudgetCanFit(account, neededCredits, budgetMonth) {
			continue
		}

		account.PrepareForDisplay()
		return account, nil
	}

	if requestTier == "work-plus" {
		return nil, fmt.Errorf("no work-plus premium account available")
	}

	return nil, ErrNoAccountAvailable
}

func accountTokenBudgetMonth(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return now.Format("2006-01")
}

func accountTokenBudgetUsageForMonth(account *workagentModel.AgentAccount, budgetMonth string) int {
	if account == nil || strings.TrimSpace(budgetMonth) == "" {
		return 0
	}
	if strings.TrimSpace(account.MonthlyTokenBudgetMonth) != budgetMonth {
		return 0
	}
	if account.MonthlyTokenCreditsUsed < 0 {
		return 0
	}
	return account.MonthlyTokenCreditsUsed
}

func accountTokenBudgetCanFit(account *workagentModel.AgentAccount, neededCredits int, budgetMonth string) bool {
	if account == nil {
		return false
	}
	if account.MonthlyTokenBudgetCredits <= 0 || neededCredits <= 0 {
		return true
	}
	used := accountTokenBudgetUsageForMonth(account, budgetMonth)
	return used+neededCredits <= account.MonthlyTokenBudgetCredits
}

// RecordTokenCreditUsage adds the settled token-spend credits for a
// successful turn to the account's current monthly budget bucket. It is
// called after conversation persistence succeeds, so failed/save-lost
// turns do not consume account budget.
func (p *AgentAccountPool) RecordTokenCreditUsage(accountID uint64, usedCredits int, now time.Time) {
	if accountID == 0 || usedCredits <= 0 {
		return
	}
	budgetMonth := accountTokenBudgetMonth(now)
	if err := globals.GraDBs["system"].Exec(
		`UPDATE w_workagent_account
		 SET monthly_token_credits_used = CASE
		     WHEN monthly_token_budget_month = ? THEN monthly_token_credits_used + ?
		     ELSE ?
		   END,
		   monthly_token_budget_month = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		budgetMonth, usedCredits, usedCredits, budgetMonth, accountID,
	).Error; err != nil {
		globals.Warn(fmt.Sprintf("[AgentAccountPool] Failed to record token credit usage for account %d: %v", accountID, err))
	}
}

// snapshotBreakers returns a copy of the breaker map taken under RLock so
// callers can iterate / index it without holding the pool mutex across
// blocking I/O. Entries point at the same *CircuitBreaker values, which
// have their own internal mutex, so concurrent reads are still safe.
func (p *AgentAccountPool) snapshotBreakers() map[uint64]*CircuitBreaker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[uint64]*CircuitBreaker, len(p.circuitBreakers))
	for id, cb := range p.circuitBreakers {
		out[id] = cb
	}
	return out
}

func normalizeRequestedTier(tier string) string {
	normalized := strings.ToLower(strings.TrimSpace(tier))
	if normalized == "work-plus" {
		return "work-plus"
	}
	return "work-pro"
}

// RecordSuccess 记录成功请求.
//
// Hot-path: bumps the in-memory CircuitBreaker (closes / resets
// consecutive_failures) and increments the per-account stat
// accumulator. The flusher goroutine coalesces N successes per
// account into ONE UPDATE per flushStatsInterval — vs. the prior
// shape that fired one DB UPDATE per call and saturated the bounded
// stat-update queue under load.
func (p *AgentAccountPool) RecordSuccess(accountID uint64) {
	p.mu.Lock()
	if cb := p.circuitBreakers[accountID]; cb != nil {
		cb.RecordSuccess()
	}
	p.mu.Unlock()

	acc := p.getOrCreateStatAccum(accountID)
	acc.mu.Lock()
	acc.totalReqsDelta++
	acc.lastUsedAt = time.Now()
	acc.dirty = true
	acc.mu.Unlock()
}

// recordFailureMaxErrLen caps how many bytes of err.Error() we persist
// to account.last_error. The column is TEXT (up to 64 KiB) but upstream
// errors during a model outage routinely include the full HTTP body or
// a stack trace stretching to many KiB. RecordFailure UPDATEs this row
// on every failed request, so a 60 KiB error rewritten on every call
// during a sustained 429 storm pumps the row's worth of write traffic
// through replication and binlog at exactly the moment DB pressure is
// already high. 1 KiB is enough to identify the failure mode in admin
// tooling without amplifying the incident.
const recordFailureMaxErrLen = 1024

// RecordFailure 记录失败请求（直接更新数据库）
func (p *AgentAccountPool) RecordFailure(accountID uint64, err error) {
	errorMsg := err.Error()
	if len(errorMsg) > recordFailureMaxErrLen {
		// Cap is byte-denominated (matches the DB write-traffic intent
		// in recordFailureMaxErrLen's docstring), but the cut must
		// land on a rune boundary so a CJK upstream error doesn't
		// store a partial UTF-8 sequence. The previous shape mixed
		// byte and rune caps: outer `len > 1024` was bytes, inner
		// `runes > 1024` was rune count. For a 1500-byte CJK error
		// (~500 runes at 3 bytes/rune), the outer check passed and
		// inner failed → no truncation, full 1500 bytes persisted on
		// every failed request during a model-gateway storm. Walk
		// back from the byte cap to the previous rune start.
		cut := recordFailureMaxErrLen
		for cut > 0 && !utf8.RuneStart(errorMsg[cut]) {
			cut--
		}
		errorMsg = errorMsg[:cut] + "…(truncated)"
	}

	// 更新熔断器并读出当前失败计数（in-memory，O(1)）。
	// 之前这里再 Pluck 一次 DB 来拿 consecutive_failures，结果在模型
	// 故障期间每次失败都同步等 DB —— 正好在 DB 也慢的时候放大延迟。
	// 现在改为读 CircuitBreaker 自己的计数，DB UPDATE 仍走异步队列。
	consecutiveFailures := 0
	p.mu.Lock()
	if cb := p.circuitBreakers[accountID]; cb != nil {
		cb.RecordFailure()
		// RecordFailure 已经把计数 +1，再读出来就是 "post-increment" 值。
		consecutiveFailures = cb.ConsecutiveFailures()
	}
	p.mu.Unlock()

	// Hot-path: stash the failure into the per-account accumulator.
	// The flusher goroutine coalesces N failures per account into one
	// UPDATE per flushStatsInterval. Live consecutive_failures comes
	// from the in-memory CircuitBreaker at flush time so the persisted
	// column matches the routing decision regardless of how many
	// failures pile up between flushes — vs. the prior `consecutive
	// _failures = consecutive_failures + 1` increment which lost
	// fidelity if the queue dropped a stat update.
	acc := p.getOrCreateStatAccum(accountID)
	acc.mu.Lock()
	acc.totalReqsDelta++
	acc.failedReqsDelta++
	now := time.Now()
	acc.lastError = errorMsg
	acc.lastErrorAt = now
	acc.lastUsedAt = now
	acc.dirty = true
	acc.mu.Unlock()

	// 检查是否需要自动切换（5次失败阈值）
	if consecutiveFailures >= 5 {
		globals.Warn(fmt.Sprintf(
			"[AgentAccountPool] Account %d has %d consecutive failures, triggering auto-switch",
			accountID, consecutiveFailures))

		// Dedupe: only one AutoSwitchAccount goroutine at a time. The
		// CAS-guarded goroutine clears the flag on exit so a genuinely
		// later storm can still trigger a switch.
		if p.autoSwitchInProgress.CompareAndSwap(false, true) {
			failures := consecutiveFailures
			go func() {
				defer p.autoSwitchInProgress.Store(false)
				// AutoSwitchAccount talks to the DB and to the
				// agent client manager. A panic here (nil deref
				// in a future refactor, gorm bug, etc.) would
				// crash the whole process — Go's goroutine
				// panic propagation has no parent to catch it.
				// Recover so the failover machinery degrades
				// gracefully instead of taking the server down.
				defer func() {
					if r := recover(); r != nil {
						globals.Error(fmt.Sprintf("[AgentAccountPool] Auto-switch panic recovered: %v", r))
					}
				}()
				reason := fmt.Sprintf("auto-failover: %d consecutive failures", failures)
				if switchErr := p.AutoSwitchAccount(reason); switchErr != nil {
					globals.Error(fmt.Sprintf("[AgentAccountPool] Auto-switch failed: %v", switchErr))
				}
			}()
		}
	}
}

// ============================================================================
// 管理API方法
// ============================================================================
