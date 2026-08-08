//go:build desktop

package cloud_proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// hit fires a GET against the loopback server and returns the body
// for assertion. Used by tests to simulate the browser following
// the OAuth redirect.
func hit(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("hit %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

func TestLoopback_HappyCallback(t *testing.T) {
	s, err := NewLoopbackCallbackServer()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	s.Start()

	if s.Port() == 0 {
		t.Fatal("Port returned 0")
	}
	if !strings.HasPrefix(s.RedirectURI(), "http://127.0.0.1:") ||
		!strings.HasSuffix(s.RedirectURI(), "/oauth/callback") {
		t.Errorf("RedirectURI shape: got %q", s.RedirectURI())
	}

	// Fire the callback like workmax.app would after consent approval.
	url := fmt.Sprintf("%s?code=auth-code-abc&state=state-xyz", s.RedirectURI())
	body := hit(t, url)
	if !strings.Contains(body, "Signed in") {
		t.Errorf("success HTML missing: %s", body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := s.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != "auth-code-abc" {
		t.Errorf("Code: got %q, want auth-code-abc", res.Code)
	}
	if res.State != "state-xyz" {
		t.Errorf("State: got %q, want state-xyz", res.State)
	}
	if res.ErrParam != "" {
		t.Errorf("ErrParam: got %q, want empty", res.ErrParam)
	}
}

func TestLoopback_DenyCallback(t *testing.T) {
	s, err := NewLoopbackCallbackServer()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	s.Start()

	url := fmt.Sprintf("%s?error=access_denied&error_description=user+rejected&state=xyz", s.RedirectURI())
	body := hit(t, url)
	if !strings.Contains(body, "Sign-in was not completed") {
		t.Errorf("error HTML missing: %s", body)
	}
	if strings.Contains(body, "Signed in to workmax") {
		t.Errorf("denied callback must not render success HTML: %s", body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := s.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ErrParam != "access_denied" {
		t.Errorf("ErrParam: got %q", res.ErrParam)
	}
	if res.Code != "" {
		t.Error("denied callback must not carry code")
	}
}

func TestLoopback_AmbiguousCallbackRendersErrorPage(t *testing.T) {
	s, err := NewLoopbackCallbackServer()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	s.Start()

	body := hit(t, fmt.Sprintf("%s?code=first&code=second&state=s", s.RedirectURI()))
	if !strings.Contains(body, "Sign-in was not completed") {
		t.Errorf("ambiguous callback error HTML missing: %s", body)
	}
	if strings.Contains(body, "Signed in to workmax") {
		t.Errorf("ambiguous callback must not render success HTML: %s", body)
	}
}

func TestCallbackResultFromQueryRejectsAmbiguousParams(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "duplicate code", raw: "code=first&code=second&state=s"},
		{name: "duplicate state", raw: "code=c&state=first&state=second"},
		{name: "duplicate error", raw: "error=access_denied&error=server_error&state=s"},
		{name: "code and error", raw: "code=c&error=access_denied&state=s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			got := callbackResultFromQuery(q)
			if got.ErrParam != "invalid_request" {
				t.Fatalf("ErrParam: got %q, want invalid_request", got.ErrParam)
			}
			if got.Code != "" {
				t.Fatalf("ambiguous callback must not expose code, got %q", got.Code)
			}
			if tc.name != "duplicate state" && got.State != "s" {
				t.Fatalf("unambiguous state should be preserved, got %q", got.State)
			}
			if tc.name == "duplicate state" && got.State != "" {
				t.Fatalf("duplicate state must not be preserved, got %q", got.State)
			}
		})
	}
}

func TestLoopback_Timeout(t *testing.T) {
	s, err := NewLoopbackCallbackServer()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	s.Start()

	// No callback. Wait should time out via ctx.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = s.Wait(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestLoopback_DuplicateCallbacksIgnored(t *testing.T) {
	s, err := NewLoopbackCallbackServer()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	s.Start()

	hit(t, fmt.Sprintf("%s?code=first&state=s", s.RedirectURI()))
	hit(t, fmt.Sprintf("%s?code=second&state=s", s.RedirectURI()))

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	res, _ := s.Wait(ctx)
	if res.Code != "first" {
		t.Errorf("Code: got %q, want first (second should be ignored)", res.Code)
	}
}

func TestLoopback_404OnOtherPaths(t *testing.T) {
	s, err := NewLoopbackCallbackServer()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	s.Start()

	resp, _ := http.Get(fmt.Sprintf("http://127.0.0.1:%d/some/other/path", s.Port()))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-callback path: status %d, want 404", resp.StatusCode)
	}
}
