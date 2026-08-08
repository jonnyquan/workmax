//go:build desktop

package desktop

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

func bootServerVersionRouter(t *testing.T, upstreamBody string, upstreamStatus int) *gin.Engine {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstreamStatus)
		io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, nil, db)
	srv := &Server{
		cfg: ServerConfig{
			SidecarVersion: "0.1.0-p1-ea",
			DB:             db,
			Proxy:          proxy,
		},
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.GET("/system/server-version", srv.handleServerVersion)
	return router
}

func TestHandleServerVersion_ProxiesCloudFloor(t *testing.T) {
	router := bootServerVersionRouter(t,
		`{"min_supported":"0.1.0","latest_recommended":"0.2.0","release_notes_url":"https://workmax.app/desktop/changelog"}`,
		http.StatusOK,
	)

	req := httptest.NewRequest(http.MethodGet, "/system/server-version", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var got serverVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MinSupported != "0.1.0" || got.LatestRecommended != "0.2.0" {
		t.Fatalf("wrong cloud floor: %+v", got)
	}
	if got.SidecarVersion != "0.1.0-p1-ea" {
		t.Fatalf("sidecar_version: %q", got.SidecarVersion)
	}
	if got.ReleaseNotesURL != "https://workmax.app/desktop/changelog" {
		t.Fatalf("release_notes_url: %q", got.ReleaseNotesURL)
	}
}

func TestHandleServerVersion_MissingCloudFloorReturnsBadGateway(t *testing.T) {
	router := bootServerVersionRouter(t,
		`{"latest_recommended":"0.2.0"}`,
		http.StatusOK,
	)

	req := httptest.NewRequest(http.MethodGet, "/system/server-version", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: %d, want 502; body: %s", rec.Code, rec.Body.String())
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("body should be JSON: %s", rec.Body.String())
	}
}

func TestHandleServerVersion_NoProxyConfigured(t *testing.T) {
	srv := &Server{cfg: ServerConfig{SidecarVersion: "test"}}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.GET("/system/server-version", srv.handleServerVersion)

	req := httptest.NewRequest(http.MethodGet, "/system/server-version", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d, want 503", rec.Code)
	}
}
