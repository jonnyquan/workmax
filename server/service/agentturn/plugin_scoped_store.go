package agentturn

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sort"

	agentv1 "server/contracts/agent/v1"
)

// PluginScopedExecutionStore is the production-safe execution view over one
// SQLStore. Its non-empty immutable scope is injected into every claim path;
// callers cannot accidentally widen it by omitting or replacing command data.
// Reconciliation and Effect dispatch deliberately continue to use the base
// SQLStore because they own different queue domains.
type PluginScopedExecutionStore struct {
	base   *SQLStore
	scope  []agentv1.EventPluginRef
	digest [sha256.Size]byte
}

// NewPluginScopedExecutionStore binds one SQLStore to an exact, non-empty set
// of immutable Plugin releases. The constructor sorts and copies the scope so
// caller mutation cannot change the worker's authority.
func NewPluginScopedExecutionStore(base *SQLStore, scope []agentv1.EventPluginRef) (*PluginScopedExecutionStore, error) {
	if base == nil || len(scope) == 0 {
		return nil, ErrPluginScopeMismatch
	}
	copyOfScope := append([]agentv1.EventPluginRef(nil), scope...)
	if err := validateClaimPluginScope(copyOfScope); err != nil {
		return nil, err
	}
	sort.Slice(copyOfScope, func(left, right int) bool {
		return pluginScopeKey(copyOfScope[left]) < pluginScopeKey(copyOfScope[right])
	})
	return &PluginScopedExecutionStore{
		base:   base,
		scope:  copyOfScope,
		digest: pluginScopeDigest(copyOfScope),
	}, nil
}

func (store *PluginScopedExecutionStore) intact() bool {
	return store != nil && store.base != nil && len(store.scope) > 0 &&
		validateClaimPluginScope(store.scope) == nil &&
		store.digest == pluginScopeDigest(store.scope)
}

// Matches proves that this view is bound to the expected base store and exact
// release set. It is used by the composition root before installing runtime
// components; no mutable internal slice is exposed.
func (store *PluginScopedExecutionStore) Matches(base *SQLStore, scope []agentv1.EventPluginRef) bool {
	if !store.intact() || base == nil || store.base != base || len(scope) == 0 {
		return false
	}
	copyOfScope := append([]agentv1.EventPluginRef(nil), scope...)
	if err := validateClaimPluginScope(copyOfScope); err != nil {
		return false
	}
	sort.Slice(copyOfScope, func(left, right int) bool {
		return pluginScopeKey(copyOfScope[left]) < pluginScopeKey(copyOfScope[right])
	})
	return store.digest == pluginScopeDigest(copyOfScope)
}

func (store *PluginScopedExecutionStore) ClaimNext(
	ctx context.Context,
	command ClaimNextCommand,
) (ClaimAttemptResult, error) {
	if !store.intact() {
		return ClaimAttemptResult{}, ErrPluginScopeMismatch
	}
	command.PluginScope = append([]agentv1.EventPluginRef(nil), store.scope...)
	return store.base.ClaimNext(ctx, command)
}

func (store *PluginScopedExecutionStore) ClaimAttempt(
	ctx context.Context,
	command ClaimAttemptCommand,
) (ClaimAttemptResult, error) {
	if !store.intact() {
		return ClaimAttemptResult{}, ErrPluginScopeMismatch
	}
	command.PluginScope = append([]agentv1.EventPluginRef(nil), store.scope...)
	return store.base.ClaimAttempt(ctx, command)
}

func (store *PluginScopedExecutionStore) HeartbeatAttempt(
	ctx context.Context,
	command HeartbeatAttemptCommand,
) (HeartbeatAttemptResult, error) {
	if !store.intact() {
		return HeartbeatAttemptResult{}, ErrPluginScopeMismatch
	}
	return store.base.HeartbeatAttempt(ctx, command)
}

func (store *PluginScopedExecutionStore) CommitAttempt(
	ctx context.Context,
	command CommitAttemptCommand,
) (CommitAttemptResult, error) {
	if !store.intact() {
		return CommitAttemptResult{}, ErrPluginScopeMismatch
	}
	return store.base.CommitAttempt(ctx, command)
}

func pluginScopeKey(plugin agentv1.EventPluginRef) string {
	return plugin.ID + "\x00" + plugin.Version + "\x00" + plugin.ReleaseDigest
}

func pluginScopeDigest(scope []agentv1.EventPluginRef) [sha256.Size]byte {
	payload := make([]byte, 0, len(scope)*96)
	for _, plugin := range scope {
		for _, field := range []string{plugin.ID, plugin.Version, plugin.ReleaseDigest} {
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(field)))
			payload = append(payload, size[:]...)
			payload = append(payload, field...)
		}
	}
	digest := sha256.Sum256(payload)
	clear(payload)
	return digest
}

var _ ExecutionStore = (*PluginScopedExecutionStore)(nil)
