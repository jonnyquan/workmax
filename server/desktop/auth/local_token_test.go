//go:build desktop

package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireLocalToken_AllowsCorrectToken(t *testing.T) {
	router := newLocalTokenTestRouter("secret")
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set(HeaderLocalToken, "secret")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestRequireLocalToken_RejectsMissingToken(t *testing.T) {
	router := newLocalTokenTestRouter("secret")
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assertForbiddenLocalTokenError(t, rec)
}

func TestRequireLocalToken_RejectsWrongToken(t *testing.T) {
	router := newLocalTokenTestRouter("secret")

	for _, token := range []string{
		"wrong",
		"s",
		strings.Repeat("x", 512),
	} {
		t.Run(fmt.Sprintf("len_%d", len(token)), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set(HeaderLocalToken, token)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assertForbiddenLocalTokenError(t, rec)
		})
	}
}

func TestRequireLocalToken_RejectsDuplicateTokenHeaders(t *testing.T) {
	router := newLocalTokenTestRouter("secret")

	for _, tokens := range [][]string{
		{"secret", "wrong"},
		{"wrong", "secret"},
		{"secret", "secret"},
	} {
		t.Run(strings.Join(tokens, "_"), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			for _, token := range tokens {
				req.Header.Add(HeaderLocalToken, token)
			}
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assertForbiddenLocalTokenError(t, rec)
		})
	}
}

func TestRequireLocalToken_PanicsForEmptyExpectedToken(t *testing.T) {
	defer func() {
		if got := recover(); got == nil {
			t.Fatal("RequireLocalToken did not panic")
		}
	}()

	_ = RequireLocalToken("")
}

func newLocalTokenTestRouter(expected string) http.Handler {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequireLocalToken(expected))
	router.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func assertForbiddenLocalTokenError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := "missing or invalid " + HeaderLocalToken
	if body.Error != want {
		t.Fatalf("error: got %q, want %q", body.Error, want)
	}
}
