package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(SecurityHeaders())
	engine.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestRequestValueHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/", func(c *gin.Context) {
		if got := QueryValue(c, "item"); got != "query" {
			t.Errorf("QueryValue = %q, want query", got)
		}
		if got := FormValue(c, "item"); got != "body" {
			t.Errorf("FormValue = %q, want body to take precedence over query", got)
		}
		if got := HeaderValue(c, "x-example"); got != "header" {
			t.Errorf("HeaderValue = %q, want header", got)
		}
		mediaType, params, err := ParseMediaTypeHeader(c, "Content-Type")
		if err != nil {
			t.Errorf("ParseMediaTypeHeader error = %v", err)
		}
		if mediaType != "application/x-www-form-urlencoded" || params["charset"] != "utf-8" {
			t.Errorf("parsed content type = %q, %v", mediaType, params)
		}
		c.Status(http.StatusNoContent)
	})

	form := url.Values{"item": {"body"}}
	request := httptest.NewRequest(http.MethodPost, "/?item=query", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	request.Header.Set("X-Example", "header")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestParseMediaTypeHeaderRejectsMalformedValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var parseErr error
	engine := gin.New()
	engine.GET("/", func(c *gin.Context) {
		_, _, parseErr = ParseMediaTypeHeader(c, "Content-Type")
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Content-Type", "text/html; charset")
	engine.ServeHTTP(httptest.NewRecorder(), request)
	if parseErr == nil {
		t.Fatal("expected malformed media type to fail parsing")
	}
}

func TestRequestValueHelpersHandleNilContext(t *testing.T) {
	if got := FormValue(nil, "field"); got != "" {
		t.Fatalf("FormValue(nil) = %q, want empty", got)
	}
	if got := QueryValue(nil, "field"); got != "" {
		t.Fatalf("QueryValue(nil) = %q, want empty", got)
	}
	if got := HeaderValue(nil, "field"); got != "" {
		t.Fatalf("HeaderValue(nil) = %q, want empty", got)
	}
}
