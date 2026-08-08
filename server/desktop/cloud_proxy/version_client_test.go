//go:build desktop

package cloud_proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_GetVersion_HappyPath(t *testing.T) {
	var gotPath, gotClientHeader, gotClientVersionHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotClientHeader = r.Header.Get("X-WorkMax-Client")
		gotClientVersionHeader = r.Header.Get("X-WorkMax-Client-Version")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"min_supported":"0.0.5","latest_recommended":"0.1.0"}`)
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	info, err := c.GetVersion(context.Background())
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if info.MinSupported != "0.0.5" {
		t.Errorf("MinSupported: %q", info.MinSupported)
	}
	if info.LatestRecommended != "0.1.0" {
		t.Errorf("LatestRecommended: %q", info.LatestRecommended)
	}
	if gotPath != CloudRouteVersion {
		t.Errorf("path: got %q, want %q", gotPath, CloudRouteVersion)
	}
	if gotClientHeader != "desktop" {
		t.Errorf("X-WorkMax-Client: %q", gotClientHeader)
	}
	if gotClientVersionHeader == "" {
		t.Errorf("X-WorkMax-Client-Version missing")
	}
}

func TestClient_GetVersion_SanitizesReleaseNotesURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "https", raw: "https://workmax.app/desktop/changelog", want: "https://workmax.app/desktop/changelog"},
		{name: "http", raw: "http://localhost:3000/desktop/changelog", want: "http://localhost:3000/desktop/changelog"},
		{name: "javascript", raw: "javascript:alert(1)", want: ""},
		{name: "credentials", raw: "https://user:pass@workmax.app/desktop/changelog", want: ""},
		{name: "access token query", raw: "https://workmax.app/desktop/changelog?access_token=secret", want: ""},
		{name: "client secret query", raw: "https://workmax.app/desktop/changelog?client_secret=secret", want: ""},
		{name: "compact apikey query", raw: "https://workmax.app/desktop/changelog?apikey=secret", want: ""},
		{name: "relative", raw: "/desktop/changelog", want: ""},
		{name: "blank", raw: "   ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(
					w,
					`{"min_supported":"0.0.5","latest_recommended":"0.1.0","release_notes_url":%q}`,
					tc.raw,
				)
			}))
			t.Cleanup(upstream.Close)

			c := NewClient(upstream.URL)
			c.HTTPClient = upstream.Client()
			info, err := c.GetVersion(context.Background())
			if err != nil {
				t.Fatalf("GetVersion: %v", err)
			}
			if info.ReleaseNotesURL != tc.want {
				t.Fatalf("ReleaseNotesURL: got %q, want %q", info.ReleaseNotesURL, tc.want)
			}
		})
	}
}

func TestClient_GetVersion_BadStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "internal_error")
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.GetVersion(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestClient_GetVersion_MalformedJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{not json`)
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.GetVersion(context.Background())
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestClient_GetVersion_RequiresVersionFloorFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing min_supported",
			body: `{"latest_recommended":"0.1.0"}`,
			want: "missing min_supported",
		},
		{
			name: "missing latest_recommended",
			body: `{"min_supported":"0.0.5"}`,
			want: "missing latest_recommended",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.body)
			}))
			t.Cleanup(upstream.Close)

			c := NewClient(upstream.URL)
			c.HTTPClient = upstream.Client()
			_, err := c.GetVersion(context.Background())
			if err == nil {
				t.Fatal("expected missing field error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}
