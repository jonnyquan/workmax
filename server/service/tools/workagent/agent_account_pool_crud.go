package workagent

import (
	"errors"
	"fmt"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
)

// Account CRUD methods used by the admin UI. Hosted on AgentAccountPool
// (rather than a separate AdminApi) because creating/deleting accounts
// also needs to keep the in-memory circuit-breaker map in sync — the
// pool owns that map and is the natural location.

// GetAllAccounts returns every non-deleted account row, with the
// PrepareForDisplay-computed fields filled in (success rate etc.).
// Errors are logged and an empty slice is returned because the admin
// UI calls this on every page load and an empty list is friendlier
// than a 500.
func (p *AgentAccountPool) GetAllAccounts() []*workagentModel.AgentAccount {
	var accounts []workagentModel.AgentAccount
	err := globals.GraDBs["system"].Where("deleted_at IS NULL").Find(&accounts).Error
	if err != nil {
		globals.Error(fmt.Sprintf("[AgentAccountPool] Failed to load accounts: %v", err))
		return []*workagentModel.AgentAccount{}
	}

	result := make([]*workagentModel.AgentAccount, len(accounts))
	for i := range accounts {
		accounts[i].PrepareForDisplay()
		result[i] = &accounts[i]
	}
	return result
}

// GetActiveAccountID returns the id of the currently active account
// or 0 if none is active. Used by switch flows and the admin UI's
// "current account" indicator.
func (p *AgentAccountPool) GetActiveAccountID() uint64 {
	var accountID uint64
	// Surface DB errors. Silently swallowing the Pluck error meant a
	// transient connection blip during admin-page load returned 0 —
	// indistinguishable from "no active account" in the UI, which
	// could prompt the operator to forcibly switch a different account
	// active when the actual one was fine. Same posture as the silent
	// thread-lookup fix in b0766676.
	if err := globals.GraDBs["system"].Model(&workagentModel.AgentAccount{}).
		Where("is_active = ? AND deleted_at IS NULL", true).
		Pluck("id", &accountID).Error; err != nil {
		globals.Warn(fmt.Sprintf("[AgentAccountPool] GetActiveAccountID Pluck failed: %v (returning 0; admin UI may show 'no active account' incorrectly)", err))
	}
	return accountID
}

// GetAccountByID looks up a single non-deleted account.
func (p *AgentAccountPool) GetAccountByID(id uint64) (*workagentModel.AgentAccount, error) {
	var account workagentModel.AgentAccount
	err := globals.GraDBs["system"].Where("id = ? AND deleted_at IS NULL", id).First(&account).Error
	if err != nil {
		return nil, fmt.Errorf("account %d not found", id)
	}
	account.PrepareForDisplay()
	return &account, nil
}

// CreateAccount persists a new account row and registers a fresh
// circuit breaker for it. The breaker map must stay in sync with the
// DB so first-request-after-create doesn't accidentally race against
// a missing-breaker check.
func (p *AgentAccountPool) CreateAccount(account *workagentModel.AgentAccount) error {
	if err := globals.GraDBs["system"].Create(account).Error; err != nil {
		return err
	}

	p.mu.Lock()
	p.circuitBreakers[account.ID] = NewCircuitBreaker(account.ID)
	p.mu.Unlock()

	globals.Info(fmt.Sprintf("[AgentAccountPool] Created account: %s (ID: %d)", account.Name, account.ID))
	return nil
}

// UpdateAccount writes the supplied account back to DB. If the caller
// passes an empty APIKey, the existing value is preserved — this lets
// the admin UI submit "edit name only" without re-typing the key.
// Doesn't touch the circuit breaker — the breaker is keyed by id, not
// API key, so an update can't invalidate the breaker.
func (p *AgentAccountPool) UpdateAccount(account *workagentModel.AgentAccount) error {
	if account.APIKey == "" {
		// Read the existing key so the "edit name only" form doesn't
		// accidentally clear it. Surface DB errors instead of silently
		// proceeding with oldAPIKey="" — the previous version ignored
		// .Error, so a transient connection blip during this Pluck
		// would land an empty string into the row on the Save below
		// and the next SDK invocation 401'd with the credential
		// silently lost.
		var oldAPIKey string
		if err := globals.GraDBs["system"].Model(&workagentModel.AgentAccount{}).
			Where("id = ?", account.ID).
			Pluck("api_key", &oldAPIKey).Error; err != nil {
			return fmt.Errorf("failed to read existing api_key for account %d: %w", account.ID, err)
		}
		if oldAPIKey == "" {
			// Pluck succeeded but returned no rows: the account was
			// soft-deleted or the id is wrong. Refuse the update so we
			// don't create a row with an empty key.
			return fmt.Errorf("account %d not found (cannot preserve empty api_key)", account.ID)
		}
		account.APIKey = oldAPIKey
	}

	if err := globals.GraDBs["system"].Save(account).Error; err != nil {
		return err
	}

	globals.Info(fmt.Sprintf("[AgentAccountPool] Updated account: %s (ID: %d)", account.Name, account.ID))
	return nil
}

// DeleteAccount soft-deletes an account. Refuses to delete the
// currently active account so the system can never end up with no
// active account by accident — the admin must explicitly switch
// before deleting. Drops the breaker entry so a future re-create
// with the same id (unlikely in prod, possible in tests) gets a
// fresh state machine.
func (p *AgentAccountPool) DeleteAccount(accountID uint64) error {
	// Surface Pluck errors instead of silently treating them as
	// "not active". A transient DB blip on this read used to leave
	// isActive=false (Go zero value), and the function then proceeded
	// to soft-delete what could actually be THE active account —
	// exactly the failure mode the guard below was written to prevent.
	var isActive bool
	if err := globals.GraDBs["system"].Model(&workagentModel.AgentAccount{}).
		Where("id = ? AND deleted_at IS NULL", accountID).
		Pluck("is_active", &isActive).Error; err != nil {
		return fmt.Errorf("failed to read is_active for account %d: %w", accountID, err)
	}

	if isActive {
		return errors.New("cannot delete active account, please switch to another account first")
	}

	now := time.Now()
	if err := globals.GraDBs["system"].Model(&workagentModel.AgentAccount{}).
		Where("id = ?", accountID).
		Update("deleted_at", now).Error; err != nil {
		return err
	}

	p.mu.Lock()
	delete(p.circuitBreakers, accountID)
	p.mu.Unlock()

	globals.Info(fmt.Sprintf("[AgentAccountPool] Deleted account ID: %d", accountID))
	return nil
}

// AccountHealth is the per-account row of the pool health snapshot.
// Composed from the DB-stored AgentAccount counters + the in-memory
// circuit breaker so the admin UI can render the full pool in one
// request instead of issuing N GetAccountStats round-trips.
type AccountHealth struct {
	ID                        uint64  `json:"id"`
	Name                      string  `json:"name"`
	Provider                  string  `json:"provider"`
	Status                    int     `json:"status"`
	IsActive                  bool    `json:"is_active"`
	IsPremium                 bool    `json:"is_premium"`
	Priority                  int     `json:"priority"`
	BreakerState              string  `json:"breaker_state"`
	ConsecutiveFailures       int     `json:"consecutive_failures"`
	TotalRequests             int64   `json:"total_requests"`
	FailedRequests            int64   `json:"failed_requests"`
	SuccessRate               float64 `json:"success_rate"`
	LastError                 string  `json:"last_error"`
	MonthlyTokenBudgetCredits int     `json:"monthly_token_budget_credits"`
	MonthlyTokenCreditsUsed   int     `json:"monthly_token_credits_used"`
	MonthlyTokenBudgetMonth   string  `json:"monthly_token_budget_month"`
}

// GetPoolHealth returns the entire pool snapshot in one call. Combines
// each account's persisted counters with its in-memory breaker so the
// admin dashboard can render "is this account live, broken, or
// recovering?" without N+1 lookups.
func (p *AgentAccountPool) GetPoolHealth() []AccountHealth {
	var accounts []workagentModel.AgentAccount
	if err := globals.GraDBs["system"].
		Where("deleted_at IS NULL").
		Order("priority DESC, id ASC").
		Find(&accounts).Error; err != nil {
		globals.Error(fmt.Sprintf("[AgentAccountPool] GetPoolHealth: load accounts: %v", err))
		return nil
	}

	breakers := p.snapshotBreakers()
	out := make([]AccountHealth, 0, len(accounts))
	for i := range accounts {
		acc := &accounts[i]
		acc.PrepareForDisplay()

		state := "unknown"
		failures := acc.ConsecutiveFailures
		if cb := breakers[acc.ID]; cb != nil {
			state = cb.GetState()
			// Prefer the live in-memory counter — DB lags behind the
			// async stat-update queue and a flapping account's true
			// failure run shows up here first.
			failures = cb.ConsecutiveFailures()
		}

		out = append(out, AccountHealth{
			ID:                        acc.ID,
			Name:                      acc.Name,
			Provider:                  acc.Provider,
			Status:                    acc.Status,
			IsActive:                  acc.IsActive,
			IsPremium:                 acc.IsPremium,
			Priority:                  acc.Priority,
			BreakerState:              state,
			ConsecutiveFailures:       failures,
			TotalRequests:             acc.TotalRequests,
			FailedRequests:            acc.FailedRequests,
			SuccessRate:               acc.SuccessRate,
			LastError:                 acc.LastError,
			MonthlyTokenBudgetCredits: acc.MonthlyTokenBudgetCredits,
			MonthlyTokenCreditsUsed:   accountTokenBudgetUsageForMonth(acc, accountTokenBudgetMonth(time.Now())),
			MonthlyTokenBudgetMonth:   acc.MonthlyTokenBudgetMonth,
		})
	}
	return out
}

// GetAccountStats returns runtime stats for one account combining
// DB-stored counters (total/failed requests, last_error_at, etc.)
// with the in-memory breaker state. Used by the admin UI's per-account
// health panel.
func (p *AgentAccountPool) GetAccountStats(accountID uint64) map[string]interface{} {
	var account workagentModel.AgentAccount
	err := globals.GraDBs["system"].Where("id = ? AND deleted_at IS NULL", accountID).First(&account).Error
	if err != nil {
		return map[string]interface{}{
			"error": "account not found",
		}
	}

	account.PrepareForDisplay()

	p.mu.RLock()
	cbState := "unknown"
	if cb := p.circuitBreakers[accountID]; cb != nil {
		cbState = cb.GetState()
	}
	p.mu.RUnlock()

	return map[string]interface{}{
		"id":                        account.ID,
		"name":                      account.Name,
		"totalRequests":             account.TotalRequests,
		"failedRequests":            account.FailedRequests,
		"successRate":               account.SuccessRate,
		"consecutiveFailures":       account.ConsecutiveFailures,
		"circuitBreakerState":       cbState,
		"lastUsedAt":                account.LastUsedAt,
		"lastError":                 account.LastError,
		"lastErrorAt":               account.LastErrorAt,
		"monthlyTokenBudgetCredits": account.MonthlyTokenBudgetCredits,
		"monthlyTokenCreditsUsed":   accountTokenBudgetUsageForMonth(&account, accountTokenBudgetMonth(time.Now())),
		"monthlyTokenBudgetMonth":   account.MonthlyTokenBudgetMonth,
	}
}
