package workagent

import (
	"fmt"

	"server/globals"
	workagentModel "server/model/workagent"
)

// AutoSwitchAccount picks the best non-broken account other than the
// current active one and flips is_active over. Triggered by the
// per-account failure threshold in RecordFailure (5+ consecutive
// failures). Notifies admin via email through NotifyAccountSwitch so
// an outage doesn't go silently rerouted.
func (p *AgentAccountPool) AutoSwitchAccount(reason string) error {
	var oldAccount workagentModel.AgentAccount
	err := globals.GraDBs["system"].Where("is_active = ? AND deleted_at IS NULL", true).First(&oldAccount).Error
	if err != nil {
		return fmt.Errorf("no active account found: %w", err)
	}

	newAccount, err := p.selectBestAvailableAccount(oldAccount.ID)
	if err != nil {
		return err
	}

	return p.switchToAccountInternal(oldAccount.ID, newAccount.ID, reason)
}

// SwitchToAccount manually flips the active account to targetAccountID.
// Called from the admin UI. Validates the target exists and is enabled
// before delegating to the same internal switch path used by
// AutoSwitchAccount, so manual + auto switches share the
// reinit/notify side effects.
func (p *AgentAccountPool) SwitchToAccount(targetAccountID uint64, reason string) error {
	var account workagentModel.AgentAccount
	err := globals.GraDBs["system"].Where("id = ? AND deleted_at IS NULL", targetAccountID).First(&account).Error
	if err != nil {
		return fmt.Errorf("account %d not found", targetAccountID)
	}
	if account.Status != 1 {
		return fmt.Errorf("account %d is disabled", targetAccountID)
	}

	// Surface the Pluck error. The previous shape silently swallowed
	// it, leaving oldAccountID=0 on any DB failure; switchToAccountInternal
	// then skipped the "disable old" step (gated by `oldAccountID != 0`)
	// and only flipped the new account active — landing the system in
	// the exact "two active accounts" state the transactional comment
	// at line 58 promises will never occur. Same silent-error family
	// as 37d2186d (GetActiveAccountID); refusing the switch on lookup
	// failure preserves the invariant.
	var oldAccountID uint64
	if err := globals.GraDBs["system"].Model(&workagentModel.AgentAccount{}).
		Where("is_active = ? AND deleted_at IS NULL", true).
		Pluck("id", &oldAccountID).Error; err != nil {
		return fmt.Errorf("failed to read current active account before switch: %w", err)
	}

	return p.switchToAccountInternal(oldAccountID, targetAccountID, reason)
}

// switchToAccountInternal flips the is_active row in DB inside a
// transaction, reloads the new account, reinitializes the shared
// AgentClientManager with it (so the next request uses the new SDK
// config), and pushes an admin email about the switch.
//
// The transaction guarantees the system is never in an "two active"
// or "zero active" state when seen from a fresh DB query — at most
// the brief in-flight commit window. The previous implementation
// ignored .Error on each Update, so a successful disable-old +
// failed enable-new path could Commit and leave the system with
// zero active accounts, breaking every chat request until manual
// recovery. Each Update now rolls back on error.
func (p *AgentAccountPool) switchToAccountInternal(oldAccountID, newAccountID uint64, reason string) error {
	tx := globals.GraDBs["system"].Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to begin transaction: %w", tx.Error)
	}
	if oldAccountID != 0 {
		if err := tx.Model(&workagentModel.AgentAccount{}).
			Where("id = ?", oldAccountID).
			Update("is_active", false).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to disable old account %d: %w", oldAccountID, err)
		}
	}
	if err := tx.Model(&workagentModel.AgentAccount{}).
		Where("id = ?", newAccountID).
		Update("is_active", true).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to enable new account %d: %w", newAccountID, err)
	}
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to update DB: %w", err)
	}

	var newAccount workagentModel.AgentAccount
	if err := globals.GraDBs["system"].Where("id = ?", newAccountID).First(&newAccount).Error; err != nil {
		return fmt.Errorf("failed to load new account: %w", err)
	}

	manager := GetAgentClientManager()
	// The chat path goes through BuildClientForAccount per-request
	// and never reads back the singleton state ReinitializeWithAccount
	// updates. We still call it so tests / debug tools that hit the
	// legacy GetClient() path see a non-nil client. If
	// ReinitializeWithAccount is ever retired, drop this whole call.
	if err := manager.ReinitializeWithAccount(&newAccount); err != nil {
		return fmt.Errorf("failed to reinitialize client: %w", err)
	}

	globals.Info(fmt.Sprintf(
		"[AgentAccountPool] Switched from account %d to %d (%s), reason: %s",
		oldAccountID, newAccountID, newAccount.Name, reason))

	// Best-effort fetch of the old account name for the email body.
	// Switching shouldn't fail just because the email lookup did.
	var oldAccount workagentModel.AgentAccount
	oldAccountName := "Unknown"
	if oldAccountID != 0 {
		if err := globals.GraDBs["system"].Where("id = ?", oldAccountID).First(&oldAccount).Error; err == nil {
			oldAccountName = oldAccount.Name
		}
	}

	NotifyAccountSwitch(oldAccountID, oldAccountName, newAccountID, newAccount.Name, reason)
	return nil
}

// selectBestAvailableAccount returns the highest-priority account that
// (a) isn't the excluded id (typically the current active account
// during a failover), (b) is enabled, and (c) doesn't have an open
// circuit breaker. Returns ErrNoAccountAvailable when nothing fits.
func (p *AgentAccountPool) selectBestAvailableAccount(excludeAccountID uint64) (*workagentModel.AgentAccount, error) {
	var accounts []workagentModel.AgentAccount
	query := globals.GraDBs["system"].Where("status = ? AND deleted_at IS NULL", 1)
	if excludeAccountID != 0 {
		query = query.Where("id <> ?", excludeAccountID)
	}
	err := query.Order("priority DESC").Find(&accounts).Error
	if err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	for i := range accounts {
		account := &accounts[i]
		cb := p.circuitBreakers[account.ID]
		if cb != nil && !cb.CanAttempt() {
			continue
		}
		return account, nil
	}

	return nil, ErrNoAccountAvailable
}
