//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// Model route constants for OSS Desktop dual-path inference.
const (
	ModelRouteOfficial = "official"
	ModelRouteLocal    = "local"

	LocalProtocolOpenAICompatible    = "openai_compatible"
	LocalProtocolAnthropicCompatible = "anthropic_compatible"

	// keychainLocalModelAPIKeyPrefix names the per-identity Keychain slot
	// holding a user's local-model API key. Service remains
	// cloudproxy.KeychainServiceName() so Keychain Access shows one app —
	// which is the default name unless the process asked for an isolated
	// namespace (WORKMAX_KEYCHAIN_SERVICE; see cloud_proxy/keychain.go).
	//
	// The suffix is the uid, and it is not cosmetic. This used to be one
	// fixed account for the whole machine, which meant switching local
	// accounts — or connecting a different WorkMax account — silently handed
	// the new identity the previous one's key, and the local engine would go
	// on authenticating to somebody else's endpoint with it. A credential
	// that outlives the identity it was entered for is a leak whether or not
	// anybody notices.
	keychainLocalModelAPIKeyPrefix = "local-model-api-key"

	// KeychainLocalModelAPIKeyLegacyAccount is the pre-partition fixed
	// account. It is read exactly once per process, migrated onto the first
	// local account's uid, and deleted. Kept exported so tests and support
	// tooling can name the thing being retired.
	KeychainLocalModelAPIKeyLegacyAccount = keychainLocalModelAPIKeyPrefix

	maxLocalBaseURLBytes    = 512
	maxLocalModelIDBytes    = 128
	maxLocalAPIKeyBytes     = 4096
	maxOfficialModelIDBytes = 128
)

// keychainLocalModelAPIKeyAccount is the Keychain account for one identity.
func keychainLocalModelAPIKeyAccount(uid uint64) string {
	return keychainLocalModelAPIKeyPrefix + ":" + strconv.FormatUint(uid, 10)
}

// LocalModelSettingsDTO is the renderer-safe wire shape. It never includes api_key.
type LocalModelSettingsDTO struct {
	PreferredRoute string `json:"preferred_route"`
	// OfficialModelID is the cloud catalog modelId this identity picked for
	// official turns (""=never chose one, use the account default). It is a
	// per-identity preference precisely because the catalog is per-account:
	// entitlement follows the membership, not the machine.
	OfficialModelID string               `json:"official_model_id"`
	Local           LocalModelProfileDTO `json:"local"`
	UpdatedAt       string               `json:"updated_at"`
}

// LocalModelProfileDTO is non-secret local profile fields.
//
// protocol/base_url/model_id describe a server installed on THIS MACHINE and
// are deliberately shared by every identity on it: somebody set up Ollama
// here, it answers on this port, and making each account retype the same
// loopback URL would be bookkeeping rather than separation. The API key is
// the opposite — it is a credential, so api_key_configured reports this
// identity's Keychain slot and nobody else's.
type LocalModelProfileDTO struct {
	Protocol         string `json:"protocol"`
	BaseURL          string `json:"base_url"`
	ModelID          string `json:"model_id"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

// LocalModelSettingsPut is the PUT body. api_key is write-only.
type LocalModelSettingsPut struct {
	PreferredRoute string `json:"preferred_route"`
	// OfficialModelID is optional: a nil pointer leaves the stored choice
	// alone, so a renderer saving only the route cannot silently clear it.
	OfficialModelID *string               `json:"official_model_id"`
	Local           *LocalModelProfilePut `json:"local"`
}

// LocalModelProfilePut carries optional secret write controls.
type LocalModelProfilePut struct {
	Protocol    string  `json:"protocol"`
	BaseURL     string  `json:"base_url"`
	ModelID     string  `json:"model_id"`
	APIKey      *string `json:"api_key"`
	ClearAPIKey bool    `json:"clear_api_key"`
}

// machineLocalProfile is the machine-wide local endpoint (migration 0009
// left w_desktop_model_settings holding exactly these columns).
type machineLocalProfile struct {
	Protocol  string
	BaseURL   string
	ModelID   string
	UpdatedAt string
}

// identityModelPreference is one identity's row in w_desktop_model_preference.
type identityModelPreference struct {
	PreferredRoute  string
	OfficialModelID string
	APIKeyPresent   bool
	UpdatedAt       string
}

// LocalModelSettingsStore persists the machine's local endpoint and each
// identity's route/model preference in SQLite, and each identity's API key in
// Keychain.
type LocalModelSettingsStore struct {
	db       *gorm.DB
	keychain cloudproxy.Keychain

	legacyKeyOnce sync.Once
}

// NewLocalModelSettingsStore wires SQLite + Keychain. keychain may be nil only
// in tests that never touch the key; production always supplies one.
func NewLocalModelSettingsStore(db *gorm.DB, keychain cloudproxy.Keychain) *LocalModelSettingsStore {
	return &LocalModelSettingsStore{db: db, keychain: keychain}
}

// Get returns the public DTO for one identity, reconciling api_key_configured
// with that identity's Keychain slot.
func (s *LocalModelSettingsStore) Get(uid uint64) (LocalModelSettingsDTO, error) {
	if s == nil || s.db == nil {
		return LocalModelSettingsDTO{}, errors.New("local model settings store unavailable")
	}
	s.migrateLegacyKeychainAccount()
	profile, err := s.loadOrSeedMachineProfile()
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	pref, err := s.loadOrSeedPreference(uid)
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	configured, err := s.keyPresent(uid)
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	if configured != pref.APIKeyPresent {
		// Mirror bit is advisory; Keychain is truth. Heal quietly.
		_ = s.setPresentFlag(uid, configured)
		pref.APIKeyPresent = configured
	}
	return dtoFrom(profile, pref, configured), nil
}

// Put validates and stores settings for one identity. Secret bytes never land
// in SQLite, and the local endpoint written here is the machine's.
func (s *LocalModelSettingsStore) Put(uid uint64, in LocalModelSettingsPut) (LocalModelSettingsDTO, error) {
	if s == nil || s.db == nil {
		return LocalModelSettingsDTO{}, errors.New("local model settings store unavailable")
	}
	s.migrateLegacyKeychainAccount()
	route := strings.TrimSpace(in.PreferredRoute)
	if route != ModelRouteLocal && route != ModelRouteOfficial {
		return LocalModelSettingsDTO{}, fmt.Errorf("preferred_route must be %q or %q", ModelRouteLocal, ModelRouteOfficial)
	}

	profile, err := s.loadOrSeedMachineProfile()
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	pref, err := s.loadOrSeedPreference(uid)
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}

	var (
		protocol string
		baseURL  string
		modelID  string
	)
	if in.Local != nil {
		protocol = strings.TrimSpace(in.Local.Protocol)
		baseURL = strings.TrimSpace(in.Local.BaseURL)
		modelID = strings.TrimSpace(in.Local.ModelID)
	} else {
		// Load existing non-secret fields when local block omitted.
		protocol = profile.Protocol
		baseURL = profile.BaseURL
		modelID = profile.ModelID
	}

	switch {
	case route == ModelRouteLocal && baseURL == "" && modelID == "":
		// Run the turn on this machine, on an official model. There is no
		// endpoint to validate because the endpoint is the sidecar's own
		// loopback gateway — but the protocol is still required, because it
		// decides which local engine runs (the claude tool loop or pi) and
		// therefore which wire shape the gateway is asked to speak.
		//
		// This is what makes "local execution, official model" expressible
		// without a third preference: an empty local endpoint on the local
		// route means the user did not bring their own, so the official model
		// they picked from the catalog is what runs.
		if err := validateLocalProtocol(protocol); err != nil {
			return LocalModelSettingsDTO{}, err
		}
	case route == ModelRouteLocal || protocol != "" || baseURL != "" || modelID != "":
		if err := validateLocalProfile(protocol, baseURL, modelID); err != nil {
			return LocalModelSettingsDTO{}, err
		}
	default:
		// Official route with empty local profile is allowed.
		protocol, baseURL, modelID = "", "", ""
	}

	officialModelID := pref.OfficialModelID
	if in.OfficialModelID != nil {
		officialModelID = strings.TrimSpace(*in.OfficialModelID)
		if err := validateOfficialModelID(officialModelID); err != nil {
			return LocalModelSettingsDTO{}, err
		}
	}

	// Keychain mutations first so a failed Keychain write does not leave SQLite
	// claiming a key that is missing.
	if in.Local != nil {
		account := keychainLocalModelAPIKeyAccount(uid)
		if in.Local.ClearAPIKey {
			if s.keychain == nil {
				return LocalModelSettingsDTO{}, errors.New("keychain unavailable")
			}
			if err := s.keychain.Delete(cloudproxy.KeychainServiceName(), account); err != nil {
				return LocalModelSettingsDTO{}, fmt.Errorf("clear local model api key: %w", err)
			}
		}
		if in.Local.APIKey != nil && !in.Local.ClearAPIKey {
			key := *in.Local.APIKey
			if err := validateAPIKey(key); err != nil {
				return LocalModelSettingsDTO{}, err
			}
			if s.keychain == nil {
				return LocalModelSettingsDTO{}, errors.New("keychain unavailable")
			}
			if err := s.keychain.Write(cloudproxy.KeychainServiceName(), account, []byte(key)); err != nil {
				return LocalModelSettingsDTO{}, fmt.Errorf("store local model api key: %w", err)
			}
		}
	}

	configured, err := s.keyPresent(uid)
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	present := 0
	if configured {
		present = 1
	}
	// The machine's endpoint is only rewritten when the caller actually sent
	// one. A user saving "official" from a second account must not blank the
	// Ollama address the first account configured.
	if in.Local != nil {
		if err := s.db.Exec(`
			INSERT INTO w_desktop_model_settings (id, local_protocol, local_base_url, local_model_id, updated_at)
			VALUES (1, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				local_protocol = excluded.local_protocol,
				local_base_url = excluded.local_base_url,
				local_model_id = excluded.local_model_id,
				updated_at     = excluded.updated_at`,
			protocol, baseURL, modelID, now,
		).Error; err != nil {
			return LocalModelSettingsDTO{}, fmt.Errorf("persist local endpoint: %w", err)
		}
	}
	if err := s.db.Exec(`
		INSERT INTO w_desktop_model_preference (uid, preferred_route, official_model_id, local_api_key_present, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(uid) DO UPDATE SET
			preferred_route       = excluded.preferred_route,
			official_model_id     = excluded.official_model_id,
			local_api_key_present = excluded.local_api_key_present,
			updated_at            = excluded.updated_at`,
		uid, route, officialModelID, present, now,
	).Error; err != nil {
		return LocalModelSettingsDTO{}, fmt.Errorf("persist model settings: %w", err)
	}

	return dtoFrom(
		machineLocalProfile{Protocol: protocol, BaseURL: baseURL, ModelID: modelID, UpdatedAt: now},
		identityModelPreference{
			PreferredRoute:  route,
			OfficialModelID: officialModelID,
			APIKeyPresent:   configured,
			UpdatedAt:       now,
		},
		configured,
	), nil
}

// LoadAPIKey returns one identity's raw local API key for Sidecar-internal use
// only. Callers must not log or return it to Renderer.
func (s *LocalModelSettingsStore) LoadAPIKey(uid uint64) (string, error) {
	if s == nil || s.keychain == nil {
		return "", errors.New("keychain unavailable")
	}
	s.migrateLegacyKeychainAccount()
	raw, err := s.keychain.Read(cloudproxy.KeychainServiceName(), keychainLocalModelAPIKeyAccount(uid))
	if err != nil {
		if errors.Is(err, cloudproxy.ErrKeychainNoEntry) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// migrateLegacyKeychainAccount moves the one pre-partition key onto the first
// local account's uid, then deletes the shared slot.
//
// It runs at most once per process, from whichever read or write happens
// first, and it is deliberately best-effort: the worst outcome of a failure is
// that the user re-enters an API key. Refusing to boot — or refusing to answer
// a settings read — because a Keychain prompt was dismissed would be a far
// worse trade.
//
// The destination is the FIRST local account, the same zero-migration trick
// migration 0005 and 0009 use: everything created before identities existed
// belongs to the machine's original user, whose uid is (1<<62).
func (s *LocalModelSettingsStore) migrateLegacyKeychainAccount() {
	s.legacyKeyOnce.Do(func() {
		if s.keychain == nil {
			return
		}
		raw, err := s.keychain.Read(cloudproxy.KeychainServiceName(), KeychainLocalModelAPIKeyLegacyAccount)
		if err != nil {
			if !errors.Is(err, cloudproxy.ErrKeychainNoEntry) {
				log.Printf("local model api key: legacy account unreadable, leaving it in place: %v", err)
			}
			return
		}
		defer clear(raw)
		uid := firstLocalAccountUID(s.db)
		account := keychainLocalModelAPIKeyAccount(uid)
		// Never overwrite a key the partitioned world already wrote: a
		// re-entered key is the newer truth.
		if _, err := s.keychain.Read(cloudproxy.KeychainServiceName(), account); err != nil {
			if !errors.Is(err, cloudproxy.ErrKeychainNoEntry) {
				log.Printf("local model api key: migration target unreadable, leaving legacy key in place: %v", err)
				return
			}
			if err := s.keychain.Write(cloudproxy.KeychainServiceName(), account, raw); err != nil {
				log.Printf("local model api key: migration write failed, leaving legacy key in place: %v", err)
				return
			}
		}
		if err := s.keychain.Delete(cloudproxy.KeychainServiceName(), KeychainLocalModelAPIKeyLegacyAccount); err != nil {
			log.Printf("local model api key: legacy account delete failed: %v", err)
		}
	})
}

// firstLocalAccountUID is the uid that owns everything created before
// identities were separated. Any bookkeeping failure falls back to the
// reserved single-user uid, which is the same answer on every machine that
// has not deleted its first account.
func firstLocalAccountUID(db *gorm.DB) uint64 {
	if db == nil {
		return localSingleUserUID
	}
	var id int64
	if err := db.Raw(`SELECT MIN(id) FROM w_desktop_local_account`).Row().Scan(&id); err != nil || id <= 0 {
		return localSingleUserUID
	}
	return localAccountUID(id)
}

func (s *LocalModelSettingsStore) loadOrSeedMachineProfile() (machineLocalProfile, error) {
	var profile machineLocalProfile
	row := s.db.Raw(
		`SELECT local_protocol, local_base_url, local_model_id, updated_at
		   FROM w_desktop_model_settings WHERE id = 1`,
	).Row()
	err := row.Scan(&profile.Protocol, &profile.BaseURL, &profile.ModelID, &profile.UpdatedAt)
	if err == nil {
		return profile, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Exec(
		`INSERT OR IGNORE INTO w_desktop_model_settings (id, local_protocol, local_base_url, local_model_id, updated_at)
		 VALUES (1, '', '', '', ?)`, now,
	).Error; err != nil {
		return machineLocalProfile{}, fmt.Errorf("seed local endpoint: %w", err)
	}
	return machineLocalProfile{UpdatedAt: now}, nil
}

func (s *LocalModelSettingsStore) loadOrSeedPreference(uid uint64) (identityModelPreference, error) {
	var (
		pref    identityModelPreference
		present int
	)
	row := s.db.Raw(
		`SELECT preferred_route, official_model_id, local_api_key_present, updated_at
		   FROM w_desktop_model_preference WHERE uid = ?`, uid,
	).Row()
	if err := row.Scan(&pref.PreferredRoute, &pref.OfficialModelID, &present, &pref.UpdatedAt); err == nil {
		pref.APIKeyPresent = present != 0
		return pref, nil
	}
	// A brand-new identity starts on the official route with nothing chosen.
	// Seeding rather than returning a zero value keeps Get/Put symmetric with
	// the pre-partition behavior, where the row always existed.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Exec(
		`INSERT OR IGNORE INTO w_desktop_model_preference
		   (uid, preferred_route, official_model_id, local_api_key_present, updated_at)
		 VALUES (?, ?, '', 0, ?)`, uid, ModelRouteOfficial, now,
	).Error; err != nil {
		return identityModelPreference{}, fmt.Errorf("seed model preference: %w", err)
	}
	return identityModelPreference{PreferredRoute: ModelRouteOfficial, UpdatedAt: now}, nil
}

func (s *LocalModelSettingsStore) keyPresent(uid uint64) (bool, error) {
	if s.keychain == nil {
		return false, nil
	}
	_, err := s.keychain.Read(cloudproxy.KeychainServiceName(), keychainLocalModelAPIKeyAccount(uid))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, cloudproxy.ErrKeychainNoEntry) {
		return false, nil
	}
	return false, err
}

func (s *LocalModelSettingsStore) setPresentFlag(uid uint64, present bool) error {
	flag := 0
	if present {
		flag = 1
	}
	return s.db.Exec(
		`UPDATE w_desktop_model_preference SET local_api_key_present = ? WHERE uid = ?`,
		flag, uid,
	).Error
}

func dtoFrom(profile machineLocalProfile, pref identityModelPreference, keyConfigured bool) LocalModelSettingsDTO {
	updatedAt := pref.UpdatedAt
	if profile.UpdatedAt > updatedAt {
		updatedAt = profile.UpdatedAt
	}
	return LocalModelSettingsDTO{
		PreferredRoute:  pref.PreferredRoute,
		OfficialModelID: pref.OfficialModelID,
		Local: LocalModelProfileDTO{
			Protocol:         profile.Protocol,
			BaseURL:          profile.BaseURL,
			ModelID:          profile.ModelID,
			APIKeyConfigured: keyConfigured,
		},
		UpdatedAt: updatedAt,
	}
}

func validateLocalProtocol(protocol string) error {
	switch protocol {
	case LocalProtocolOpenAICompatible, LocalProtocolAnthropicCompatible:
		return nil
	default:
		return fmt.Errorf("local.protocol must be %q or %q", LocalProtocolOpenAICompatible, LocalProtocolAnthropicCompatible)
	}
}

func validateLocalProfile(protocol, baseURL, modelID string) error {
	if err := validateLocalProtocol(protocol); err != nil {
		return err
	}
	if err := validateLocalBaseURL(baseURL); err != nil {
		return err
	}
	if err := validateModelID(modelID); err != nil {
		return err
	}
	return nil
}

func validateLocalBaseURL(raw string) error {
	if raw == "" {
		return errors.New("local.base_url is required")
	}
	if len(raw) > maxLocalBaseURLBytes {
		return fmt.Errorf("local.base_url exceeds %d bytes", maxLocalBaseURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("local.base_url invalid: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("local.base_url scheme must be https or http")
	}
	if u.User != nil {
		return errors.New("local.base_url must not embed credentials")
	}
	if u.Fragment != "" {
		return errors.New("local.base_url must not include a fragment")
	}
	if u.Host == "" {
		return errors.New("local.base_url host is required")
	}
	host := u.Hostname()
	if u.Scheme == "http" {
		if !isLoopbackHost(host) {
			return errors.New("http local.base_url is only allowed for loopback hosts")
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func validateModelID(id string) error {
	if id == "" {
		return errors.New("local.model_id is required")
	}
	if len(id) > maxLocalModelIDBytes {
		return fmt.Errorf("local.model_id exceeds %d bytes", maxLocalModelIDBytes)
	}
	if !isModelIDCharset(id) {
		return errors.New("local.model_id contains unsupported characters")
	}
	return nil
}

// validateOfficialModelID accepts "" (nothing chosen) and otherwise the same
// conservative charset as a local model id. The sidecar does NOT check the id
// against the cloud catalog here: entitlement is the cloud's answer and it
// changes without a write happening on this machine, so a store-time check
// would only be able to produce a stale verdict.
func validateOfficialModelID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > maxOfficialModelIDBytes {
		return fmt.Errorf("official_model_id exceeds %d bytes", maxOfficialModelIDBytes)
	}
	if !isModelIDCharset(id) {
		return errors.New("official_model_id contains unsupported characters")
	}
	return nil
}

func isModelIDCharset(id string) bool {
	for _, r := range id {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-:/", r)) {
			return false
		}
	}
	return true
}

func validateAPIKey(key string) error {
	if key == "" {
		return errors.New("local.api_key must not be empty when provided")
	}
	if len(key) > maxLocalAPIKeyBytes {
		return fmt.Errorf("local.api_key exceeds %d bytes", maxLocalAPIKeyBytes)
	}
	if !utf8.ValidString(key) {
		return errors.New("local.api_key must be valid UTF-8")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return errors.New("local.api_key must not contain control characters")
		}
	}
	return nil
}

// DecodeLocalModelSettingsPut strictly decodes JSON without unknown fields.
func DecodeLocalModelSettingsPut(raw []byte) (LocalModelSettingsPut, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in LocalModelSettingsPut
	if err := dec.Decode(&in); err != nil {
		return LocalModelSettingsPut{}, err
	}
	if dec.More() {
		return LocalModelSettingsPut{}, errors.New("trailing json content")
	}
	return in, nil
}
