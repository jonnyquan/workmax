//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	cloudproxy "server/desktop/cloud_proxy"
)

// Model route constants for OSS Desktop dual-path inference.
const (
	ModelRouteOfficial = "official"
	ModelRouteLocal    = "local"

	LocalProtocolOpenAICompatible    = "openai_compatible"
	LocalProtocolAnthropicCompatible = "anthropic_compatible"

	// KeychainLocalModelAPIKeyAccount stores the user local-model API key.
	// Service remains cloudproxy.KeychainService so Keychain Access shows one app.
	KeychainLocalModelAPIKeyAccount = "local-model-api-key"

	maxLocalBaseURLBytes = 512
	maxLocalModelIDBytes = 128
	maxLocalAPIKeyBytes  = 4096
)

// LocalModelSettingsDTO is the renderer-safe wire shape. It never includes api_key.
type LocalModelSettingsDTO struct {
	PreferredRoute string             `json:"preferred_route"`
	Local          LocalModelProfileDTO `json:"local"`
	UpdatedAt      string             `json:"updated_at"`
}

// LocalModelProfileDTO is non-secret local profile fields.
type LocalModelProfileDTO struct {
	Protocol         string `json:"protocol"`
	BaseURL          string `json:"base_url"`
	ModelID          string `json:"model_id"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

// LocalModelSettingsPut is the PUT body. api_key is write-only.
type LocalModelSettingsPut struct {
	PreferredRoute string                  `json:"preferred_route"`
	Local          *LocalModelProfilePut   `json:"local"`
}

// LocalModelProfilePut carries optional secret write controls.
type LocalModelProfilePut struct {
	Protocol    string  `json:"protocol"`
	BaseURL     string  `json:"base_url"`
	ModelID     string  `json:"model_id"`
	APIKey      *string `json:"api_key"`
	ClearAPIKey bool    `json:"clear_api_key"`
}

type modelSettingsRow struct {
	ID                 int    `gorm:"column:id;primaryKey"`
	PreferredRoute     string `gorm:"column:preferred_route"`
	LocalProtocol      string `gorm:"column:local_protocol"`
	LocalBaseURL       string `gorm:"column:local_base_url"`
	LocalModelID       string `gorm:"column:local_model_id"`
	LocalAPIKeyPresent int    `gorm:"column:local_api_key_present"`
	UpdatedAt          string `gorm:"column:updated_at"`
}

func (modelSettingsRow) TableName() string { return "w_desktop_model_settings" }

// LocalModelSettingsStore persists route preference + non-secret local profile
// in SQLite and the API key in Keychain.
type LocalModelSettingsStore struct {
	db       *gorm.DB
	keychain cloudproxy.Keychain
}

// NewLocalModelSettingsStore wires SQLite + Keychain. keychain may be nil only
// in tests that never touch the key; production always supplies one.
func NewLocalModelSettingsStore(db *gorm.DB, keychain cloudproxy.Keychain) *LocalModelSettingsStore {
	return &LocalModelSettingsStore{db: db, keychain: keychain}
}

// Get returns the public DTO, reconciling api_key_configured with Keychain.
func (s *LocalModelSettingsStore) Get() (LocalModelSettingsDTO, error) {
	if s == nil || s.db == nil {
		return LocalModelSettingsDTO{}, errors.New("local model settings store unavailable")
	}
	row, err := s.loadOrSeedRow()
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	configured, err := s.keyPresent()
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	if configured != (row.LocalAPIKeyPresent != 0) {
		// Mirror bit is advisory; Keychain is truth. Heal quietly.
		_ = s.setPresentFlag(configured)
		row.LocalAPIKeyPresent = 0
		if configured {
			row.LocalAPIKeyPresent = 1
		}
	}
	return dtoFromRow(row, configured), nil
}

// Put validates and stores settings. Secret bytes never land in SQLite.
func (s *LocalModelSettingsStore) Put(in LocalModelSettingsPut) (LocalModelSettingsDTO, error) {
	if s == nil || s.db == nil {
		return LocalModelSettingsDTO{}, errors.New("local model settings store unavailable")
	}
	route := strings.TrimSpace(in.PreferredRoute)
	if route != ModelRouteLocal && route != ModelRouteOfficial {
		return LocalModelSettingsDTO{}, fmt.Errorf("preferred_route must be %q or %q", ModelRouteLocal, ModelRouteOfficial)
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
	}

	// Load existing non-secret fields when local block omitted.
	row, err := s.loadOrSeedRow()
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	if in.Local == nil {
		protocol = row.LocalProtocol
		baseURL = row.LocalBaseURL
		modelID = row.LocalModelID
	}

	if route == ModelRouteLocal || (protocol != "" || baseURL != "" || modelID != "") {
		if err := validateLocalProfile(protocol, baseURL, modelID); err != nil {
			return LocalModelSettingsDTO{}, err
		}
	} else {
		// Official with empty local profile is allowed.
		protocol, baseURL, modelID = "", "", ""
	}

	if route == ModelRouteLocal {
		// Ensure a key will exist after this write (existing or new).
		willHaveKey, err := s.keyPresent()
		if err != nil {
			return LocalModelSettingsDTO{}, err
		}
		if in.Local != nil {
			if in.Local.ClearAPIKey {
				willHaveKey = false
			}
			if in.Local.APIKey != nil {
				if strings.TrimSpace(*in.Local.APIKey) != "" {
					willHaveKey = true
				}
			}
		}
		if !willHaveKey {
			// Local route may still work for keyless local servers (Ollama).
			// Allow configured profile without key.
		}
	}

	// Keychain mutations first so a failed Keychain write does not leave SQLite
	// claiming a key that is missing.
	if in.Local != nil {
		if in.Local.ClearAPIKey {
			if s.keychain == nil {
				return LocalModelSettingsDTO{}, errors.New("keychain unavailable")
			}
			if err := s.keychain.Delete(cloudproxy.KeychainService, KeychainLocalModelAPIKeyAccount); err != nil {
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
			if err := s.keychain.Write(cloudproxy.KeychainService, KeychainLocalModelAPIKeyAccount, []byte(key)); err != nil {
				return LocalModelSettingsDTO{}, fmt.Errorf("store local model api key: %w", err)
			}
		}
	}

	configured, err := s.keyPresent()
	if err != nil {
		return LocalModelSettingsDTO{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	present := 0
	if configured {
		present = 1
	}
	next := modelSettingsRow{
		ID:                 1,
		PreferredRoute:     route,
		LocalProtocol:      protocol,
		LocalBaseURL:       baseURL,
		LocalModelID:       modelID,
		LocalAPIKeyPresent: present,
		UpdatedAt:          now,
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"preferred_route", "local_protocol", "local_base_url", "local_model_id", "local_api_key_present", "updated_at"}),
	}).Create(&next).Error; err != nil {
		return LocalModelSettingsDTO{}, fmt.Errorf("persist model settings: %w", err)
	}
	return dtoFromRow(next, configured), nil
}

// LoadAPIKey returns the raw local API key for Sidecar-internal use only.
// Callers must not log or return it to Renderer.
func (s *LocalModelSettingsStore) LoadAPIKey() (string, error) {
	if s == nil || s.keychain == nil {
		return "", errors.New("keychain unavailable")
	}
	raw, err := s.keychain.Read(cloudproxy.KeychainService, KeychainLocalModelAPIKeyAccount)
	if err != nil {
		if errors.Is(err, cloudproxy.ErrKeychainNoEntry) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

func (s *LocalModelSettingsStore) loadOrSeedRow() (modelSettingsRow, error) {
	var row modelSettingsRow
	err := s.db.First(&row, 1).Error
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return modelSettingsRow{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row = modelSettingsRow{
		ID:             1,
		PreferredRoute: ModelRouteOfficial,
		UpdatedAt:      now,
	}
	if err := s.db.Create(&row).Error; err != nil {
		return modelSettingsRow{}, err
	}
	return row, nil
}

func (s *LocalModelSettingsStore) keyPresent() (bool, error) {
	if s.keychain == nil {
		return false, nil
	}
	_, err := s.keychain.Read(cloudproxy.KeychainService, KeychainLocalModelAPIKeyAccount)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, cloudproxy.ErrKeychainNoEntry) {
		return false, nil
	}
	return false, err
}

func (s *LocalModelSettingsStore) setPresentFlag(present bool) error {
	flag := 0
	if present {
		flag = 1
	}
	return s.db.Model(&modelSettingsRow{}).Where("id = 1").
		Update("local_api_key_present", flag).Error
}

func dtoFromRow(row modelSettingsRow, keyConfigured bool) LocalModelSettingsDTO {
	return LocalModelSettingsDTO{
		PreferredRoute: row.PreferredRoute,
		Local: LocalModelProfileDTO{
			Protocol:         row.LocalProtocol,
			BaseURL:          row.LocalBaseURL,
			ModelID:          row.LocalModelID,
			APIKeyConfigured: keyConfigured,
		},
		UpdatedAt: row.UpdatedAt,
	}
}

func validateLocalProfile(protocol, baseURL, modelID string) error {
	switch protocol {
	case LocalProtocolOpenAICompatible, LocalProtocolAnthropicCompatible:
	default:
		return fmt.Errorf("local.protocol must be %q or %q", LocalProtocolOpenAICompatible, LocalProtocolAnthropicCompatible)
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
	for _, r := range id {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-:/", r)) {
			return errors.New("local.model_id contains unsupported characters")
		}
	}
	return nil
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
