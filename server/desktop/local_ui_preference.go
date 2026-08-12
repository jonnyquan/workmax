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

// The three densities. "standard" is the default and, like "system" above, is
// expressed in the page as the ABSENCE of the attribute — the shipped
// stylesheet already is the standard answer.
//
// Density scales spacing and nothing else. Type stays put because a 13px row
// is 13px whatever the density is, and control heights stay put because a
// button that shrinks below its minimum stops being reliably hittable — which
// is a different thing from a window that packs more rows onto a screen.
const (
	DensityCompact     = "compact"
	DensityStandard    = "standard"
	DensityComfortable = "comfortable"
)

// AppearanceSettingsDTO is the wire shape for GET/PUT /settings/appearance.
//
// Two preferences on one route because they are read together, once, while the
// shell is serving index.html: a density that needed a second round trip would
// either hold up the first frame or land after it as a visible re-flow.
type AppearanceSettingsDTO struct {
	Appearance string `json:"appearance"`
	Density    string `json:"density"`
	UpdatedAt  string `json:"updated_at"`
}

// AppearanceSettingsPut is the PUT body. Each field is a closed vocabulary,
// and each is optional: a caller changing the theme says nothing about the
// density, and an absent field must leave the stored value alone rather than
// resetting it to the default. That is why they are pointers — "" and "not
// mentioned" are different answers here, and a plain string cannot tell them
// apart.
type AppearanceSettingsPut struct {
	Appearance *string `json:"appearance,omitempty"`
	Density    *string `json:"density,omitempty"`
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
		density    string
		updatedAt  string
	)
	row := s.db.Raw(`SELECT appearance, density, updated_at FROM w_desktop_ui_preference WHERE id = 1`).Row()
	if err := row.Scan(&appearance, &density, &updatedAt); err == nil {
		if !ValidAppearance(appearance) {
			appearance = AppearanceSystem
		}
		if !ValidDensity(density) {
			density = DensityStandard
		}
		return AppearanceSettingsDTO{Appearance: appearance, Density: density, UpdatedAt: updatedAt}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Exec(
		`INSERT OR IGNORE INTO w_desktop_ui_preference (id, appearance, density, updated_at) VALUES (1, ?, ?, ?)`,
		AppearanceSystem, DensityStandard, now,
	).Error; err != nil {
		return AppearanceSettingsDTO{}, fmt.Errorf("seed ui preference: %w", err)
	}
	return AppearanceSettingsDTO{Appearance: AppearanceSystem, Density: DensityStandard, UpdatedAt: now}, nil
}

// Put validates and stores whichever preferences the body mentions, leaving
// the others as they were. A body that mentions nothing is refused rather than
// treated as a no-op write: it is far more likely to be a caller sending the
// wrong shape than one deliberately asking for nothing to happen.
func (s *UIPreferenceStore) Put(in AppearanceSettingsPut) (AppearanceSettingsDTO, error) {
	if s == nil || s.db == nil {
		return AppearanceSettingsDTO{}, errors.New("ui preference store unavailable")
	}
	if in.Appearance == nil && in.Density == nil {
		return AppearanceSettingsDTO{}, errors.New("no preference given")
	}
	// Read first, so an unmentioned field is written back as itself. The two
	// live in one row and one UPDATE, so there is no window in which a caller
	// setting the density could observe the theme reset to a default.
	current, err := s.Get()
	if err != nil {
		return AppearanceSettingsDTO{}, err
	}
	appearance := current.Appearance
	if in.Appearance != nil {
		appearance = strings.TrimSpace(*in.Appearance)
		if !ValidAppearance(appearance) {
			return AppearanceSettingsDTO{}, fmt.Errorf(
				"appearance must be %q, %q or %q", AppearanceSystem, AppearanceLight, AppearanceDark)
		}
	}
	density := current.Density
	if in.Density != nil {
		density = strings.TrimSpace(*in.Density)
		if !ValidDensity(density) {
			return AppearanceSettingsDTO{}, fmt.Errorf(
				"density must be %q, %q or %q", DensityCompact, DensityStandard, DensityComfortable)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Exec(`
		INSERT INTO w_desktop_ui_preference (id, appearance, density, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			appearance = excluded.appearance,
			density = excluded.density,
			updated_at = excluded.updated_at`,
		appearance, density, now,
	).Error; err != nil {
		return AppearanceSettingsDTO{}, fmt.Errorf("persist ui preference: %w", err)
	}
	return AppearanceSettingsDTO{Appearance: appearance, Density: density, UpdatedAt: now}, nil
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

// ValidDensity reports whether a value is one of the three densities.
func ValidDensity(value string) bool {
	switch value {
	case DensityCompact, DensityStandard, DensityComfortable:
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
