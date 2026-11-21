package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageReferencesEmbeddedBootstrapStylesheet(t *testing.T) {
	h := makeTestHandler(t, t.TempDir())

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("GET / status=%d body=%s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), `href="/static/bootstrap.min.css"`) {
		t.Fatal("rendered page does not reference the local Bootstrap stylesheet")
	}
	if strings.Contains(page.Body.String(), "https://") || strings.Contains(page.Body.String(), "<script") {
		t.Fatal("rendered page references a remote asset or JavaScript")
	}

	css := httptest.NewRecorder()
	h.ServeHTTP(css, httptest.NewRequest(http.MethodGet, "/static/bootstrap.min.css", nil))
	if css.Code != http.StatusOK {
		t.Fatalf("stylesheet status=%d body=%s", css.Code, css.Body.String())
	}
	if got := css.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("stylesheet Content-Type=%q", got)
	}
	if !strings.Contains(css.Body.String(), "Bootstrap  v5.3.8") {
		t.Fatal("served stylesheet is not the pinned Bootstrap version")
	}

	license, err := pageFiles.ReadFile("static/BOOTSTRAP-LICENSE.txt")
	if err != nil {
		t.Fatalf("read embedded Bootstrap license: %v", err)
	}
	if !strings.Contains(string(license), "The MIT License") {
		t.Fatal("embedded Bootstrap license does not contain the MIT notice")
	}
}
