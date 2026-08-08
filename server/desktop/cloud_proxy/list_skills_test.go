//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClient_ListSkills_HappyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/work-agent/skills" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth header: got %q", got)
		}
		if r.Header.Get("X-WorkMax-Client") != "desktop" {
			t.Errorf("missing X-WorkMax-Client")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"code": 1,
			"message": "ok",
			"data": {
				"items": [
					{"agentMode":"ppt","version":"2.0.0","hasQuestionForm":true,
					 "hasDirectionsFallback":true,"hasPostScripts":true,
					 "artifacts":{"primaryType":"deck","outputTypes":["pptx","pdf"],"previewTypes":["deck","pdf"],"exportTargets":["pptx","pdf"],"critiqueAnchors":["functionality","hierarchy"]},
					 "labelKey":"WorkAgent.modeSelector.modes.ppt.name",
					 "descriptionKey":"WorkAgent.modeSelector.modes.ppt.description"},
					{"agentMode":"flashCard","version":"1.0.0","hasQuestionForm":false,
					 "hasDirectionsFallback":false,"hasPostScripts":false,
					 "labelKey":"WorkAgent.modeSelector.modes.flashCard.name",
					 "descriptionKey":"WorkAgent.modeSelector.modes.flashCard.description"}
				],
				"count": 2
			}
		}`)
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	items, err := c.ListSkills(context.Background(), "tok")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("count: got %d, want 2", len(items))
	}
	if items[0].AgentMode != "ppt" || items[0].Version != "2.0.0" {
		t.Errorf("first item: %+v", items[0])
	}
	if !items[0].HasQuestionForm {
		t.Errorf("hasQuestionForm should round-trip true")
	}
	if items[0].Artifacts == nil || items[0].Artifacts.PrimaryType != "deck" {
		t.Errorf("artifacts should round-trip, got %+v", items[0].Artifacts)
	}
	if items[0].LabelKey != "WorkAgent.modeSelector.modes.ppt.name" {
		t.Errorf("labelKey: %q", items[0].LabelKey)
	}
}

func TestClient_ListSkills_HTTPError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "boom")
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListSkills(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err message: %v", err)
	}
}

func TestClient_ListSkills_CloudCodeNonSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0,"message":"forbidden","data":{}}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListSkills(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "code 0") {
		t.Errorf("err message: %v", err)
	}
}

func TestClient_ListSkills_CloudCodeNonSuccessRedactsMessage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0,"message":"Authorization: Bearer bearer-secret access_token=access-secret https://user:pass@example.com/path","data":{}}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListSkills(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range []string{"bearer-secret", "access-secret", "user:pass"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("ListSkills error leaked %q: %v", secret, err)
		}
	}
	if strings.Contains(err.Error(), "Authorization") || strings.Contains(err.Error(), "example.com") {
		t.Fatalf("error included arbitrary upstream message: %v", err)
	}
}

func TestClient_ListSkills_BadJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `not json`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListSkills(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid JSON response") {
		t.Errorf("err message: %v", err)
	}
}

func TestClient_ListSkills_UnauthorizedReturnsClosedSentinel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `bare-secret-that-must-not-enter-the-error`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListSkills(context.Background(), "tok")
	if !errors.Is(err, ErrListSkillsAuthExpired) {
		t.Fatalf("ListSkills error = %v, want ErrListSkillsAuthExpired", err)
	}
	if strings.Contains(err.Error(), "bare-secret") {
		t.Fatalf("ListSkills 401 leaked response body: %v", err)
	}
}

func TestClient_ListSkills_SessionChangeCancelsInflightRequest(t *testing.T) {
	started := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			close(upstreamCanceled)
		case <-releaseUpstream:
		}
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseUpstream) }) })

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.ListSkills(ctx, "access-token")
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for skills request")
	}
	cancel(ErrSessionChanged)

	select {
	case err := <-result:
		if !errors.Is(err, ErrSessionChanged) {
			t.Fatalf("ListSkills error = %v, want ErrSessionChanged", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("skills request did not stop after session change")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("skills session change did not cancel upstream HTTP")
	}
}
