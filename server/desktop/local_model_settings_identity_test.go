//go:build desktop

package desktop

import (
	"context"
	"strings"
	"testing"

	cloudproxy "server/desktop/cloud_proxy"
)

// The bug this file exists for: the local-model API key lived under one fixed
// Keychain account for the whole machine. Switch local accounts, or connect a
// different WorkMax account, and LoadAPIKey handed the new identity the old
// one's credential — which the local engine would then present to whatever
// endpoint it was pointed at.

func TestLocalModelAPIKeyIsScopedToTheIdentityThatEnteredIt(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	store := NewLocalModelSettingsStore(db, kc)

	first := localSingleUserUID
	second := localSingleUserUID + 1

	firstKey := "sk-first-identity"
	if _, err := store.Put(first, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
			APIKey:   &firstKey,
		},
	}); err != nil {
		t.Fatalf("first put: %v", err)
	}

	// The second identity on the same machine has entered nothing.
	got, err := store.LoadAPIKey(second)
	if err != nil {
		t.Fatalf("second LoadAPIKey: %v", err)
	}
	if got != "" {
		t.Fatalf("second identity read the first identity's key: %q", got)
	}
	dto, err := store.Get(second)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if dto.Local.APIKeyConfigured {
		t.Fatal("second identity must not be told a key is configured")
	}
	// ...and the endpoint IS shared, because a server installed on this
	// machine is installed for everyone who uses it.
	if dto.Local.BaseURL != "http://127.0.0.1:11434/v1" || dto.Local.ModelID != "llama3.2" {
		t.Fatalf("machine endpoint should be shared, got %+v", dto.Local)
	}
	// The route preference is NOT shared: it is a choice, not an installation.
	if dto.PreferredRoute != ModelRouteOfficial {
		t.Fatalf("second identity inherited route %q", dto.PreferredRoute)
	}

	// The first identity still has exactly what it entered.
	if got, err := store.LoadAPIKey(first); err != nil || got != firstKey {
		t.Fatalf("first LoadAPIKey = %q, %v", got, err)
	}

	// Clearing one identity's key leaves the other alone.
	secondKey := "sk-second-identity"
	if _, err := store.Put(second, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
			APIKey:   &secondKey,
		},
	}); err != nil {
		t.Fatalf("second put: %v", err)
	}
	if _, err := store.Put(second, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol:    LocalProtocolOpenAICompatible,
			BaseURL:     "http://127.0.0.1:11434/v1",
			ModelID:     "llama3.2",
			ClearAPIKey: true,
		},
	}); err != nil {
		t.Fatalf("second clear: %v", err)
	}
	if got, err := store.LoadAPIKey(second); err != nil || got != "" {
		t.Fatalf("second key survived its own clear: %q %v", got, err)
	}
	if got, err := store.LoadAPIKey(first); err != nil || got != firstKey {
		t.Fatalf("clearing one identity's key disturbed another: %q %v", got, err)
	}
}

// The one existing key on an upgrading machine belongs to whoever configured
// that machine — the first local account — and it must survive the partition
// without the user noticing anything happened.
func TestLegacyLocalModelAPIKeyMigratesToTheFirstAccount(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	if err := kc.Write(cloudproxy.KeychainService, KeychainLocalModelAPIKeyLegacyAccount, []byte("sk-from-before")); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	store := NewLocalModelSettingsStore(db, kc)

	got, err := store.LoadAPIKey(localSingleUserUID)
	if err != nil {
		t.Fatalf("LoadAPIKey: %v", err)
	}
	if got != "sk-from-before" {
		t.Fatalf("migrated key = %q, want the pre-partition key", got)
	}
	// The shared slot is gone: leaving it would hand the next identity the
	// same credential the moment anything read it directly.
	if _, err := kc.Read(cloudproxy.KeychainService, KeychainLocalModelAPIKeyLegacyAccount); err == nil {
		t.Fatal("legacy fixed Keychain account must be removed after migration")
	}
	// And a second identity is unaffected by the migration.
	if got, err := store.LoadAPIKey(localSingleUserUID + 1); err != nil || got != "" {
		t.Fatalf("migration leaked into another identity: %q %v", got, err)
	}
}

// Idempotence and non-fatality: a key already entered under the partitioned
// account is newer truth than the legacy slot, and must not be overwritten.
func TestLegacyKeychainMigrationNeverOverwritesANewerKey(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	if err := kc.Write(cloudproxy.KeychainService, KeychainLocalModelAPIKeyLegacyAccount, []byte("sk-old")); err != nil {
		t.Fatal(err)
	}
	if err := kc.Write(cloudproxy.KeychainService, keychainLocalModelAPIKeyAccount(localSingleUserUID), []byte("sk-new")); err != nil {
		t.Fatal(err)
	}
	store := NewLocalModelSettingsStore(db, kc)
	if got, err := store.LoadAPIKey(localSingleUserUID); err != nil || got != "sk-new" {
		t.Fatalf("LoadAPIKey = %q, %v; the re-entered key must win", got, err)
	}
	if _, err := kc.Read(cloudproxy.KeychainService, KeychainLocalModelAPIKeyLegacyAccount); err == nil {
		t.Fatal("the superseded legacy slot must still be removed")
	}
}

// A Keychain that refuses to answer must not take the settings surface down
// with it: the worst acceptable outcome is re-entering a key.
func TestLegacyKeychainMigrationFailureIsNotFatal(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := &failingKeychain{memKeychain: newMemKeychain()}
	if err := kc.memKeychain.Write(cloudproxy.KeychainService, KeychainLocalModelAPIKeyLegacyAccount, []byte("sk-old")); err != nil {
		t.Fatal(err)
	}
	kc.failWrites = true
	store := NewLocalModelSettingsStore(db, kc)

	dto, err := store.Get(localSingleUserUID)
	if err != nil {
		t.Fatalf("Get must survive a failed migration: %v", err)
	}
	if dto.PreferredRoute != ModelRouteOfficial {
		t.Fatalf("route = %q", dto.PreferredRoute)
	}
	// The legacy key is still there rather than destroyed: a migration that
	// could not write must not delete the only copy.
	if _, err := kc.memKeychain.Read(cloudproxy.KeychainService, KeychainLocalModelAPIKeyLegacyAccount); err != nil {
		t.Fatalf("a failed migration must not delete the legacy key: %v", err)
	}
}

type failingKeychain struct {
	*memKeychain
	failWrites bool
}

func (f *failingKeychain) Write(service, account string, value []byte) error {
	if f.failWrites {
		return errKeychainUnavailableForTest
	}
	return f.memKeychain.Write(service, account, value)
}

var errKeychainUnavailableForTest = errTestKeychain("keychain is locked")

type errTestKeychain string

func (e errTestKeychain) Error() string { return string(e) }

// Migration 0009's zero-migration promise: the machine that already had a
// preference keeps it, under the identity that made it.
func TestMigration0009HandsTheExistingPreferenceToTheFirstAccount(t *testing.T) {
	db := openMigratedTestDB(t)

	var (
		uid   uint64
		route string
	)
	if err := db.Raw(
		`SELECT uid, preferred_route FROM w_desktop_model_preference`,
	).Row().Scan(&uid, &route); err != nil {
		t.Fatalf("0009 must seed one preference row from the old global row: %v", err)
	}
	if uid != localSingleUserUID {
		t.Fatalf("seeded uid = %d, want the reserved single-user uid %d", uid, localSingleUserUID)
	}
	if route != ModelRouteOfficial {
		t.Fatalf("seeded route = %q, want the pre-partition default", route)
	}

	// The columns that moved are gone from the machine table, so nothing can
	// read a stale global answer.
	for _, column := range []string{"preferred_route", "local_api_key_present"} {
		var count int
		if err := db.Raw(
			`SELECT COUNT(*) FROM pragma_table_info('w_desktop_model_settings') WHERE name = ?`,
			column,
		).Row().Scan(&count); err != nil {
			t.Fatalf("pragma_table_info: %v", err)
		}
		if count != 0 {
			t.Fatalf("w_desktop_model_settings still carries the moved column %q", column)
		}
	}
}

// The account is derived from the uid, and uids are per-data-dir counters that
// every isolated data dir restarts from the same number — so isolation of the
// data dir buys nothing here. Only the SERVICE name separates a throwaway run
// from the user's real key, which is why WORKMAX_KEYCHAIN_SERVICE exists.
//
// This became load-bearing in 1855d91: before it, Write stored the empty string
// and the collision cost nothing. Now a smoke run without the override
// overwrites the user's configured key.
func TestLocalModelAPIKeyFollowsTheKeychainServiceOverride(t *testing.T) {
	const isolated = "ai.workmax.desktop.test-isolated"
	t.Setenv(cloudproxy.KeychainServiceEnv, isolated)

	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	store := NewLocalModelSettingsStore(db, kc)

	key := "sk-isolated-run"
	if _, err := store.Put(localSingleUserUID, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
			APIKey:   &key,
		},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	account := keychainLocalModelAPIKeyAccount(localSingleUserUID)
	if _, err := kc.Read(isolated, account); err != nil {
		t.Fatalf("the key must land under the isolated service: %v", err)
	}
	// The point of the whole exercise: the real slot is untouched.
	if _, err := kc.Read(cloudproxy.KeychainService, account); err == nil {
		t.Fatalf("an isolated run wrote into the real service %q", cloudproxy.KeychainService)
	}
	// And reads follow it back, so the run is functional, not merely harmless.
	got, err := store.LoadAPIKey(localSingleUserUID)
	if err != nil || got != key {
		t.Fatalf("LoadAPIKey = %q, %v; want the key it just stored", got, err)
	}
}

// The default is what every user runs, and it must be byte-identical to what
// shipped before the override existed.
func TestLocalModelAPIKeyKeepsTheDefaultServiceWithoutAnOverride(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	store := NewLocalModelSettingsStore(db, kc)

	key := "sk-default-service"
	if _, err := store.Put(localSingleUserUID, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
			APIKey:   &key,
		},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := kc.Read(cloudproxy.KeychainService, keychainLocalModelAPIKeyAccount(localSingleUserUID)); err != nil {
		t.Fatalf("without an override the key must stay in the shipping slot: %v", err)
	}
}

// A malformed override is not a warning. Bootstrap refuses before it resolves a
// data dir, takes the sidecar lock or opens SQLite, because the alternative is
// a run that believes it is isolated and is not.
func TestBootstrapRefusesAMalformedKeychainServiceOverride(t *testing.T) {
	t.Setenv(cloudproxy.KeychainServiceEnv, "not a service name")
	boot, err := Bootstrap(BootstrapConfig{})
	if err == nil {
		_ = boot.Shutdown(context.Background())
		t.Fatal("Bootstrap started with a malformed keychain service override")
	}
	if !strings.Contains(err.Error(), cloudproxy.KeychainServiceEnv) {
		t.Fatalf("the failure must name the variable to fix: %v", err)
	}
}
