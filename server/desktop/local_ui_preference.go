//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// The three appearance states. "system" is the default and is expressed in the
// page as the ABSENCE of data-theme, because the media query already is the
// system answer — see styles.css and renderer.js.
const (
	AppearanceSystem = "system"
	AppearanceLight  = "light"
	AppearanceDark   = "dark"
)

// AppearanceSettingsDTO is the wire shape for GET/PUT /settings/appearance.
type AppearanceSettingsDTO struct {
	Appearance string `json:"appearance"`
	UpdatedAt  string `json:"updated_at"`
}

// AppearanceSettingsPut is the PUT body. One field, one closed vocabulary.
type AppearanceSettingsPut struct {
	Appearance string `json:"appearance"`
}

// UIPreferenceStore persists the machine's display preferences (migration
// 0010: one row, CHECK (id = 1)).
//
// Machine-scoped on purpose — the reasoning is in the migration, and the
// consequence here is the API: Get and Put take no uid, so there is no way for
// a caller to accidentally make a theme identity-shaped later on.
type UIPreferenceStore struct {
	db *gorm.DB
}

// NewUIPreferenceStore wires the store to the local SQLite database.
func NewUIPreferenceStore(db *gorm.DB) *UIPreferenceStore {
	return &UIPreferenceStore{db: db}
}

// Get returns the stored appearance, seeding the row if a database predating
// migration 0010's INSERT somehow has none.
//
// An unrecognised stored value answers "system" rather than being passed
// through. The value's destination is an attribute on <html> written by the
// shell, so "whatever is in the column" must never be a thing the caller has
// to sanitise: the closed vocabulary is enforced on the way out as well as on
// the way in.
func (s *UIPreferenceStore) Get() (AppearanceSettingsDTO, error) {
	if s == nil || s.db == nil {
		return AppearanceSettingsDTO{}, errors.New("ui preference store unavailable")
	}
	var (
		appearance string
		updatedAt  string
	)
	row := s.db.Raw(`SELECT appearance, updated_at FROM w_desktop_ui_preference WHERE id = 1`).Row()
	if err := row.Scan(&appearance, &updatedAt); err == nil {
		if !ValidAppearance(appearance) {
			appearance = AppearanceSystem
		}
		return AppearanceSettingsDTO{Appearance: appearance, UpdatedAt: updatedAt}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Exec(
		`INSERT OR IGNORE INTO w_desktop_ui_preference (id, appearance, updated_at) VALUES (1, ?, ?)`,
		AppearanceSystem, now,
	).Error; err != nil {
		return AppearanceSettingsDTO{}, fmt.Errorf("seed ui preference: %w", err)
	}
	return AppearanceSettingsDTO{Appearance: AppearanceSystem, UpdatedAt: now}, nil
}

// Put validates and stores the appearance choice.
func (s *UIPreferenceStore) Put(in AppearanceSettingsPut) (AppearanceSettingsDTO, error) {
	if s == nil || s.db == nil {
		return AppearanceSettingsDTO{}, errors.New("ui preference store unavailable")
	}
	appearance := strings.TrimSpace(in.Appearance)
	if !ValidAppearance(appearance) {
		return AppearanceSettingsDTO{}, fmt.Errorf(
			"appearance must be %q, %q or %q", AppearanceSystem, AppearanceLight, AppearanceDark)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Exec(`
		INSERT INTO w_desktop_ui_preference (id, appearance, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			appearance = excluded.appearance,
			updated_at = excluded.updated_at`,
		appearance, now,
	).Error; err != nil {
		return AppearanceSettingsDTO{}, fmt.Errorf("persist ui preference: %w", err)
	}
	return AppearanceSettingsDTO{Appearance: appearance, UpdatedAt: now}, nil
}

// ValidAppearance reports whether a value is one of the three states.
func ValidAppearance(value string) bool {
	switch value {
	case AppearanceSystem, AppearanceLight, AppearanceDark:
		return true
	default:
		return false
	}
}

// DecodeAppearanceSettingsPut strictly decodes the PUT body: unknown fields
// are rejected and so is anything trailing the object, the same rule
// DecodeLocalModelSettingsPut applies.
func DecodeAppearanceSettingsPut(raw []byte) (AppearanceSettingsPut, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in AppearanceSettingsPut
	if err := dec.Decode(&in); err != nil {
		return AppearanceSettingsPut{}, err
	}
	if dec.More() {
		return AppearanceSettingsPut{}, errors.New("trailing json content")
	}
	return in, nil
}
