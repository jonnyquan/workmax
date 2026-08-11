//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	migrationsdesktop "server/desktop/migrations_desktop"
)

func openUIPreferenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:ui-preference-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// A machine that has never been asked follows the system.
func TestUIPreferenceStore_DefaultsToSystem(t *testing.T) {
	store := NewUIPreferenceStore(openUIPreferenceDB(t))
	dto, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dto.Appearance != AppearanceSystem {
		t.Fatalf("appearance = %q, want %q", dto.Appearance, AppearanceSystem)
	}
	if dto.UpdatedAt == "" {
		t.Fatal("updated_at must be populated")
	}
}

// The bug this whole change exists for: a choice has to be readable by the
// NEXT process, not just by the one that made it.
func TestUIPreferenceStore_ChoiceSurvivesANewStore(t *testing.T) {
	db := openUIPreferenceDB(t)
	if _, err := NewUIPreferenceStore(db).Put(AppearanceSettingsPut{Appearance: AppearanceDark}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	dto, err := NewUIPreferenceStore(db).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dto.Appearance != AppearanceDark {
		t.Fatalf("appearance = %q, want dark after a relaunch", dto.Appearance)
	}
}

func TestUIPreferenceStore_RejectsAnythingOutsideTheThreeStates(t *testing.T) {
	store := NewUIPreferenceStore(openUIPreferenceDB(t))
	for _, bad := range []string{"", "System", "midnight", `dark"><script>`, "light dark"} {
		if _, err := store.Put(AppearanceSettingsPut{Appearance: bad}); err == nil {
			t.Fatalf("Put(%q) was accepted; the vocabulary is closed", bad)
		}
	}
	dto, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dto.Appearance != AppearanceSystem {
		t.Fatalf("a rejected write changed the stored value to %q", dto.Appearance)
	}
}

// The stored value becomes an attribute on <html>, so a row that was edited by
// hand (or by an older build) must not be handed back for the shell to write
// into markup.
func TestUIPreferenceStore_UnrecognisedStoredValueReadsAsSystem(t *testing.T) {
	db := openUIPreferenceDB(t)
	if err := db.Exec(`UPDATE w_desktop_ui_preference SET appearance = ? WHERE id = 1`,
		`dark" onload="alert(1)`).Error; err != nil {
		t.Fatalf("seed hostile row: %v", err)
	}
	dto, err := NewUIPreferenceStore(db).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dto.Appearance != AppearanceSystem {
		t.Fatalf("appearance = %q, want system", dto.Appearance)
	}
}

func TestDecodeAppearanceSettingsPut_IsStrict(t *testing.T) {
	if _, err := DecodeAppearanceSettingsPut([]byte(`{"appearance":"dark"}`)); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	for _, bad := range []string{
		`{"appearance":"dark","theme":"dark"}`,
		`{"appearance":"dark"}{"appearance":"light"}`,
		`{"appearance":"dark"} trailing`,
		`[]`,
	} {
		if _, err := DecodeAppearanceSettingsPut([]byte(bad)); err == nil {
			t.Fatalf("DecodeAppearanceSettingsPut(%q) was accepted", bad)
		}
	}
}

func TestAppearanceHTTP_GetPutAndValidation(t *testing.T) {
	db := openUIPreferenceDB(t)
	token := "test-local-token-32chars-minimum!!"
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     token,
		DB:             db,
		DeviceID:       "device-test",
		UIPreferences:  NewUIPreferenceStore(db),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.httpServer.Handler)
	t.Cleanup(ts.Close)

	get := func() AppearanceSettingsDTO {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/settings/appearance", nil)
		req.Header.Set("X-Local-Token", token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET status = %d", res.StatusCode)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control = %q; a cached theme is a stale first frame", res.Header.Get("Cache-Control"))
		}
		var dto AppearanceSettingsDTO
		if err := json.NewDecoder(res.Body).Decode(&dto); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return dto
	}
	put := func(body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/settings/appearance", strings.NewReader(body))
		req.Header.Set("X-Local-Token", token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer res.Body.Close()
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(res.Body)
		return res.StatusCode, raw.String()
	}

	if got := get().Appearance; got != AppearanceSystem {
		t.Fatalf("default appearance = %q", got)
	}
	if status, body := put(`{"appearance":"dark"}`); status != http.StatusOK {
		t.Fatalf("PUT dark = %d %s", status, body)
	}
	if got := get().Appearance; got != AppearanceDark {
		t.Fatalf("appearance after PUT = %q, want dark", got)
	}
	for _, bad := range []string{
		`{"appearance":"midnight"}`,
		`{"appearance":"dark","extra":1}`,
		`{}`,
	} {
		if status, _ := put(bad); status != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400", bad, status)
		}
	}
	if got := get().Appearance; got != AppearanceDark {
		t.Fatalf("a rejected write changed the stored value to %q", got)
	}

	// The route is not identity-scoped, and the perimeter still applies.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/settings/appearance", nil)
	req.Header.Set("X-Local-Token", token)
	req.Header.Set("Origin", "https://attacker.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET with Origin: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("Origin-bearing GET = %d, want 403", res.StatusCode)
	}
}

// Without a store wired the route is present and fail-closed, the same shape
// every other optional dependency takes.
func TestAppearanceHTTP_UnavailableWithoutAStore(t *testing.T) {
	db := openUIPreferenceDB(t)
	token := "test-local-token-32chars-minimum!!"
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     token,
		DB:             db,
		DeviceID:       "device-test",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.httpServer.Handler)
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/settings/appearance", nil)
	req.Header.Set("X-Local-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
}
