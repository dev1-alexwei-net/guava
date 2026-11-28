package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestStartupLandingHandler(t *testing.T) {
	dashboard := func(c *gin.Context) {
		w := c.Writer
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("dashboard"))
	}

	t.Run("default opens dashboard", func(t *testing.T) {
		handler := startupLandingHandler(dashboard, false)
		rec := httptest.NewRecorder()
		engine := gin.New()
		engine.GET("/", handler)
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "dashboard" {
			t.Fatalf("response = %d %q, want dashboard", rec.Code, rec.Body.String())
		}
	})

	t.Run("account flow is first landing only", func(t *testing.T) {
		handler := startupLandingHandler(dashboard, true)
		engine := gin.New()
		engine.GET("/", handler)
		first := httptest.NewRecorder()
		engine.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/?theme=dark", nil))
		if first.Code != http.StatusFound || first.Header().Get("Location") != "/settings?theme=dark" {
			t.Fatalf("first response = %d %q, want redirect to account settings", first.Code, first.Header().Get("Location"))
		}

		second := httptest.NewRecorder()
		engine.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
		if second.Code != http.StatusOK || second.Body.String() != "dashboard" {
			t.Fatalf("second response = %d %q, want dashboard", second.Code, second.Body.String())
		}
	})
}
