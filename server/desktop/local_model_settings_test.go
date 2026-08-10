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

func openModelSettingsDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Unique DSN per test: shared memory cache would leak rows across tests.
	dsn := "file:model-settings-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

func TestLocalModelSettingsStore_DefaultOfficialNoKeyLeak(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	store := NewLocalModelSettingsStore(db, kc)

	dto, err := store.Get(localSingleUserUID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dto.PreferredRoute != ModelRouteOfficial {
		t.Fatalf("route = %q, want official", dto.PreferredRoute)
	}
	if dto.Local.APIKeyConfigured {
		t.Fatal("api key should not be configured")
	}
	raw, _ := json.Marshal(dto)
	if bytes.Contains(raw, []byte("api_key")) && !bytes.Contains(raw, []byte("api_key_configured")) {
		t.Fatalf("unexpected api_key field in dto json: %s", raw)
	}
	if bytes.Contains(raw, []byte(`"api_key"`)) {
		t.Fatalf("secret field api_key must not appear: %s", raw)
	}
}

func TestLocalModelSettingsStore_PutLocalLoopbackWithKey(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	store := NewLocalModelSettingsStore(db, kc)

	key := "sk-test-local-secret"
	dto, err := store.Put(localSingleUserUID, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
			APIKey:   &key,
		},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if dto.PreferredRoute != ModelRouteLocal || !dto.Local.APIKeyConfigured {
		t.Fatalf("unexpected dto: %+v", dto)
	}
	got, err := store.LoadAPIKey(localSingleUserUID)
	if err != nil || got != key {
		t.Fatalf("LoadAPIKey = %q, %v", got, err)
	}
	// SQLite must not store the secret.
	var blob string
	if err := db.Raw(`SELECT local_base_url || local_model_id FROM w_desktop_model_settings WHERE id = 1`).Scan(&blob).Error; err != nil {
		t.Fatalf("scan: %v", err)
	}
	if strings.Contains(blob, key) {
		t.Fatal("api key leaked into sqlite row")
	}
}

func TestLocalModelSettingsStore_RejectsRemoteHTTP(t *testing.T) {
	db := openModelSettingsDB(t)
	store := NewLocalModelSettingsStore(db, newMemKeychain())
	_, err := store.Put(localSingleUserUID, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://evil.example/v1",
			ModelID:  "x",
		},
	})
	if err == nil {
		t.Fatal("expected rejection of non-loopback http")
	}
}

func TestLocalModelSettingsHTTP_GetPutNoSecretEcho(t *testing.T) {
	db := openModelSettingsDB(t)
	kc := newMemKeychain()
	store := NewLocalModelSettingsStore(db, kc)
	token := "test-local-token-32chars-minimum!!"
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     token,
		DB:             db,
		DeviceID:       "device-test",
		ModelSettings:  store,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.httpServer.Handler)
	t.Cleanup(ts.Close)

	// Default GET
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/settings/model-route", nil)
	req.Header.Set("X-Local-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", res.StatusCode)
	}
	var got LocalModelSettingsDTO
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PreferredRoute != ModelRouteOfficial {
		t.Fatalf("route = %q", got.PreferredRoute)
	}

	body := `{"preferred_route":"local","local":{"protocol":"openai_compatible","base_url":"http://127.0.0.1:11434/v1","model_id":"llama3.2","api_key":"super-secret-key"}}`
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/settings/model-route", strings.NewReader(body))
	req.Header.Set("X-Local-Token", token)
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer res.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", res.StatusCode, raw.String())
	}
	if strings.Contains(raw.String(), "super-secret-key") {
		t.Fatalf("response echoed secret: %s", raw.String())
	}
	if strings.Contains(raw.String(), `"api_key"`) {
		t.Fatalf("response includes api_key field: %s", raw.String())
	}
	var put LocalModelSettingsDTO
	if err := json.Unmarshal(raw.Bytes(), &put); err != nil {
		t.Fatalf("unmarshal put: %v", err)
	}
	if !put.Local.APIKeyConfigured || put.PreferredRoute != ModelRouteLocal {
		t.Fatalf("put dto: %+v", put)
	}

	// Browser Origin must be rejected by route policy.
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/settings/model-route", nil)
	req.Header.Set("X-Local-Token", token)
	req.Header.Set("Origin", "https://evil.example")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET origin: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("expected Origin rejection")
	}

}
